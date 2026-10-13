package server

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Kiowx/opencode-cc/internal/proxy"
)

// Free-tier marker-tool filtering for the OpenAI Chat Completions lane.
//
// The proxy injects two marker tools ("shell", "read") into every Zen free-lane
// request (see proxy.PrepareFreeChatBody) because that lane gates on the exact
// tool shape the official OpenCode client sends. Those tools are NOT declared by
// the downstream client, and the model occasionally calls one spontaneously.
// The /v1/messages lane already resolves such names to the client's declared
// equivalent (proxy.ResolveDeclaredToolName, wired through
// StreamConverter.RestrictTools) so the call survives as a real tool call
// instead of stalling the agent loop.
//
// This lane used to be a raw byte relay, so a phantom "shell" call reached
// OpenCode verbatim: the client has no such tool and errors with "tool not
// found", ending the turn. filterChatToolCalls applies the same name
// resolution to the streaming wire format so both lanes behave identically.

// chatCallKey identifies one streamed tool call. OpenAI numbers tool calls by
// index within a choice, and a call's fragments (name first, then arguments)
// all carry the same pair, so this keys the per-call verdict across chunks.
type chatCallKey struct {
	choice int
	call   int
}

// chatToolCallFilter resolves upstream tool-call names against the client's
// declared tools and remembers the verdict per call for the life of the
// stream, so argument-only continuation fragments inherit the decision made
// when the name arrived.
type chatToolCallFilter struct {
	declared []string

	// verdicts records keep/drop per call key.
	verdicts map[chatCallKey]bool
	// keptAny tracks, per choice, whether at least one call survived, so a
	// trailing finish_reason:"tool_calls" can be corrected to "stop" when
	// every call was dropped.
	keptAny map[int]bool

	droppedNames []string
	renamedFrom  []string
}

// newChatToolCallFilter builds a filter for the client-declared tool names.
func newChatToolCallFilter(declared []string) *chatToolCallFilter {
	clean := make([]string, 0, len(declared))
	for _, d := range declared {
		if d = strings.TrimSpace(d); d != "" {
			clean = append(clean, d)
		}
	}
	return &chatToolCallFilter{
		declared: clean,
		verdicts: map[chatCallKey]bool{},
		keptAny:  map[int]bool{},
	}
}

