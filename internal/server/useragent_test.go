package server

import (
	"strings"
	"testing"
)

func TestParseOCVersion(t *testing.T) {
	cases := map[string]string{
		"opencode v2.0.5":               "2.0.5",
		"opencode2 v0.0.0-next-17444":   "0.0.0-next-17444",
		"v1.18.15":                      "1.18.15",
		"1.18.15":                       "1.18.15",
		"opencode/2.0.5 extra":          "2.0.5",
		"":                              "",
		"no version here":               "",
		"opencode2 v0.0.0-next-17444\n": "0.0.0-next-17444",
	}
	for in, want := range cases {
		if got := parseOCVersion(strings.TrimSpace(in)); got != want {
			t.Errorf("parseOCVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsStableOCVersion(t *testing.T) {
	stable := []string{"2.0.5", "1.18.15", "1.18.16"}
	for _, v := range stable {
		if !isStableOCVersion(v) {
			t.Errorf("isStableOCVersion(%q) = false, want true", v)
		}
	}
	unstable := []string{"", "0.0.0-next-17444", "0.0.0", "2.0.5-next.1", "2.0.5-beta"}
	for _, v := range unstable {
		if isStableOCVersion(v) {
			t.Errorf("isStableOCVersion(%q) = true, want false", v)
		}
	}
}

func TestOcUAFormat(t *testing.T) {
	ua := ocUA()
	if !strings.HasPrefix(ua, "opencode/latest/") {
		t.Fatalf("ocUA() = %q, must use the current installation format", ua)
	}
	parts := strings.Split(ua, "/")
	if len(parts) != 4 || parts[0] != "opencode" || parts[1] != "latest" || parts[3] != "cli" {
		t.Fatalf("ocUA() = %q, want opencode/latest/<version>/cli", ua)
	}
	if !isStableOCVersion(parts[2]) {
		t.Fatalf("ocUA() = %q, contains a preview version", ua)
	}
	if strings.Contains(ua, "opencode2") || strings.Contains(ua, "next") {
		t.Fatalf("ocUA() = %q, contains preview output", ua)
	}
}
