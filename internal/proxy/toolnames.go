package proxy

import "strings"

// Upstream function-name resolution.
//
// Zen presents tools to the model namespaced the OpenCode way ("default.Bash",
// "default.shell"), and the proxy injects lowercase free-tier marker tools
// ("shell", "read" — see free_tier.go) that muse-spark, being OpenCode-trained,
// sometimes calls spontaneously instead of the client's declared equivalents.
// Both shapes arrive as function calls whose name is NOT declared by the
// downstream client. Dropping such calls silently turns them into text-only
// end_turn responses, which stalls agent loops: the model announces what it
// is about to do, the turn ends, and the user has to type "continue". This
// file resolves those names to the client's declared tool instead.

// markerToolAliases maps the injected free-tier marker tool names to client
// tool names that can serve the same purpose, most preferred first.
var markerToolAliases = map[string][]string{
	"shell": {"Bash", "bash", "Shell", "shell", "run_command"},
	"read":  {"Read", "read", "read_file", "ReadFile", "view_file"},
}

// ResolveDeclaredToolName maps an upstream function-call name to a name
// declared by the downstream client. It resolves, in order:
//
//  1. exact match against declared,
//  2. case-insensitive match (returns the declared canonical name),
//  3. OpenCode namespace stripping ("default.Bash" -> "Bash", last dot
//     segment, then 1-2 again),
//  4. free-tier marker aliases (shell->Bash, read->Read) on both the raw
//     and the namespace-stripped name.
//
// ok is false when nothing matches; callers should drop the call but log it
// (never silently — invisible drops are how agent-loop stalls hide).
// A nil/empty declared list resolves nothing.
func ResolveDeclaredToolName(name string, declared []string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(declared) == 0 {
		return "", false
	}
	if d, ok := exactOrFold(name, declared); ok {
		return d, true
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i+1 < len(name) {
		short := name[i+1:]
		if d, ok := exactOrFold(short, declared); ok {
			return d, true
		}
		if d, ok := markerTarget(short, declared); ok {
			return d, true
		}
	}
	return markerTarget(name, declared)
}

func exactOrFold(name string, declared []string) (string, bool) {
	for _, d := range declared {
		if d == name {
			return d, true
		}
	}
	for _, d := range declared {
		if strings.EqualFold(d, name) {
			return d, true
		}
	}
	return "", false
}

// markerTarget resolves one of the injected free-tier marker names to the
// best-matching declared tool, exact before case-insensitive.
func markerTarget(name string, declared []string) (string, bool) {
	candidates, isMarker := markerToolAliases[strings.ToLower(name)]
	if !isMarker {
		return "", false
	}
	for _, want := range candidates {
		for _, d := range declared {
			if d == want {
				return d, true
			}
		}
	}
	for _, want := range candidates {
		for _, d := range declared {
			if strings.EqualFold(d, want) {
				return d, true
			}
		}
	}
	return "", false
}
