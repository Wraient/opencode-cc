package proxy

import "testing"

func TestRecoverNamelessTool(t *testing.T) {
	bashSchema := jsonRawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
	readSchema := jsonRawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`)
	declared := []string{"Bash", "Read"}
	schemas := map[string]jsonRawMessage{"Bash": bashSchema, "Read": readSchema}

	if got, ok := RecoverNamelessTool(`{"command":"echo hi"}`, declared, schemas); !ok || got != "Bash" {
		t.Errorf(`{"command"} -> (%q, %v), want (Bash, true)`, got, ok)
	}
	if got, ok := RecoverNamelessTool(`{"file_path":"/x"}`, declared, schemas); !ok || got != "Read" {
		t.Errorf(`{"file_path"} -> (%q, %v), want (Read, true)`, got, ok)
	}
	// ambiguous: matches neither schema's required+properties
	if _, ok := RecoverNamelessTool(`{"pattern":"x"}`, declared, schemas); ok {
		t.Errorf(`{"pattern"} must not recover, got ok`)
	}
	// malformed / empty args never recover
	for _, bad := range []string{"", "{}", "{not json", `"str"`} {
		if _, ok := RecoverNamelessTool(bad, declared, schemas); ok {
			t.Errorf("%q must not recover", bad)
		}
	}
	// tie between two schemaless tools is ambiguous
	if _, ok := RecoverNamelessTool(`{"a":1}`, []string{"X", "Y"}, nil); ok {
		t.Errorf("tie must not recover")
	}
	// single schemaless tool with empty required matches non-empty args
	if got, ok := RecoverNamelessTool(`{"a":1}`, []string{"X"}, nil); !ok || got != "X" {
		t.Errorf("single schemaless tool -> (%q, %v), want (X, true)", got, ok)
	}
}
