package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// applyFilter runs one SSE data line through the filter and returns the
// resulting line (or the original when unchanged).
func applyFilter(t *testing.T, f *chatToolCallFilter, line string) string {
	t.Helper()
	out, changed := f.filterChatToolCalls([]byte(line))
	if !changed {
		return line
	}
	return string(out)
}

func TestFilterRenamesMarkerToolToDeclaredTool(t *testing.T) {
	// The free lane injects a phantom "shell" marker; the client declared
	// "Bash". The call must be renamed, not dropped.
	f := newChatToolCallFilter([]string{"Bash"})
	line := `{"id":"1","object":"chat.completion.chunk","model":"space-bunny-free","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":null}]}`

	out := applyFilter(t, f, line)

	if strings.Contains(out, `"name":"shell"`) {
		t.Fatalf("phantom marker tool leaked to client: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("expected rename to declared Bash, got: %s", out)
	}
	if !strings.Contains(out, `"arguments":"{}"`) {
		t.Fatalf("arguments lost during rename: %s", out)
	}
	if got := f.summary(); !strings.Contains(got, "renamed=shell->Bash") {
		t.Fatalf("summary missing rename record: %q", got)
	}
}

func TestFilterRenamesNamespacedMarkerTool(t *testing.T) {
	// Zen presents OpenCode-namespaced names ("default.shell").
	f := newChatToolCallFilter([]string{"bash"})
	line := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"default.shell","arguments":"{}"}}]}}]}`

	out := applyFilter(t, f, line)
	if strings.Contains(out, "default.shell") {
		t.Fatalf("namespaced phantom leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"bash"`) {
		t.Fatalf("expected rename to declared bash, got: %s", out)
	}
}

func TestFilterDropsUnresolvableToolCall(t *testing.T) {
	// "frobnicate" is neither declared nor a marker alias: it must be dropped.
	f := newChatToolCallFilter([]string{"Bash"})
	line := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"frobnicate","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

	out := applyFilter(t, f, line)
	if strings.Contains(out, "frobnicate") {
		t.Fatalf("unknown tool call leaked: %s", out)
	}
	// A choice that lost every call must not claim tool_calls, or the client
	// waits forever for tool results that will never arrive.
	if strings.Contains(out, `"tool_calls"`) && strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("dropped call still advertised as tool_calls: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason should fall back to stop: %s", out)
	}
	if got := f.summary(); !strings.Contains(got, "dropped=frobnicate") {
		t.Fatalf("summary missing drop record: %q", got)
	}
}

func TestFilterDropsEverythingWhenClientDeclaredNoTools(t *testing.T) {
	f := newChatToolCallFilter(nil)
	line := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

	out := applyFilter(t, f, line)
	if strings.Contains(out, `"name"`) {
		t.Fatalf("tool call leaked to a client with no tools: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason should be stop: %s", out)
	}
}

func TestFilterKeepsDeclaredToolAcrossArgumentFragments(t *testing.T) {
	// Name arrives in the first fragment, arguments stream in later fragments
	// keyed by the same index. Both must survive.
	f := newChatToolCallFilter([]string{"Bash"})
	first := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"Bash","arguments":""}}]}}]}`
	second := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`

	if out := applyFilter(t, f, first); !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("declared tool dropped in name fragment: %s", out)
	}
	// The fragment carries escaped JSON inside the arguments string, so match
	// the decoded content rather than a raw byte sequence.
	if out := applyFilter(t, f, second); !strings.Contains(out, `command`) {
		t.Fatalf("argument fragment dropped: %s", out)
	}
}

func TestFilterDropsArgumentFragmentsOfDroppedCall(t *testing.T) {
	// Once a call is judged phantom, its later argument fragments must be
	// suppressed too, or the client receives orphaned arguments.
	f := newChatToolCallFilter([]string{"Bash"})
	first := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"frobnicate","arguments":""}}]}}]}`
	second := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]}}]}`

	if out := applyFilter(t, f, first); strings.Contains(out, "frobnicate") {
		t.Fatalf("phantom name leaked: %s", out)
	}
	if out := applyFilter(t, f, second); strings.Contains(out, `{\"x\":1}`) {
		t.Fatalf("orphan argument fragment leaked: %s", out)
	}
}

