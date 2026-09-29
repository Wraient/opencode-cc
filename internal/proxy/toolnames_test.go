package proxy

import "testing"

func TestResolveDeclaredToolName(t *testing.T) {
	declared := []string{"Bash", "Read", "Edit"}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		// exact and case-insensitive matches keep working
		{"Bash", "Bash", true},
		{"bash", "Bash", true},
		{"READ", "Read", true},
		// OpenCode namespacing applied by Zen: last dot segment resolves
		{"default.Bash", "Bash", true},
		{"default.bash", "Bash", true},
		{"provider.default.Read", "Read", true},
		// free-tier marker tools resolve to their declared equivalents
		{"shell", "Bash", true},
		{"SHELL", "Bash", true},
		{"read", "Read", true},
		{"default.shell", "Bash", true},
		{"default.read", "Read", true},
		// genuinely undeclared names still resolve to nothing
		{"evil_tool", "", false},
		{"default.evil_tool", "", false},
		{"", "", false},
		{"default.", "", false},
	}
	for _, c := range cases {
		got, ok := ResolveDeclaredToolName(c.in, declared)
		if ok != c.ok || got != c.want {
			t.Errorf("ResolveDeclaredToolName(%q) = (%q, %v), want (%q, %v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestResolveDeclaredToolNameEmptyDeclared(t *testing.T) {
	if _, ok := ResolveDeclaredToolName("Bash", nil); ok {
		t.Errorf("nil declared list must resolve nothing")
	}
	if _, ok := ResolveDeclaredToolName("Bash", []string{}); ok {
		t.Errorf("empty declared list must resolve nothing")
	}
}

func TestResolveDeclaredToolNameMarkerWithoutEquivalent(t *testing.T) {
	// A marker with no declared equivalent (e.g. no Read-like tool) still
	// resolves to nothing rather than inventing a tool.
	if _, ok := ResolveDeclaredToolName("read", []string{"Bash"}); ok {
		t.Errorf("read with no Read-like declared tool must resolve nothing")
	}
}
