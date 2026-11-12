package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Smoke check names (§12.1).
const (
	CheckPlain = "plain"
	CheckTools = "tool_call"
	CheckJSON  = "json"
)

// CheckResult is one smoke check.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	MS     int64  `json:"ms"`
}

// Smoke is the stored result of a provider test.
type Smoke struct {
	Model    string        `json:"model"`
	Response string        `json:"response_model"`
	Checks   []CheckResult `json:"checks"`
	At       time.Time     `json:"at"`
}

// Passed reports whether a check passed.
func (s *Smoke) Passed(name string) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Checks {
		if c.Name == name {
			return c.OK
		}
	}
	return false
}

// OK reports whether every check passed.
func (s *Smoke) OK() bool {
	if s == nil || len(s.Checks) == 0 {
		return false
	}
	for _, c := range s.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

const weatherTool = "get_weather"

var weatherSchema = map[string]any{
	"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"},
}

// smoker runs the checks against one provider.
type smoker struct {
	c        Config
	key      string
	model    string
	http     *http.Client
	response string
}

// RunSmoke runs the three checks. A failed check does not stop the others.
// redact removes secret values from error details.
func RunSmoke(ctx context.Context, hc *http.Client, c Config, key, model string, redact func(string) string) Smoke {
	s := &smoker{c: c, key: key, model: model, http: hc}
	out := Smoke{Model: model, At: time.Now().UTC()}
	for _, ch := range []struct {
		name string
		run  func(context.Context) (string, error)
	}{{CheckPlain, s.plain}, {CheckTools, s.tools}, {CheckJSON, s.json}} {
		start := time.Now()
		detail, err := ch.run(ctx)
		r := CheckResult{Name: ch.name, OK: err == nil, Detail: detail, MS: time.Since(start).Milliseconds()}
		if err != nil {
			r.Detail = redact(err.Error())
		}
		out.Checks = append(out.Checks, r)
	}
	out.Response = s.response
	return out
}

func (s *smoker) post(ctx context.Context, path string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u, err := s.c.Endpoint(path, "")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.c.SetCredential(req.Header, s.key)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, clip(string(raw), 300))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("the answer is not JSON: %s", clip(string(raw), 200))
	}
	if m, ok := out["model"].(string); ok && m != "" {
		s.response = m
	} else if m, ok := out["modelVersion"].(string); ok {
		s.response = m
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// dig walks maps and slices: dig(v, "choices", 0, "message").
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

const (
	plainPrompt = "Reply with the word ok."
	toolPrompt  = "What is the weather in Oslo? Use the get_weather tool."
	jsonPrompt  = `Answer with a JSON object that has one key, "answer", with the value "ok". Answer with JSON only.`
	toolResult  = `{"temp_c": 21, "sky": "clear"}`
)

func (s *smoker) plain(ctx context.Context) (string, error) {
	txt, err := s.text(ctx, plainPrompt, false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(txt) == "" {
		return "", fmt.Errorf("the answer has no text")
	}
	return "answered: " + clip(txt, 60), nil
}

func (s *smoker) json(ctx context.Context) (string, error) {
	txt, err := s.text(ctx, jsonPrompt, true)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(txt)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "```json"), "```")
	t = strings.TrimSpace(strings.TrimSuffix(t, "```"))
	var v map[string]any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return "", fmt.Errorf("the answer is not a JSON object: %s", clip(txt, 80))
	}
	if _, ok := v["answer"]; !ok {
		return "", fmt.Errorf("the JSON answer has no key answer")
	}
	return "answered with a JSON object", nil
}

// text sends one user message and returns the text of the answer.
func (s *smoker) text(ctx context.Context, prompt string, wantJSON bool) (string, error) {
	switch s.c.APIType {
	case OpenAICompletions:
		body := map[string]any{"model": s.model, "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": prompt}}}
		if wantJSON {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
		out, err := s.post(ctx, "chat/completions", body)
		if err != nil {
			return "", err
		}
		return str(dig(out, "choices", 0, "message", "content")), nil
	case OpenAIResponses:
		body := map[string]any{"model": s.model, "max_output_tokens": 256, "input": prompt}
		if wantJSON {
			body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer", "strict": true,
				"schema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}},
					"required": []string{"answer"}, "additionalProperties": false}}}
		}
		out, err := s.post(ctx, "responses", body)
		if err != nil {
			return "", err
		}
		return responsesText(out), nil
	case AnthropicMessages:
		out, err := s.post(ctx, "messages", map[string]any{"model": s.model, "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": prompt}}})
		if err != nil {
			return "", err
		}
		return anthropicText(out), nil
	case GoogleGenerative:
		body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}}}
		if wantJSON {
			body["generationConfig"] = map[string]any{"responseMimeType": "application/json"}
		}
		out, err := s.post(ctx, "models/"+s.model+":generateContent", body)
		if err != nil {
			return "", err
		}
		return googleText(out), nil
	}
	return "", fmt.Errorf("api type %s is not supported", s.c.APIType)
}

func responsesText(out map[string]any) string {
	var b strings.Builder
	items, _ := out["output"].([]any)
	for _, it := range items {
		if str(dig(it, "type")) != "message" {
			continue
		}
		parts, _ := dig(it, "content").([]any)
		for _, p := range parts {
			b.WriteString(str(dig(p, "text")))
		}
	}
	return b.String()
}

