package models

import (
	"bytes"
	"encoding/json"
)

// Usage is the token count of one model call.
type Usage struct {
	In, Out, CacheRead int64
	found              bool
}

// Cost returns the cost in USD at prices per million tokens.
func (u Usage) Cost(priceIn, priceOut, priceCache float64) float64 {
	return (float64(u.In)*priceIn + float64(u.Out)*priceOut + float64(u.CacheRead)*priceCache) / 1e6
}

// usageReader reads usage from a response as it streams by. It handles
// server-sent events line by line and a plain JSON body at the end.
type usageReader struct {
	stream bool
	line   []byte
	body   bytes.Buffer
	u      Usage
}

const maxBody = 16 << 20

func (r *usageReader) Write(p []byte) (int, error) {
	if !r.stream {
		if r.body.Len() < maxBody {
			r.body.Write(p)
		}
		return len(p), nil
	}
	for _, b := range p {
		if b != '\n' {
			if len(r.line) < 1<<20 {
				r.line = append(r.line, b)
			}
			continue
		}
		r.event(r.line)
		r.line = r.line[:0]
	}
	return len(p), nil
}

func (r *usageReader) event(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(line[len("data:"):])
	if len(data) == 0 || data[0] != '{' {
		return
	}
	r.take(data)
}

// Result returns the usage after the response ended.
func (r *usageReader) Result() Usage {
	if r.stream {
		if len(r.line) > 0 {
			r.event(r.line)
		}
	} else {
		r.take(r.body.Bytes())
	}
	return r.u
}

type usageFields struct {
	// openai-completions
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// openai-responses and anthropic-messages
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	CacheReadInputTokens *int64 `json:"cache_read_input_tokens"`
}

type usageEnvelope struct {
	Type    string       `json:"type"`
	Usage   *usageFields `json:"usage"`
	Message *struct {
		Usage *usageFields `json:"usage"`
	} `json:"message"`
	Response *struct {
		Usage *usageFields `json:"usage"`
	} `json:"response"`
	// google-generative-ai
	UsageMetadata *struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func (r *usageReader) take(data []byte) {
	var e usageEnvelope
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch {
	case e.UsageMetadata != nil:
		m := e.UsageMetadata
		r.u = Usage{In: m.PromptTokenCount - m.CachedContentTokenCount, Out: m.CandidatesTokenCount, CacheRead: m.CachedContentTokenCount, found: true}
	case e.Response != nil && e.Response.Usage != nil:
		r.apply(e.Response.Usage, false)
	case e.Message != nil && e.Message.Usage != nil:
		// anthropic message_start: input and cache reads; output follows in message_delta.
		r.apply(e.Message.Usage, true)
	case e.Usage != nil:
		r.apply(e.Usage, e.Type == "message_delta")
	}
}

func (r *usageReader) apply(f *usageFields, merge bool) {
	u := r.u
	if !merge {
		u = Usage{}
	}
	switch {
	case f.PromptTokens != nil:
		var cached int64
		if f.PromptTokensDetails != nil {
			cached = f.PromptTokensDetails.CachedTokens
		}
		u.In, u.CacheRead = *f.PromptTokens-cached, cached
		if f.CompletionTokens != nil {
			u.Out = *f.CompletionTokens
		}
	case f.InputTokensDetails != nil:
		// openai-responses counts cached tokens inside input_tokens.
		cached := f.InputTokensDetails.CachedTokens
		if f.InputTokens != nil {
			u.In, u.CacheRead = *f.InputTokens-cached, cached
		}
		if f.OutputTokens != nil {
			u.Out = *f.OutputTokens
		}
	default:
		// anthropic counts cache reads apart from input_tokens.
		if f.InputTokens != nil {
			u.In = *f.InputTokens
		}
		if f.CacheReadInputTokens != nil {
			u.CacheRead = *f.CacheReadInputTokens
		}
		if f.OutputTokens != nil {
			u.Out = *f.OutputTokens
		}
	}
	u.found = true
	r.u = u
}