func TestFilterLeavesNonToolLinesByteIdentical(t *testing.T) {
	f := newChatToolCallFilter([]string{"Bash"})
	lines := []string{
		`{"id":"1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}],"usage":null}`,
		`{"id":"1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`{"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}
	for _, line := range lines {
		out, changed := f.filterChatToolCalls([]byte(line))
		if changed {
			t.Fatalf("line should be untouched but was rewritten:\n in: %s\nout: %s", line, out)
		}
		if string(out) != line {
			t.Fatalf("line mutated:\n in: %s\nout: %s", line, out)
		}
	}
}

func TestFilterPreservesUsageAndUnknownFields(t *testing.T) {
	f := newChatToolCallFilter([]string{"Bash"})
	line := `{"id":"1","object":"chat.completion.chunk","created":123,"system_fingerprint":"fp","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"frobnicate","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}`

	out := applyFilter(t, f, line)
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("filtered line is not valid JSON: %v\n%s", err, out)
	}
	if got["id"] != "1" || got["object"] != "chat.completion.chunk" {
		t.Fatalf("top-level fields lost: %s", out)
	}
	if _, ok := got["created"]; !ok {
		t.Fatalf("unknown field 'created' dropped: %s", out)
	}
	if _, ok := got["system_fingerprint"]; !ok {
		t.Fatalf("unknown field 'system_fingerprint' dropped: %s", out)
	}
	usage, ok := got["usage"].(map[string]any)
	if !ok || usage["prompt_tokens"] != float64(9) {
		t.Fatalf("usage block lost: %s", out)
	}
}

func TestDeclaredOpenAIToolNamesReadsBothShapes(t *testing.T) {
	chatBody := []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}]}`)
	got := declaredOpenAIToolNames(chatBody)
	if len(got) != 2 || got[0] != "Bash" || got[1] != "Read" {
		t.Fatalf("unexpected declared names: %#v", got)
	}
	responsesBody := []byte(`{"tools":[{"type":"function","name":"shell"},{"type":"function","name":"read"}]}`)
	got = declaredOpenAIToolNames(responsesBody)
	if len(got) != 2 || got[0] != "shell" || got[1] != "read" {
		t.Fatalf("unexpected declared names (responses shape): %#v", got)
	}
	if declaredOpenAIToolNames([]byte(`not json`)) != nil {
		t.Fatal("invalid JSON should yield no declared names")
	}
}

// TestRelayFiltersSplitLines drives the relay the way io.Copy does: arbitrary
// byte chunks that do not align with SSE line boundaries.
func TestRelayFiltersSplitLines(t *testing.T) {
	var dst bytes.Buffer
	relay := &openAIStreamRelay{
		dst:      &dst,
		flusher:  nopFlusher{},
		log:      &strings.Builder{},
		logLimit: 1 << 20,
		filter:   newChatToolCallFilter([]string{"Bash"}),
	}
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"

	// Feed one byte at a time: the worst case for line buffering.
	for i := 0; i < len(stream); i++ {
		if _, err := relay.Write([]byte{stream[i]}); err != nil {
			t.Fatalf("write failed at %d: %v", i, err)
		}
	}
	if err := relay.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	out := dst.String()
	if strings.Contains(out, `"name":"shell"`) {
		t.Fatalf("phantom marker leaked through byte-wise relay: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("expected renamed Bash in relay output: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("DONE sentinel lost: %s", out)
	}
	if !strings.Contains(out, "\n\n") {
		t.Fatalf("SSE event framing lost: %q", out)
	}
}

type nopFlusher struct{}

func (nopFlusher) Flush() {}

// TestRelayPreservesSSEFramingOnRewrite is the regression test for a rewrite
// bug: a filtered line must keep its "data:" prefix and its original single
// line terminator. Dropping the prefix makes the client ignore the chunk as a
// nameless event, and an extra newline injects a blank event into the stream.
func TestRelayPreservesSSEFramingOnRewrite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"unix newline", "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
		{"crlf newline", "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\r\n\r\ndata: [DONE]\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			relay := &openAIStreamRelay{
				dst:      &dst,
				flusher:  nopFlusher{},
				log:      &strings.Builder{},
				logLimit: 1 << 20,
				filter:   newChatToolCallFilter([]string{"Bash"}),
			}
			for i := 0; i < len(tc.input); i++ {
				if _, err := relay.Write([]byte{tc.input[i]}); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if err := relay.Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			out := dst.String()
			if !strings.Contains(out, `"name":"Bash"`) {
				t.Fatalf("rename did not survive: %q", out)
			}
			// Every emitted event must still be a data event.
			for _, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\n") {
				line = strings.TrimRight(line, "\r")
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "data:") {
					t.Fatalf("non-data line emitted, framing corrupted: %q (full: %q)", line, out)
				}
			}
			// Exactly one terminator per line: a rewritten line must not gain a
			// second newline (that would inject a blank event).
			if strings.Contains(out, "}\n\n\n") || strings.Contains(out, "}\r\n\r\n\r\n") {
				t.Fatalf("rewrite added an extra newline: %q", out)
			}
		})
	}
}

// A payload containing newlines (pretty-printed JSON) must not be split across
// events, and the [DONE] sentinel must pass through untouched.
func TestRelayKeepsMultilineJSONPayloadIntact(t *testing.T) {
	var dst bytes.Buffer
	relay := &openAIStreamRelay{
		dst:      &dst,
		flusher:  nopFlusher{},
		log:      &strings.Builder{},
		logLimit: 1 << 20,
		filter:   newChatToolCallFilter([]string{"Bash"}),
	}
	in := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\\nb\"}}]}\n\ndata: [DONE]\n\n"
	if _, err := relay.Write([]byte(in)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := relay.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := dst.String(); got != in {
		t.Fatalf("non-tool line mutated:\n in: %q\nout: %q", in, got)
	}
}

func TestRelayPassesNonJSONLinesThrough(t *testing.T) {
	var dst bytes.Buffer
	relay := &openAIStreamRelay{
		dst:      &dst,
		flusher:  nopFlusher{},
		log:      &strings.Builder{},
		logLimit: 1 << 20,
		filter:   newChatToolCallFilter([]string{"Bash"}),
	}
	in := ": keep-alive comment\nevent: ping\ndata: [DONE]\n\n"
	if _, err := relay.Write([]byte(in)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := dst.String(); got != in {
		t.Fatalf("non-JSON lines mutated:\n in: %q\nout: %q", in, got)
	}
}
