package model

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// openai-completions

type ccRequest struct {
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

func (f *Fake) completions(w http.ResponseWriter, r *http.Request) {
	mode := f.nextFailure()
	if failed(w, mode) {
		return
	}
	var in ccRequest
	body := mustRead(r)
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	var tools []string
	for _, t := range in.Tools {
		tools = append(tools, t.Function.Name)
	}
	var prompt, toolResult string
	for _, m := range in.Messages {
		if m.Role == "tool" {
			toolResult = text(m.Content)
		} else {
			prompt += text(m.Content) + "\n"
		}
	}
	if in.ResponseFormat != nil && in.ResponseFormat.Type != "text" {
		prompt += " json"
	}
	rep := decide(in.Model, tools, toolResult, prompt, len(body), mode == FailNoTools)
	usage := map[string]any{"prompt_tokens": rep.In, "completion_tokens": rep.Out, "total_tokens": rep.In + rep.Out,
		"prompt_tokens_details": map[string]any{"cached_tokens": 0}}
	msg := map[string]any{"role": "assistant", "content": rep.Text}
	finish := "stop"
	if rep.ToolName != "" {
		msg["content"] = nil
		msg["tool_calls"] = []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": rep.ToolName, "arguments": rep.ToolArgs}}}
		finish = "tool_calls"
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
	if rep.ToolName != "" {
		s.send("", chunk(map[string]any{"tool_calls": msg["tool_calls"]}, nil))
	} else {
		for _, part := range split(rep.Text) {
			s.send("", chunk(map[string]any{"content": part}, nil))
		}
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
	var tools []string
	for _, t := range in.Tools {
		tools = append(tools, t.Name)
	}
	prompt := in.Instructions + "\n"
	var toolResult string
	var items []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Output  string          `json:"output"`
	}
	if json.Unmarshal(in.Input, &items) == nil {
		for _, it := range items {
			if it.Type == "function_call_output" {
				toolResult = it.Output
			} else {
				prompt += text(it.Content) + "\n"
			}
		}
	} else {
		prompt += text(in.Input)
	}
	if in.Text != nil && in.Text.Format.Type == "json_schema" {
		prompt += " json"
	}
	rep := decide(in.Model, tools, toolResult, prompt, len(body), mode == FailNoTools)
	var output []any
	if rep.ToolName != "" {
		output = append(output, map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": rep.ToolName, "arguments": rep.ToolArgs, "status": "completed"})
	} else {
		output = append(output, map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": rep.Text, "annotations": []any{}}}})
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
	if rep.ToolName == "" {
		for _, part := range split(rep.Text) {
			s.send("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 0, "delta": part})
		}
	}
	s.send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": output[0]})
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
	var tools []string
	for _, t := range in.Tools {
		tools = append(tools, t.Name)
	}
	prompt := text(in.System) + "\n"
	var toolResult string
	for _, m := range in.Messages {
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_result" {
					toolResult = text(b.Content)
				}
			}
		}
		prompt += text(m.Content) + "\n"
	}
	rep := decide(in.Model, tools, toolResult, prompt, len(body), mode == FailNoTools)
	var content []any
	stop := "end_turn"
	if rep.ToolName != "" {
		var args map[string]any
		_ = json.Unmarshal([]byte(rep.ToolArgs), &args)
		content = append(content, map[string]any{"type": "tool_use", "id": "toolu_1", "name": rep.ToolName, "input": args})
		stop = "tool_use"
	} else {
		content = append(content, map[string]any{"type": "text", "text": rep.Text})
	}
	if !in.Stream {
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg_fake", "type": "message", "role": "assistant", "model": in.Model,
			"content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": rep.In, "output_tokens": rep.Out, "cache_read_input_tokens": 0}})
		return
	}
	s := newSSE(w, mode == FailCut)
	s.send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fake", "type": "message", "role": "assistant",
		"model": in.Model, "content": []any{}, "usage": map[string]any{"input_tokens": rep.In, "output_tokens": 1, "cache_read_input_tokens": 0}}})
	if rep.ToolName != "" {
		s.send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": rep.ToolName, "input": map[string]any{}}})
		s.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": rep.ToolArgs}})
	} else {
		s.send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, part := range split(rep.Text) {
			s.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": part}})
		}
	}
	s.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	s.send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": rep.Out}})
	s.send("message_stop", map[string]any{"type": "message_stop"})
}

func mustRead(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}
