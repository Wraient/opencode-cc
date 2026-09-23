package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// IsFreeModel reports whether a Zen model is currently served through the
// free-tier lane. Zen rotates this catalog frequently, so the intentionally
// broad suffix check covers the current free IDs (including the stealth
// big-pickle model) without making protocol routing depend on a stale list.
func IsFreeModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return strings.Contains(m, "-free") || m == "big-pickle"
}

// PrepareFreeChatBody adapts an OpenAI Chat Completions request to Zen's
// free-tier gate. The public API accepts ordinary non-streaming requests,
// while Zen's free lane currently requires the same streaming/tool shape as
// the official OpenCode client. The marker tools are deliberately described
// as internal compatibility tools; callers' real tools remain untouched.
func PrepareFreeChatBody(body []byte) ([]byte, error) {
	return prepareFreeBody(body, "chat")
}

// PrepareFreeResponsesBody is the Responses API equivalent of
// PrepareFreeChatBody.
func PrepareFreeResponsesBody(body []byte) ([]byte, error) {
	return prepareFreeBody(body, "responses")
}

func prepareFreeBody(body []byte, format string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s request: %w", format, err)
	}
	if payload == nil {
		return nil, fmt.Errorf("%s request must be a JSON object", format)
	}

	payload["stream"] = json.RawMessage(`true`)
	if format == "chat" {
		if _, ok := payload["stream_options"]; !ok || bytes.Equal(bytes.TrimSpace(payload["stream_options"]), []byte("null")) {
			payload["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		}
	}

	tools, err := freeGateTools(payload["tools"], format)
	if err != nil {
		return nil, err
	}
	payload["tools"] = tools

	if _, ok := payload["tool_choice"]; !ok || bytes.Equal(bytes.TrimSpace(payload["tool_choice"]), []byte("null")) {
		payload["tool_choice"] = json.RawMessage(`"auto"`)
	}

	return json.Marshal(payload)
}

func freeGateTools(raw json.RawMessage, format string) (json.RawMessage, error) {
	var tools []json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, fmt.Errorf("decode tools: %w", err)
		}
	}
	present := map[string]bool{}
	for _, tool := range tools {
		if name, ok := freeToolName(tool, format); ok {
			present[name] = true
		}
	}
	for _, name := range []string{"shell", "read"} {
		if present[name] {
			continue
		}
		tools = append(tools, freeMarkerTool(name, format))
	}
	return json.Marshal(tools)
}

func freeToolName(raw json.RawMessage, format string) (string, bool) {
	var envelope struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", false
	}
	if format == "responses" && envelope.Name != "" {
		return envelope.Name, true
	}
	if envelope.Function.Name != "" {
		return envelope.Function.Name, true
	}
	return "", false
}

func freeMarkerTool(name, format string) json.RawMessage {
	description := "Internal OpenCode free-tier compatibility marker; do not call."
	parameters := `{"type":"object","properties":{}}`
	var tool string
	if format == "responses" {
		tool = fmt.Sprintf(`{"type":"function","name":%q,"description":%q,"parameters":%s}`, name, description, parameters)
	} else {
		tool = fmt.Sprintf(`{"type":"function","function":{"name":%q,"description":%q,"parameters":%s}}`, name, description, parameters)
	}
	return json.RawMessage(tool)
}
