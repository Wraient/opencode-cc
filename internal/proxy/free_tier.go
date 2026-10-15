package proxy

import (
	"bytes"
	_ "embed"
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
// free-tier request contract. The public API accepts non-streaming requests;
// upstream uses streaming and the versioned OpenCode tool definitions.
// Existing client tools remain byte-for-byte intact. Added tools are never
// executed by this proxy and must be filtered against client declarations.
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

//go:embed opencode/tools.json
var openCodeToolsJSON []byte

// OpenCode tool definitions are captured from the released CLI and checked
// against the pinned source. See opencode/manifest.json and opencode/LICENSE.
// Empty schemas are not equivalent: they were rejected with FreeTierError
// while the same request with these definitions succeeded on 2026-10-15.
func freeMarkerTool(name, format string) json.RawMessage {
	var definitions []map[string]json.RawMessage
	if err := json.Unmarshal(openCodeToolsJSON, &definitions); err != nil {
		panic("invalid embedded OpenCode tool definitions: " + err.Error())
	}
	for _, definition := range definitions {
		var candidate string
		_ = json.Unmarshal(definition["name"], &candidate)
		if candidate != name {
			continue
		}
		var tool map[string]any
		if format == "responses" {
			tool = make(map[string]any, len(definition)+1)
			for key, value := range definition {
				tool[key] = value
			}
			tool["type"] = "function"
		} else {
			tool = map[string]any{"type": "function", "function": definition}
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			panic(err)
		}
		return encoded
	}
	panic("missing embedded OpenCode tool: " + name)
}
