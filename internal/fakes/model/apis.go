package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
			if len(p.Content) > 0 {
				b.WriteString(text(p.Content))
			}
		}
		return b.String()
	}
	return string(raw)
}

func mustRead(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}

// answer decides, waits for a scripted delay and reports a model that always fails.
func (f *Fake) answer(w http.ResponseWriter, r *http.Request, p parsed, mode string) (reply, bool) {
	rep, ok := f.decide(p, mode == FailNoTools)
	if !ok {
		failed(w, Fail429)
		return rep, false
	}
	if rep.Delay > 0 {
		select {
		case <-time.After(rep.Delay):
		case <-r.Context().Done():
			return rep, false
		}
	}
	return rep, true
}

// openai-completions

func (f *Fake) completions(w http.ResponseWriter, r *http.Request) {
	mode := f.nextFailure()
	if failed(w, mode) {
		return
	}
	body := mustRead(r)
	var in struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	p := parsed{Model: in.Model, BodyLen: len(body), WantJSON: in.ResponseFormat != nil && in.ResponseFormat.Type != "text"}
	for _, t := range in.Tools {
		p.Tools = append(p.Tools, t.Function.Name)
	}
	for _, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			p.System += text(m.Content) + "\n"
		case "tool":
			p.ToolResult = text(m.Content)
		case "assistant":
			p.Turns++
		default:
			p.Prompt += text(m.Content) + "\n"
		}
	}
	rep, ok := f.answer(w, r, p, mode)
	if !ok {
		return
	}
	usage := map[string]any{"prompt_tokens": rep.In, "completion_tokens": rep.Out, "total_tokens": rep.In + rep.Out,
		"prompt_tokens_details": map[string]any{"cached_tokens": 0}}
	msg := map[string]any{"role": "assistant", "content": rep.Text}
	finish := "stop"
	var calls []any
	for i, c := range rep.Calls {
		calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call_%d_%d", p.Turns, i), "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}})
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
		finish = "tool_calls"
		if rep.Text == "" {
			msg["content"] = nil
		}
	}
	if !in.Stream {
		writeJSON(w, http.StatusOK, map[string]any{"id": "chatcmpl-fake", "object": "chat.completion", "model": in.Model,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": usage})
		return
	}
	s := newSSE(w, mode == FailCut)
	chunk := func(delta map[string]any, fin any) map[string]any {
		return map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": in.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}}
	}
	s.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	for _, part := range split(rep.Text) {
		if part != "" {
			s.send("", chunk(map[string]any{"content": part}, nil))
		}
	}
	if len(calls) > 0 {
		s.send("", chunk(map[string]any{"tool_calls": calls}, nil))
	}
	s.send("", chunk(map[string]any{}, finish))
	s.send("", map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": in.Model, "choices": []any{}, "usage": usage})
	s.done()
}

// split cuts text into a few stream parts.
func split(s string) []string {
	if len(s) < 8 {
		return []string{s}
	}
	third := len(s) / 3
	return []string{s[:third], s[third : 2*third], s[2*third:]}
}

// openai-responses

func (f *Fake) responses(w http.ResponseWriter, r *http.Request) {
	mode := f.nextFailure()
	if failed(w, mode) {
		return
	}
	body := mustRead(r)
	var in struct {
		Model        string          `json:"model"`
		Stream       bool            `json:"stream"`
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		Tools        []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Text *struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	p := parsed{Model: in.Model, BodyLen: len(body), System: in.Instructions, WantJSON: in.Text != nil && in.Text.Format.Type == "json_schema"}
	for _, t := range in.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	var items []struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Output  string          `json:"output"`
	}
	if json.Unmarshal(in.Input, &items) == nil {
		inCalls := false
		for _, it := range items {
			switch {
			case it.Type == "function_call":
				if !inCalls {
					p.Turns++
				}
				inCalls = true
				continue
			case it.Type == "function_call_output":
				p.ToolResult = it.Output
			case it.Role == "assistant":
				p.Turns++
			case it.Role == "system" || it.Role == "developer":
				p.System += text(it.Content) + "\n"
			default:
				p.Prompt += text(it.Content) + "\n"
			}
			inCalls = false
		}
	} else {
		p.Prompt = text(in.Input)
	}
	rep, ok := f.answer(w, r, p, mode)
	if !ok {
		return
	}
	var output []any
	if rep.Text != "" {
		output = append(output, map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": rep.Text, "annotations": []any{}}}})
	}
	for i, c := range rep.Calls {
		output = append(output, map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d_%d", p.Turns, i), "call_id": fmt.Sprintf("call_%d_%d", p.Turns, i),
			"name": c.Name, "arguments": c.Args, "status": "completed"})
	}
	usage := map[string]any{"input_tokens": rep.In, "output_tokens": rep.Out, "total_tokens": rep.In + rep.Out,
		"input_tokens_details": map[string]any{"cached_tokens": 0}}
	resp := map[string]any{"id": "resp_fake", "object": "response", "model": in.Model, "status": "completed", "output": output, "usage": usage}
	if !in.Stream {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	s := newSSE(w, mode == FailCut)
	s.send("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_fake", "model": in.Model, "status": "in_progress"}})
	for i, item := range output {
		s.send("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": i, "item": item})
		if m, _ := item.(map[string]any); m["type"] == "message" {
			for _, part := range split(rep.Text) {
				s.send("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": i, "content_index": 0, "delta": part})
			}
		}
		s.send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
	}
	s.send("response.completed", map[string]any{"type": "response.completed", "response": resp})
}

// anthropic-messages

func (f *Fake) messages(w http.ResponseWriter, r *http.Request) {
	mode := f.nextFailure()
	if failed(w, mode) {
		return
	}
	body := mustRead(r)
	var in struct {
		Model    string          `json:"model"`
		Stream   bool            `json:"stream"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": err.Error()}})
		return
	}
	p := parsed{Model: in.Model, BodyLen: len(body), System: text(in.System)}
	for _, t := range in.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	for _, m := range in.Messages {
		if m.Role == "assistant" {
			p.Turns++
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_result" {
					p.ToolResult = text(b.Content)
				}
			}
		}
		p.Prompt += text(m.Content) + "\n"
	}
	rep, ok := f.answer(w, r, p, mode)
	if !ok {
		return
	}
	var content []any
	stop := "end_turn"
	if rep.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": rep.Text})
	}
	for i, c := range rep.Calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(c.Args), &args)
		content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_%d", p.Turns, i), "name": c.Name, "input": args})
		stop = "tool_use"
	}
	if !in.Stream {
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg_fake", "type": "message", "role": "assistant", "model": in.Model,
			"content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": rep.In, "output_tokens": rep.Out, "cache_read_input_tokens": 0}})
		return
	}
	s := newSSE(w, mode == FailCut)
	s.send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fake", "type": "message", "role": "assistant",
		"model": in.Model, "content": []any{}, "usage": map[string]any{"input_tokens": rep.In, "output_tokens": 1, "cache_read_input_tokens": 0}}})
	for i, block := range content {
		b := block.(map[string]any)
		if b["type"] == "text" {
			s.send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			for _, part := range split(rep.Text) {
				s.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": part}})
			}
		} else {
			args, _ := json.Marshal(b["input"])
			s.send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]any{}}})
			s.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		}
		s.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	s.send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": rep.Out}})
	s.send("message_stop", map[string]any{"type": "message_stop"})
}
