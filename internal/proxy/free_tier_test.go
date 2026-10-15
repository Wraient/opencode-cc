package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The regression was an empty parameter schema, despite correct tool names.
// Pin the released CLI contract in both wire protocols, including required
// inputs, descriptions, strictness, and nested property constraints.
func TestFreeToolsMatchReleasedOpenCode(t *testing.T) {
	var definitions []map[string]json.RawMessage
	if err := json.Unmarshal(openCodeToolsJSON, &definitions); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"chat", "responses"} {
		for _, definition := range definitions {
			var name string
			_ = json.Unmarshal(definition["name"], &name)
			var got map[string]json.RawMessage
			if err := json.Unmarshal(freeMarkerTool(name, format), &got); err != nil {
				t.Fatal(err)
			}
			if format == "chat" {
				function := got["function"]
				got = nil
				if err := json.Unmarshal(function, &got); err != nil {
					t.Fatal(err)
				}
			} else {
				delete(got, "type")
			}
			wantJSON, _ := json.Marshal(definition)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("%s/%s does not match released CLI", format, name)
			}
			var schema struct {
				Required   []string       `json:"required"`
				Properties map[string]any `json:"properties"`
			}
			_ = json.Unmarshal(got["parameters"], &schema)
			if len(schema.Required) == 0 || len(schema.Properties) == 0 {
				t.Fatalf("%s/%s has placeholder schema", format, name)
			}
		}
	}
}

func TestFreeToolsPreserveCallerDefinitionsAndAreIdempotent(t *testing.T) {
	for _, format := range []string{"chat", "responses"} {
		caller := json.RawMessage(`{"type":"function","name":"read","description":"caller","parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}`)
		if format == "chat" {
			caller = json.RawMessage(`{"type":"function","function":{"name":"read","description":"caller","parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}}`)
		}
		input := append(append([]byte(`{"tool_choice":"none","tools":[`), caller...), []byte(`]}`)...)
		once, err := prepareFreeBody(input, format)
		if err != nil {
			t.Fatal(err)
		}
		twice, err := prepareFreeBody(once, format)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(once, twice) {
			t.Fatal("tool preparation is not idempotent")
		}
		var payload struct {
			Tools  []json.RawMessage `json:"tools"`
			Choice string            `json:"tool_choice"`
		}
		_ = json.Unmarshal(once, &payload)
		if len(payload.Tools) != 2 || !bytes.Equal(payload.Tools[0], caller) || payload.Choice != "none" {
			t.Fatalf("caller contract changed: %s", once)
		}
	}
}

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
