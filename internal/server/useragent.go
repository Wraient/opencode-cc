package server

import (
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

const (
	fallbackOCVersion = "2.0.12"
	// The current OpenCode installation format is
	// opencode/<channel>/<version>/<client>. Zen's free-tier gate validates
	// this shape, so do not append the old AI SDK suffix here.
	zenUAChannel = "latest"
	zenUAClient  = "cli"
)

var (
	ocVersionOnce sync.Once
	ocVersion     string
	versionRe     = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)
)

// ocUA returns the current stock OpenCode installation User-Agent, for
// example "opencode/latest/2.0.12/cli". Zen's free-tier gate validates the
// channel/version/client shape as well as the semver, so this must not use
// the older "opencode/<version> ai-sdk/..." form.
func ocUA() string {
	ocVersionOnce.Do(func() {
		ocVersion = detectOCVersion()
	})
	return "opencode/" + zenUAChannel + "/" + ocVersion + "/" + zenUAClient
}

// parseOCVersion extracts the first semver from raw "--version" output:
// "opencode v2.0.5" -> "2.0.5",
// "opencode2 v0.0.0-next-17444" -> "0.0.0-next-17444". Empty when none found.
func parseOCVersion(out string) string {
	return versionRe.FindString(out)
}

// isStableOCVersion reports whether v is a stable opencode release, usable
// in the upstream User-Agent. Preview/dev builds (0.0.0, -next, any
// prerelease tag) are rejected: Zen's free-tier gate does not accept them.
func isStableOCVersion(v string) bool {
	if v == "" || strings.HasPrefix(v, "0.0.0") || strings.Contains(v, "next") {
		return false
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return false
	}
	return true
}

// detectOCVersion probes the installed opencode binaries and uses the first
// stable release. Preview/dev builds are skipped; fallbackOCVersion is
// returned when no stable version is found.
func detectOCVersion() string {
	candidates := [][]string{
		{"/usr/bin/opencode", "--version"},
		{"opencode1", "--version"},
		{"opencode", "--version"},
	}
	for _, c := range candidates {
		out, err := exec.Command(c[0], c[1:]...).Output()
		if err != nil {
			continue
		}
		if v := parseOCVersion(strings.TrimSpace(string(out))); isStableOCVersion(v) {
			return v
		}
	}
	return fallbackOCVersion
}
