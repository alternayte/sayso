//go:build contract

package models_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/runner"
)

func TestSCN_MOD_002_NoToolCallsCannotBeUsed(t *testing.T) {
	e := newEnv(t)
	e.provider(t, models.ProviderInput{Name: "plain-only", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-plain-0001"),
		Models: []string{fakemodel.NoToolsPrefix + "model"}})
	var sm models.Smoke
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/providers/plain-only/test", map[string]string{}, &sm); code != http.StatusOK {
		t.Fatalf("test: %d", code)
	}
	if !sm.Passed(models.CheckPlain) || !sm.Passed(models.CheckJSON) || sm.Passed(models.CheckTools) {
		t.Fatalf("smoke %+v", sm)
	}
	target := []models.Target{{Provider: "plain-only", Model: fakemodel.NoToolsPrefix + "model"}}
	b, _ := json.Marshal(map[string]any{"targets": target})
	req, _ := http.NewRequest(http.MethodPut, e.Base+"/api/v1/profiles/review", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(msg), "tool call check") {
		t.Fatalf("profile with a provider without tool calls: %d %s", resp.StatusCode, msg)
	}
	// The same provider with a model that does tool calls passes and can be used.
	e.tested(t, models.ProviderInput{Name: "with-tools", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-tools-0001"), Models: []string{"fake-large"}})
	target = []models.Target{{Provider: "with-tools", Model: "fake-large"}}
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/profiles/review", map[string]any{"targets": target}, nil); code != http.StatusOK {
		t.Fatalf("profile with a tested provider: %d", code)
	}
}

// requests for each API type: path and a streaming body.
var apiCalls = map[string]struct{ path, body string }{
	models.OpenAICompletions: {"chat/completions", `{"model":"fake-large","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Say hello."}]}`},
	models.OpenAIResponses:   {"responses", `{"model":"fake-large","stream":true,"input":"Say hello."}`},
	models.AnthropicMessages: {"messages", `{"model":"fake-large","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"Say hello."}]}`},
}

