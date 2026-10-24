package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DeepSeek-style reasoning backends require every assistant turn of a
// thinking-mode conversation to carry reasoning_content once any turn does.
// Omitting the field on a turn whose thinking was never produced (or was
// lost, e.g. across a proxy restart) makes the upstream reject the whole
// request with:
//
//	400 invalid_request_error: The `reasoning_content` in the thinking mode
//	must be passed back to the API
//
// The upstream client does not have to reproduce the original reasoning text:
// an explicit empty string satisfies the presence check. This is what the
// OpenCode client itself injects for DeepSeek (anomalyco/opencode#25000, fix
// anomalyco/opencode#24250), and Zen's own engineers describe an empty
// reasoning_content injection as the intended consumer-safe behavior.
//
// RequiresReasoningReplay reports whether the upstream model id belongs to
// such a family. Provider prefixes ("oc/deepseek-…") are ignored.
func RequiresReasoningReplay(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return strings.Contains(m, "deepseek")
}

// ensureReasoningReplayForRequest marks every assistant message of a request
// that targets a reasoning-replay backend, so OpenAIMessage.MarshalJSON emits
// reasoning_content even when the stored value is empty. Turns that do carry
// real reasoning keep it; only the field presence changes.
func ensureReasoningReplayForRequest(req *OpenAIRequest) {
	if req == nil || !RequiresReasoningReplay(req.Model) {
		return
	}
	for i := range req.Messages {
		if req.Messages[i].Role == "assistant" {
			req.Messages[i].ReasoningContentSet = true
		}
	}
}

// EnsureRawReasoningReplay adds an explicit empty reasoning_content to every
// assistant message of a raw OpenAI chat payload that does not carry the field
// yet (or carries an explicit null). It is the /v1/chat/completions counterpart
// of ensureReasoningReplayForRequest, which covers the translated routes.
//
// A payload whose messages array cannot be parsed is left untouched: the
// upstream will report the real problem, and this best-effort repair must
// never reject a request that used to pass.
func EnsureRawReasoningReplay(payload map[string]json.RawMessage) error {
	raw, ok := payload["messages"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil
	}
	changed := false
	for _, msg := range messages {
		var role string
		if r, ok := msg["role"]; ok {
			_ = json.Unmarshal(r, &role)
		}
		if role != "assistant" {
			continue
		}
		if current, ok := msg["reasoning_content"]; ok && !isJSONNull(current) {
			continue
		}
		msg["reasoning_content"] = json.RawMessage(`""`)
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	payload["messages"] = encoded
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
