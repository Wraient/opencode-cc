package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsFreeModel(t *testing.T) {
	for _, model := range []string{"big-pickle", "muse-spark-1.3-contributor-free", "nemotron-3.5-lightning-free", "x-preview-f-free"} {
		if !IsFreeModel(model) {
			t.Errorf("IsFreeModel(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-5.5", "claude-sonnet", "muse-spark-1.3"} {
		if IsFreeModel(model) {
			t.Errorf("IsFreeModel(%q) = true, want false", model)
		}
	}
}

func TestPrepareFreeChatBodyAddsCanonicalGateShape(t *testing.T) {
	body := []byte(`{"model":"big-pickle","stream":false,"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`)
	out, err := PrepareFreeChatBody(body)
	if err != nil {
		t.Fatalf("PrepareFreeChatBody: %v", err)
	}
	var payload struct {
		Stream        bool            `json:"stream"`
		ToolChoice    any             `json:"tool_choice"`
		StreamOptions map[string]bool `json:"stream_options"`
		Tools         []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !payload.Stream || payload.ToolChoice != "auto" || !payload.StreamOptions["include_usage"] {
		t.Fatalf("gate fields = stream:%v choice:%v options:%v", payload.Stream, payload.ToolChoice, payload.StreamOptions)
	}
	seen := map[string]bool{}
	for _, tool := range payload.Tools {
		seen[tool.Function.Name] = true
	}
	for _, name := range []string{"Read", "shell", "read"} {
		if !seen[name] {
			t.Errorf("missing tool %q; tools=%v", name, seen)
		}
	}
}

func TestPrepareFreeResponsesBodyAddsGateTools(t *testing.T) {
	out, err := PrepareFreeResponsesBody([]byte(`{"model":"muse-spark-1.3-contributor-free","stream":false}`))
	if err != nil {
		t.Fatalf("PrepareFreeResponsesBody: %v", err)
	}
	if !strings.Contains(string(out), `"name":"shell"`) || !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("response gate tools missing: %s", out)
	}
	if !strings.Contains(string(out), `"stream":true`) {
		t.Fatalf("response stream was not forced: %s", out)
	}
}