func anthropicText(out map[string]any) string {
	var b strings.Builder
	parts, _ := out["content"].([]any)
	for _, p := range parts {
		if str(dig(p, "type")) == "text" {
			b.WriteString(str(dig(p, "text")))
		}
	}
	return b.String()
}

func googleText(out map[string]any) string {
	var b strings.Builder
	parts, _ := dig(out, "candidates", 0, "content", "parts").([]any)
	for _, p := range parts {
		b.WriteString(str(dig(p, "text")))
	}
	return b.String()
}

// tools runs a tool call round trip: the model calls get_weather, gets the
// result and answers with text.
func (s *smoker) tools(ctx context.Context) (string, error) {
	noCall := fmt.Errorf("the model did not call the tool, so it cannot run the agent")
	switch s.c.APIType {
	case OpenAICompletions:
		tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": weatherTool, "description": "Weather of a city", "parameters": weatherSchema}}}
		msgs := []any{map[string]any{"role": "user", "content": toolPrompt}}
		out, err := s.post(ctx, "chat/completions", map[string]any{"model": s.model, "max_tokens": 256, "messages": msgs, "tools": tools})
		if err != nil {
			return "", err
		}
		call := dig(out, "choices", 0, "message", "tool_calls", 0)
		if str(dig(call, "function", "name")) != weatherTool {
			return "", noCall
		}
		msgs = append(msgs, dig(out, "choices", 0, "message"), map[string]any{"role": "tool", "tool_call_id": str(dig(call, "id")), "content": toolResult})
		out, err = s.post(ctx, "chat/completions", map[string]any{"model": s.model, "max_tokens": 256, "messages": msgs, "tools": tools})
		if err != nil {
			return "", err
		}
		return roundTrip(str(dig(out, "choices", 0, "message", "content")))
	case OpenAIResponses:
		tools := []any{map[string]any{"type": "function", "name": weatherTool, "description": "Weather of a city", "parameters": weatherSchema}}
		input := []any{map[string]any{"role": "user", "content": toolPrompt}}
		out, err := s.post(ctx, "responses", map[string]any{"model": s.model, "max_output_tokens": 256, "input": input, "tools": tools})
		if err != nil {
			return "", err
		}
		var call any
		items, _ := out["output"].([]any)
		for _, it := range items {
			if str(dig(it, "type")) == "function_call" && str(dig(it, "name")) == weatherTool {
				call = it
			}
		}
		if call == nil {
			return "", noCall
		}
		input = append(input, call, map[string]any{"type": "function_call_output", "call_id": str(dig(call, "call_id")), "output": toolResult})
		out, err = s.post(ctx, "responses", map[string]any{"model": s.model, "max_output_tokens": 256, "input": input, "tools": tools})
		if err != nil {
			return "", err
		}
		return roundTrip(responsesText(out))
	case AnthropicMessages:
		tools := []any{map[string]any{"name": weatherTool, "description": "Weather of a city", "input_schema": weatherSchema}}
		msgs := []any{map[string]any{"role": "user", "content": toolPrompt}}
		out, err := s.post(ctx, "messages", map[string]any{"model": s.model, "max_tokens": 256, "messages": msgs, "tools": tools})
		if err != nil {
			return "", err
		}
		var use any
		parts, _ := out["content"].([]any)
		for _, p := range parts {
			if str(dig(p, "type")) == "tool_use" && str(dig(p, "name")) == weatherTool {
				use = p
			}
		}
		if use == nil {
			return "", noCall
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": parts},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": str(dig(use, "id")), "content": toolResult}}})
		out, err = s.post(ctx, "messages", map[string]any{"model": s.model, "max_tokens": 256, "messages": msgs, "tools": tools})
		if err != nil {
			return "", err
		}
		return roundTrip(anthropicText(out))
	case GoogleGenerative:
		tools := []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": weatherTool, "description": "Weather of a city", "parameters": weatherSchema}}}}
		contents := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": toolPrompt}}}}
		out, err := s.post(ctx, "models/"+s.model+":generateContent", map[string]any{"contents": contents, "tools": tools})
		if err != nil {
			return "", err
		}
		var call any
		parts, _ := dig(out, "candidates", 0, "content", "parts").([]any)
		for _, p := range parts {
			if fc := dig(p, "functionCall"); str(dig(fc, "name")) == weatherTool {
				call = fc
			}
		}
		if call == nil {
			return "", noCall
		}
		contents = append(contents, map[string]any{"role": "model", "parts": parts},
			map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": weatherTool, "response": map[string]any{"temp_c": 21}}}}})
		out, err = s.post(ctx, "models/"+s.model+":generateContent", map[string]any{"contents": contents, "tools": tools})
		if err != nil {
			return "", err
		}
		return roundTrip(googleText(out))
	}
	return "", fmt.Errorf("api type %s is not supported", s.c.APIType)
}

func roundTrip(answer string) (string, error) {
	if strings.TrimSpace(answer) == "" {
		return "", fmt.Errorf("the model called the tool but gave no answer after the tool result")
	}
	return "called " + weatherTool + " and answered after the result", nil
}