// summary renders a stable, log-safe description of what the filter changed.
func (f *chatToolCallFilter) summary() string {
	if f == nil || (len(f.droppedNames) == 0 && len(f.renamedFrom) == 0) {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(f.renamedFrom) > 0 {
		parts = append(parts, "renamed="+strings.Join(dedupe(f.renamedFrom), ","))
	}
	if len(f.droppedNames) > 0 {
		parts = append(parts, "dropped="+strings.Join(dedupe(f.droppedNames), ","))
	}
	return strings.Join(parts, " ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// verdictFor decides a call's fate. An unlabelled fragment (no name yet) is
// passed through unchanged: it will be judged when its name arrives, and
// inventing a decision here would either leak or swallow a legitimate call.
func (f *chatToolCallFilter) verdictFor(key chatCallKey, name string) (keep bool, decided bool) {
	if v, ok := f.verdicts[key]; ok {
		return v, true
	}
	if strings.TrimSpace(name) == "" {
		return true, false
	}
	if len(f.declared) == 0 {
		// Client declared no tools: every call is phantom.
		f.verdicts[key] = false
		f.droppedNames = append(f.droppedNames, name)
		return false, true
	}
	resolved, ok := proxy.ResolveDeclaredToolName(name, f.declared)
	if !ok {
		f.verdicts[key] = false
		f.droppedNames = append(f.droppedNames, name)
		return false, true
	}
	f.verdicts[key] = true
	if resolved != name {
		f.renamedFrom = append(f.renamedFrom, name+"->"+resolved)
	}
	return true, true
}

// filterChatToolCalls rewrites one SSE "data:" line of an OpenAI Chat
// Completions stream. Lines that are not tool-bearing JSON are returned
// byte-identical, so the common path stays a pure relay and unknown upstream
// fields survive untouched.
func (f *chatToolCallFilter) filterChatToolCalls(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return line, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(trimmed, &root) != nil {
		return line, false
	}
	rawChoices, ok := root["choices"]
	if !ok {
		return line, false
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(rawChoices, &choices) != nil {
		return line, false
	}

	changed := false
	for ci := range choices {
		choice := choices[ci]
		idx := ci
		if raw, ok := choice["index"]; ok {
			var n int
			if json.Unmarshal(raw, &n) == nil {
				idx = n
			}
		}

		rawDelta, hasDelta := choice["delta"]
		if hasDelta {
			var delta map[string]json.RawMessage
			if json.Unmarshal(rawDelta, &delta) == nil {
				if rawCalls, hasCalls := delta["tool_calls"]; hasCalls {
					var calls []map[string]json.RawMessage
					if json.Unmarshal(rawCalls, &calls) == nil && len(calls) > 0 {
						kept := make([]map[string]json.RawMessage, 0, len(calls))
						callsChanged := false
						for ti, call := range calls {
							callIdx := ti
							if raw, ok := call["index"]; ok {
								var n int
								if json.Unmarshal(raw, &n) == nil {
									callIdx = n
								}
							}
							name := toolCallName(call)
							keep, decided := f.verdictFor(chatCallKey{choice: idx, call: callIdx}, name)
							if !decided {
								// Argument-only fragment of a call whose name
								// has not arrived yet: pass through untouched.
								kept = append(kept, call)
								f.keptAny[idx] = true
								continue
							}
							if !keep {
								callsChanged = true
								continue
							}
							f.keptAny[idx] = true
							if resolved, ok := proxy.ResolveDeclaredToolName(name, f.declared); ok && resolved != name {
								if setToolCallName(call, resolved) {
									callsChanged = true
								}
							}
							kept = append(kept, call)
						}
						if callsChanged {
							// Renames mutate the call maps in place, so the
							// array must be re-encoded even when no call was
							// dropped (len(kept) == len(calls)).
							if len(kept) == 0 {
								delete(delta, "tool_calls")
							} else if encoded, err := json.Marshal(kept); err == nil {
								delta["tool_calls"] = encoded
							}
							if len(delta) == 0 {
								delete(choice, "delta")
							} else if encoded, err := json.Marshal(delta); err == nil {
								choice["delta"] = encoded
							}
							changed = true
						}
					}
				}
			}
		}

		// A choice that lost every call must not advertise tool_calls as the
		// finish reason: OpenAI SDKs treat that as "there are calls to run" and
		// then wait forever for tool results that will never arrive.
		if rawFinish, ok := choice["finish_reason"]; ok && !f.keptAny[idx] {
			var finish string
			if json.Unmarshal(rawFinish, &finish) == nil && finish == "tool_calls" {
				choice["finish_reason"] = json.RawMessage(`"stop"`)
				changed = true
			}
		}
	}
	if !changed {
		return line, false
	}
	if encodedChoices, err := json.Marshal(choices); err == nil {
		root["choices"] = encodedChoices
	}
	out, err := json.Marshal(root)
	if err != nil {
		return line, false
	}
	return out, true
}

// toolCallName extracts function.name from a raw tool-call delta.
func toolCallName(call map[string]json.RawMessage) string {
	rawFn, ok := call["function"]
	if !ok {
		return ""
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(rawFn, &fn) != nil {
		return ""
	}
	rawName, ok := fn["name"]
	if !ok {
		return ""
	}
	var name string
	if json.Unmarshal(rawName, &name) != nil {
		return ""
	}
	return name
}

// setToolCallName rewrites function.name in a raw tool-call delta.
func setToolCallName(call map[string]json.RawMessage, name string) bool {
	rawFn, ok := call["function"]
	if !ok {
		return false
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(rawFn, &fn) != nil {
		return false
	}
	encoded, err := json.Marshal(name)
	if err != nil {
		return false
	}
	if fn["name"] == nil {
		// Never introduce a name where the upstream sent none: an argument-only
		// continuation must stay argument-only.
		return false
	}
	fn["name"] = encoded
	callFn, err := json.Marshal(fn)
	if err != nil {
		return false
	}
	call["function"] = callFn
	return true
}

// declaredOpenAIToolNames extracts the function names a client declared on a
// Chat Completions request.
func declaredOpenAIToolNames(body []byte) []string {
	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
			Name string `json:"name"` // Responses-style shape
		} `json:"tools"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	out := make([]string, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		if t.Function.Name != "" {
			out = append(out, t.Function.Name)
			continue
		}
		if t.Name != "" {
			out = append(out, t.Name)
		}
	}
	return out
}
