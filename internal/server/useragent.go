package server

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Fallback matches the audited source release in proxy/opencode/manifest.json.
	// Never advertise an older client than the bundled request contract.
	fallbackOCVersion = "2.0.29"
	// The current OpenCode installation format is
	// opencode/<channel>/<version>/<client>. Zen's free-tier gate validates
	// this shape, so do not append the old AI SDK suffix here.
	zenUAChannel = "latest"
	zenUAClient  = "cli"
)

var (
	ocVersionMu      sync.Mutex
	ocVersionChecked time.Time
	ocVersion        string
	versionRe        = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)
)

// ocUA returns the current stock OpenCode installation User-Agent, for
// example "opencode/latest/2.0.12/cli". Zen's free-tier gate validates the
// channel/version/client shape as well as the semver, so this must not use
// the older "opencode/<version> ai-sdk/..." form.
func ocUA() string {
	ocVersionMu.Lock()
	defer ocVersionMu.Unlock()
	if ocVersion == "" || time.Since(ocVersionChecked) >= 5*time.Minute {
		ocVersion = detectOCVersion()
		ocVersionChecked = time.Now()
	}
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

// newerStableOCVersion compares numeric release components, not lexicographic
// strings (2.0.29 is newer than 2.0.9). Invalid/prerelease probes are ignored.
func newerStableOCVersion(current, candidate string) string {
	if !isStableOCVersion(candidate) || parseOCVersion(candidate) != candidate {
		return current
	}
	a, b := strings.Split(current, "."), strings.Split(candidate, ".")
	if len(a) != 3 || len(b) != 3 {
		return current
	}
	for i := range a {
		x, errA := strconv.Atoi(a[i])
		y, errB := strconv.Atoi(b[i])
		if errA != nil || errB != nil {
			return current
		}
		if y > x {
			return candidate
		}
		if y < x {
			return current
		}
	}
	return current
}

// Probe with a deadline and select the newest stable installed release or the
// audited compatibility version, whichever is newer. Re-probed every 5 minutes
// so upgrading the installed CLI does not require restarting this service.
func detectOCVersion() string {
	candidates := [][]string{
		{"/usr/bin/opencode", "--version"},
		{"opencode1", "--version"},
		{"opencode", "--version"},
	}
	// Explicit home installs: systemd services and other restricted-PATH
	// environments may not resolve bare "opencode" even though it exists.
	if home := os.Getenv("HOME"); home != "" {
		candidates = append(candidates,
			[]string{home + "/.opencode/bin/opencode", "--version"},
			[]string{home + "/.local/bin/opencode", "--version"},
		)
	}
	version := fallbackOCVersion
	for _, c := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, c[0], c[1:]...).Output()
		cancel()
		if err != nil {
			continue
		}
		version = newerStableOCVersion(version, parseOCVersion(strings.TrimSpace(string(out))))
	}
	return version
}
