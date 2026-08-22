package server

import (
	"os/exec"
	"strings"
	"sync"
)

const (
	fallbackOCVersion = "1.18.15"
	uaSuffix          = "ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.13"
)

var (
	ocVersionOnce sync.Once
	ocVersion     string
)

// ocUA returns a User-Agent identical to the locally installed opencode CLI,
// e.g. "opencode/1.18.15 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.13".
func ocUA() string {
	ocVersionOnce.Do(func() {
		v := fallbackOCVersion
		if out, err := exec.Command("opencode", "--version").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				v = strings.TrimPrefix(s, "v")
			}
		}
		ocVersion = v
	})
	return "opencode/" + ocVersion + " " + uaSuffix
}