func TestSCN_MOD_005_ProxySwapsCredentialsAndStreams(t *testing.T) {
	e := newEnv(t)
	styles := []struct {
		style, header, want string
	}{
		{models.AuthBearer, "Authorization", "Bearer KEY"},
		{models.AuthXAPIKey, "X-Api-Key", "KEY"},
		{models.AuthAPIKey, "Api-Key", "KEY"},
		{models.AuthHeader, "X-Gateway-Key", "KEY"},
	}
	var ms []runner.Model
	type pv struct{ name, api, header, want, key string }
	var pvs []pv
	for _, st := range styles {
		for api := range apiCalls {
			name := strings.ReplaceAll(fmt.Sprintf("%s-%s", strings.ReplaceAll(st.style, "_", "-"), api), "openai-", "oai-")
			k := "sk-" + name + "-secret"
			in := models.ProviderInput{Name: name, APIType: api, AuthStyle: st.style, Key: key(k), Models: []string{"fake-large"},
				Headers: map[string]string{"X-Team": "acme-review"}}
			if st.style == models.AuthHeader {
				in.AuthHeader = st.header
			}
			e.provider(t, in)
			ms = append(ms, runner.Model{Provider: name, Model: "fake-large"})
			pvs = append(pvs, pv{name, api, st.header, strings.Replace(st.want, "KEY", k, 1), k})
		}
	}
	_, tok := e.run(t, 0, ms...)
	for _, p := range pvs {
		e.model.Reset()
		call := apiCalls[p.api]
		// The agent sends the run token the way its SDK sends a key.
		h := http.Header{}
		switch p.api {
		case models.AnthropicMessages:
			h.Set("x-api-key", tok)
		default:
			h.Set("Authorization", "Bearer "+tok)
		}
		resp, got := e.call(t, p.name, call.path+"?trace=1", h, call.body)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("%s: %d %s", p.name, resp.StatusCode, got)
		}
		reqs := e.model.Requests()
		if len(reqs) != 1 {
			t.Fatalf("%s: %d upstream requests", p.name, len(reqs))
		}
		up := reqs[0]
		if up.Path != "/v1/"+call.path || up.Query != "trace=1" || up.Body != call.body {
			t.Fatalf("%s: upstream %s?%s body %s", p.name, up.Path, up.Query, up.Body)
		}
		if v := up.Header.Get(p.header); v != p.want {
			t.Fatalf("%s: header %s = %q", p.name, p.header, v)
		}
		if up.Header.Get("X-Team") != "acme-review" {
			t.Fatalf("%s: extra header missing", p.name)
		}
		for k, vs := range up.Header {
			for _, v := range vs {
				if strings.Contains(v, tok) {
					t.Fatalf("%s: upstream header %s carries the run token", p.name, k)
				}
			}
		}
		// The stream reaches the agent unchanged.
		e.model.Reset()
		direct, err := http.Post(e.url+"/v1/"+call.path, "application/json", strings.NewReader(call.body))
		if err != nil {
			t.Fatal(err)
		}
		want := new(strings.Builder)
		_, _ = fmt.Fprint(want, readAll(t, direct))
		if string(got) != want.String() {
			t.Fatalf("%s: stream changed:\n got %q\nwant %q", p.name, got, want.String())
		}
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func TestSCN_MOD_006_ProxyRefusesOtherModelsAndEndedRuns(t *testing.T) {
	e := newEnv(t)
	e.provider(t, models.ProviderInput{Name: "main", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-main-0001"), Models: []string{"fake-large"}})
	run, tok := e.run(t, 0, runner.Model{Provider: "main", Model: "fake-large"})
	h := http.Header{"Authorization": {"Bearer " + tok}}
	if resp, b := e.call(t, "main", "chat/completions", h, `{"model":"fake-small","messages":[]}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("other model: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.call(t, "main", "chat/completions", h, `{"model":"fake-large","messages":[{"role":"user","content":"hi"}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed model: %d", resp.StatusCode)
	}
	if _, err := runner.New(e.Deps.Pool).FinishRun(context.Background(), runner.FinishRunParams{ID: run.ID, Status: "done", ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	// A second run is active; the token of the ended run still fails.
	e.run(t, 0, runner.Model{Provider: "main", Model: "fake-large"})
	if resp, _ := e.call(t, "main", "chat/completions", h, `{"model":"fake-large","messages":[]}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ended run token: %d", resp.StatusCode)
	}
	if resp, _ := e.call(t, "main", "chat/completions", http.Header{"Authorization": {"Bearer sk-main-0001"}}, `{"model":"fake-large","messages":[]}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("provider key as token: %d", resp.StatusCode)
	}
	if n := len(e.model.Requests()); n != 1 {
		t.Fatalf("the fake got %d requests, want 1", n)
	}
}

func TestSCN_MOD_007_UsageAndCost(t *testing.T) {
	e := newEnv(t)
	for _, api := range []string{models.OpenAICompletions, models.OpenAIResponses, models.AnthropicMessages} {
		name := strings.ReplaceAll(api, "openai-", "oai-")
		e.provider(t, models.ProviderInput{Name: name, APIType: api, AuthStyle: models.AuthBearer, Key: key("sk-usage-" + name), Models: []string{"fake-large"}})
		run, tok := e.run(t, 0, runner.Model{Provider: name, Model: "fake-large", PriceIn: 3, PriceOut: 15, PriceCacheRead: 0.3})
		call := apiCalls[api]
		for _, stream := range []bool{true, false} {
			body := call.body
			if !stream {
				body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
			}
			resp, b := e.call(t, name, call.path, http.Header{"Authorization": {"Bearer " + tok}}, body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s stream=%v: %d %s", api, stream, resp.StatusCode, b)
			}
		}
		rows, err := e.Deps.Pool.Query(context.Background(), `SELECT tokens_in, tokens_out, cost_usd::float8 FROM model_usage WHERE run_id = $1 ORDER BY id`, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		var total float64
		for rows.Next() {
			var in, out int64
			var cost float64
			_ = rows.Scan(&in, &out, &cost)
			if in <= 0 || out <= 0 {
				t.Fatalf("%s: usage row %d without tokens", api, n)
			}
			want := (float64(in)*3 + float64(out)*15) / 1e6
			if diff := cost - want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("%s: cost %.9f, want %.9f", api, cost, want)
			}
			total += cost
			n++
		}
		rows.Close()
		if n != 2 {
			t.Fatalf("%s: %d usage rows", api, n)
		}
		var runCost float64
		if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT cost_usd::float8 FROM agent_runs WHERE id = $1`, run.ID).Scan(&runCost); err != nil {
			t.Fatal(err)
		}
		if diff := runCost - total; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("%s: run cost %.6f, usage sum %.6f", api, runCost, total)
		}
		if c := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = 'model_turn' AND tokens_out > 0`, run.ID); c != 2 {
			t.Fatalf("%s: %d model_turn events", api, c)
		}
	}
}

func TestSCN_MOD_008_RetryBeforeFirstByteOnly(t *testing.T) {
	e := newEnv(t)
	e.provider(t, models.ProviderInput{Name: "main", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-main-0001"), Models: []string{"fake-large"}})
	_, tok := e.run(t, 0, runner.Model{Provider: "main", Model: "fake-large"})
	h := http.Header{"Authorization": {"Bearer " + tok}}
	body := apiCalls[models.OpenAICompletions].body

	e.model.Fail(fakemodel.Fail429, 1)
	if resp, b := e.call(t, "main", "chat/completions", h, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("429 then 200: %d %s", resp.StatusCode, b)
	}
	if n := len(e.model.Requests()); n != 2 {
		t.Fatalf("429 then 200: %d upstream requests", n)
	}
	e.model.Reset()
	e.model.Fail(fakemodel.Fail500, 3)
	if resp, _ := e.call(t, "main", "chat/completions", h, body); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("three 500s: %d", resp.StatusCode)
	}
	if n := len(e.model.Requests()); n != 3 {
		t.Fatalf("three 500s: %d upstream requests, want 1 and 2 retries", n)
	}

	// A stream that fails after the first byte is not retried, and the agent sees the error.
	e.model.Reset()
	e.model.Fail(fakemodel.FailCut, 1)
	req, _ := http.NewRequest(http.MethodPost, e.Base+"/llm/main/chat/completions", strings.NewReader(body))
	req.Header = h.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := readErrorful(resp)
	if readErr == nil {
		t.Fatal("the cut stream ended cleanly at the agent")
	}
	if n := len(e.model.Requests()); n != 1 {
		t.Fatalf("cut stream: %d upstream requests", n)
	}

	// Proxy overhead p95 over 200 calls against the fake.
	e.model.Reset()
	plain := `{"model":"fake-large","messages":[{"role":"user","content":"hi"}]}`
	var direct, proxied []time.Duration
	for i := 0; i < 200; i++ {
		start := time.Now()
		r, err := http.Post(e.url+"/v1/chat/completions", "application/json", strings.NewReader(plain))
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, r)
		direct = append(direct, time.Since(start))
		start = time.Now()
		if resp, _ := e.call(t, "main", "chat/completions", h, plain); resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d: %d", i, resp.StatusCode)
		}
		proxied = append(proxied, time.Since(start))
	}
	p95 := func(d []time.Duration) time.Duration {
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d[len(d)*95/100-1]
	}
	overhead := p95(proxied) - p95(direct)
	t.Logf("proxy p95 %s, direct p95 %s, overhead %s", p95(proxied), p95(direct), overhead)
	if overhead > 50*time.Millisecond {
		t.Fatalf("proxy overhead p95 %s is more than 50 ms", overhead)
	}
}

func readErrorful(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}

// manualClock is a test clock for the provider limits.
type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []struct {
		at time.Time
		ch chan time.Time
	}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, struct {
		at time.Time
		ch chan time.Time
	}{c.now.Add(d), ch})
	return ch
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func (c *manualClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func TestSCN_MOD_009_ProviderConcurrencyAndTokens(t *testing.T) {
	e := newEnv(t)
	e.provider(t, models.ProviderInput{Name: "one-at-a-time", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-one-0001"),
		Models: []string{"fake-large"}, MaxConcurrency: 1})
	_, tok := e.run(t, 0, runner.Model{Provider: "one-at-a-time", Model: "fake-large"}, runner.Model{Provider: "small-tpm", Model: "fake-large"})
	h := http.Header{"Authorization": {"Bearer " + tok}}
	plain := `{"model":"fake-large","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`

	e.model.Delay = 300 * time.Millisecond
	type span struct{ start, end time.Time }
	spans := make([]span, 2)
	var wg sync.WaitGroup
	for i := range spans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spans[i].start = time.Now()
			if resp, _ := e.call(t, "one-at-a-time", "chat/completions", h, plain); resp.StatusCode != http.StatusOK {
				t.Errorf("call %d: %d", i, resp.StatusCode)
			}
			spans[i].end = time.Now()
		}()
	}
	wg.Wait()
	reqs := e.model.Requests()
	if len(reqs) != 2 || reqs[1].At.Sub(reqs[0].At) < 280*time.Millisecond {
		t.Fatalf("the two requests overlapped at the provider: %v", reqs[1].At.Sub(reqs[0].At))
	}
	e.model.Delay = 0

	// tokens_per_minute: the second request waits, and after 60 s the proxy answers 429.
	clock := &manualClock{now: time.Now()}
	e.F.Models.SetClock(clock)
	e.provider(t, models.ProviderInput{Name: "small-tpm", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key("sk-tpm-0001"),
		Models: []string{"fake-large"}, TokensPerMinute: 150})
	small := `{"model":"fake-large","max_tokens":40,"messages":[{"role":"user","content":"hi"}]}`
	if resp, _ := e.call(t, "small-tpm", "chat/completions", h, small); resp.StatusCode != http.StatusOK {
		t.Fatalf("first call: %d", resp.StatusCode)
	}
	// The next request needs more tokens than the minute has left.
	large := `{"model":"fake-large","max_tokens":400,"messages":[{"role":"user","content":"hi"}]}`
	done := make(chan int, 1)
	go func() {
		resp, _ := e.call(t, "small-tpm", "chat/completions", h, large)
		done <- resp.StatusCode
	}()
	deadline := time.Now().Add(5 * time.Second)
	for clock.pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second call did not wait for capacity")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case code := <-done:
		t.Fatalf("the second call did not wait: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	// The clock passes 60 s without capacity for the request, and the proxy gives up.
	for i := 0; i < 59; i++ {
		clock.Advance(time.Second)
	}
	select {
	case code := <-done:
		t.Fatalf("the call ended before 60 s: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	clock.Advance(time.Second)
	select {
	case code := <-done:
		if code != http.StatusTooManyRequests {
			t.Fatalf("after 60 s: %d, want 429", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy did not answer after 60 s")
	}
}
