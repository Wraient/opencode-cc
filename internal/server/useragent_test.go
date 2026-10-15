package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFallbackVersionMatchesAuditedSource(t *testing.T) {
	data, err := os.ReadFile("../proxy/opencode/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if fallbackOCVersion != manifest.Version {
		t.Fatalf("fallback version %s differs from audited source %s", fallbackOCVersion, manifest.Version)
	}
}

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

func TestVersionDoesNotRegressBelowAuditedContract(t *testing.T) {
	for _, tc := range []struct{ current, candidate, want string }{
		{"2.0.29", "2.0.24", "2.0.29"},
		{"2.0.9", "2.0.29", "2.0.29"},
		{"2.0.29", "1.19.0", "2.0.29"},
		{"2.0.29", "2.1.0", "2.1.0"},
		{"2.0.29", "2.1.0-beta", "2.0.29"},
		{"2.0.29", "bad", "2.0.29"},
	} {
		if got := newerStableOCVersion(tc.current, tc.candidate); got != tc.want {
			t.Errorf("newerStableOCVersion(%q, %q) = %q, want %q", tc.current, tc.candidate, got, tc.want)
		}
	}
}
