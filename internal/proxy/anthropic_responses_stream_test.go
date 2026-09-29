package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type anthropicStreamEvent struct {
	Type         string         `json:"type"`
	Index        int            `json:"index"`
	Message      map[string]any `json:"message"`
	ContentBlock map[string]any `json:"content_block"`
	Delta        map[string]any `json:"delta"`
	Usage        map[string]any `json:"usage"`
}

func feedResponsesStream(t *testing.T, conv *ResponsesToAnthropicStreamer, frames [][2]string) []anthropicStreamEvent {
	t.Helper()
	for _, f := range frames {
		if err := conv.HandleEvent(f[0], []byte(f[1])); err != nil {
			t.Fatalf("HandleEvent %s: %v", f[0], err)
		}
	}
	if err := conv.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return nil
}

func collectAnthropicEvents(t *testing.T, buf *bytes.Buffer) []anthropicStreamEvent {
	t.Helper()
	var out []anthropicStreamEvent
	for _, frame := range strings.Split(strings.TrimSpace(buf.String()), "\n\n") {
		var event, data string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if event == "" || data == "" {
			continue
		}
		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("decode %s: %v", event, err)
		}
		ev.Type = event
		out = append(out, ev)
	}
	return out
}

func runResponsesStream(t *testing.T, restrict []string, frames [][2]string) []anthropicStreamEvent {
	t.Helper()
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "muse-spark-1.3-contributor-free")
	conv.RestrictTools(restrict)
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return collectAnthropicEvents(t, &buf)
}

func TestResponsesStreamTextAndToolCall(t *testing.T) {
	events := runResponsesStream(t, []string{"get_time"}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"get_time"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"tz\":"}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"\"UTC\"}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"get_time","arguments":"{\"tz\":\"UTC\"}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":1}}}}`},
	})
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := []string{"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence:\n got %v\nwant %v", types, want)
	}
	toolStart := events[4].ContentBlock
	if toolStart["type"] != "tool_use" || toolStart["id"] != "call_1" || toolStart["name"] != "get_time" {
		t.Errorf("tool_use start: %v", toolStart)
	}
	toolDelta := events[5].Delta
	if toolDelta["type"] != "input_json_delta" || !strings.Contains(toolDelta["partial_json"].(string), "UTC") {
		t.Errorf("tool delta: %v", toolDelta)
	}
	msgDelta := events[7]
	if msgDelta.Delta["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason: %v", msgDelta.Delta)
	}
	if msgDelta.Usage["input_tokens"].(float64) != 7 || msgDelta.Usage["output_tokens"].(float64) != 3 {
		t.Errorf("usage: %v", msgDelta.Usage)
	}
}

func TestResponsesStreamBlockIndexes(t *testing.T) {
	events := runResponsesStream(t, []string{"get_time"}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"get_time"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"get_time","arguments":"{}"}}`},
		{"response.output_text.delta", `{"item_id":"c","delta":"Bye"}`},
		{"response.output_text.done", `{"item_id":"c"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	})
	type ti struct {
		typ string
		idx int
	}
	var got []ti
	for _, e := range events {
		switch e.Type {
		case "content_block_start", "content_block_delta", "content_block_stop":
			got = append(got, ti{e.Type, e.Index})
		}
	}
	want := []ti{
		{"content_block_start", 0}, {"content_block_delta", 0}, {"content_block_stop", 0},
		{"content_block_start", 1}, {"content_block_delta", 1}, {"content_block_stop", 1},
		{"content_block_start", 2}, {"content_block_delta", 2}, {"content_block_stop", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("block events:\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block event %d:\n got %+v\nwant %+v\nfull: %v", i, got[i], want[i], got)
		}
	}
}

func TestResponsesStreamDropsUndeclaredToolAndSkipsPing(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"allowed_tool"})
	frames := [][2]string{
		{"ping", `{}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"evil_tool"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"evil_tool","arguments":"{}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "tool_use") {
		t.Errorf("undeclared tool must be dropped:\n%s", got)
	}
	for _, ev := range collectAnthropicEvents(t, &buf) {
		if ev.Type == "message_delta" && ev.Delta["stop_reason"] != "end_turn" {
			t.Errorf("stop_reason without tool_use: %v", ev.Delta)
		}
	}
}

func TestResponsesStreamResolvesMarkerAndNamespacedTools(t *testing.T) {
	// Regression test for the agent-loop stall: muse-spark calls the injected
	// free-tier marker "shell" (presented upstream as "default.shell") instead
	// of the client's declared "Bash". The call must be rewritten to the
	// declared name with stop_reason tool_use — never silently dropped into a
	// text-only end_turn (which forces the user to type "continue").
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Running it now."}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"default.shell"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"echo hi\"}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	}
	events := runResponsesStream(t, []string{"Bash"}, frames)
	var toolName, stop string
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock["type"] == "tool_use" {
			toolName, _ = e.ContentBlock["name"].(string)
		}
		if e.Type == "message_delta" {
			stop, _ = e.Delta["stop_reason"].(string)
		}
	}
	if toolName != "Bash" {
		t.Errorf("marker tool rewritten to: %q, want %q", toolName, "Bash")
	}
	if stop != "tool_use" {
		t.Errorf("stop_reason: %q, want %q", stop, "tool_use")
	}
}

func TestResponsesStreamStillDropsHallucinatedTools(t *testing.T) {
	// Genuinely undeclared tools must still be dropped (existing behavior),
	// and now recorded for diagnostics.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash"})
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"evil_tool"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"evil_tool","arguments":"{}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if strings.Contains(buf.String(), "tool_use") {
		t.Errorf("hallucinated tool must be dropped:\n%s", buf.String())
	}
	if got := conv.DroppedCalls(); len(got) != 1 || got[0].Name != "evil_tool" {
		t.Errorf("DroppedCalls() = %v, want [evil_tool]", got)
	} else {
		d := got[0]
		if !d.SawAdded || !d.SawDone || d.AddedType != "function_call" {
			t.Errorf("dropped call shape = %+v, want added+done function_call", d)
		}
	}
	if got := conv.StopReason(); got != "end_turn" {
		t.Errorf("StopReason() = %q, want end_turn", got)
	}
}

func TestResponsesStreamDoneArgsReplaceDeltas(t *testing.T) {
	// output_item.done carries the complete arguments while deltas streamed
	// the same content as fragments: the emitted input must be the single
	// JSON object, never doubled ("{...}{...}"), or clients fail to parse it.
	events := runResponsesStream(t, []string{"Bash"}, [][2]string{
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"Bash"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":"}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"echo hi\"}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	})
	var partial string
	for _, e := range events {
		if e.Type == "content_block_delta" {
			if s, ok := e.Delta["partial_json"].(string); ok {
				partial = s
			}
		}
	}
	if partial != `{"command":"echo hi"}` {
		t.Errorf("input_json_delta = %q, want single object", partial)
	}
}

func TestResponsesStreamNamelessDropShape(t *testing.T) {
	// The live stall shape: args stream in but the name never arrives in any
	// frame. With two declared tools and no schemas the call is ambiguous:
	// dropped (no name to emit) and recorded with its shape so the server
	// log shows WHY (added/done tracking + args head).
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash", "Read"})
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Running it."}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if strings.Contains(buf.String(), "tool_use") {
		t.Errorf("ambiguous nameless tool must be dropped:\n%s", buf.String())
	}
	got := conv.DroppedCalls()
	if len(got) != 1 {
		t.Fatalf("DroppedCalls() = %v, want 1 entry", got)
	}
	d := got[0]
	if d.Name != "" {
		t.Errorf("dropped name = %q, want empty", d.Name)
	}
	if d.SawAdded {
		t.Errorf("saw_added = true, want false (no added frame sent)")
	}
	if d.ArgsLen == 0 || d.ArgsHead == "" {
		t.Errorf("args not recorded: %+v", d)
	}
	if got := conv.StopReason(); got != "end_turn" {
		t.Errorf("StopReason() = %q, want end_turn", got)
	}
}

func TestResponsesStreamNamelessRecoveredBySchema(t *testing.T) {
	// Same wire shape, but the args uniquely match one declared schema:
	// the call is attributed instead of stalling the loop.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash", "Read"})
	conv.SetDeclaredSchemas([]AnthropicTool{
		{Name: "Bash", InputSchema: jsonRawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)},
		{Name: "Read", InputSchema: jsonRawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`)},
	})
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Running it."}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	events := collectAnthropicEvents(t, &buf)
	var toolName, stop string
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock["type"] == "tool_use" {
			toolName, _ = e.ContentBlock["name"].(string)
		}
		if e.Type == "message_delta" {
			stop, _ = e.Delta["stop_reason"].(string)
		}
	}
	if toolName != "Bash" {
		t.Errorf("recovered tool = %q, want Bash", toolName)
	}
	if stop != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stop)
	}
	if got := conv.RecoveredCalls(); got != 1 {
		t.Errorf("RecoveredCalls() = %d, want 1", got)
	}
	if got := conv.DroppedCalls(); len(got) != 0 {
		t.Errorf("DroppedCalls() = %v, want none", got)
	}
}

func TestResponsesStreamSkipsReasoningItemsSilently(t *testing.T) {
	// A "reasoning" output item is never a tool candidate: no tool_use, no
	// drop record, still end_turn.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash"})
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"r","item":{"id":"rs1","type":"reasoning"}}`},
		{"response.output_item.done", `{"item_id":"r","item":{"id":"rs1","type":"reasoning"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if strings.Contains(buf.String(), "tool_use") {
		t.Errorf("reasoning item must not become a tool:\n%s", buf.String())
	}
	if got := conv.DroppedCalls(); len(got) != 0 {
		t.Errorf("DroppedCalls() = %v, want none", got)
	}
}

func TestResponsesStreamTopLevelFallbacks(t *testing.T) {
	// Zen flavor: name/call_id/arguments ride on the frame top level rather
	// than nested under item. The call must still be emitted whole.
	events := runResponsesStream(t, []string{"Bash"}, [][2]string{
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":"}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","name":"Bash","delta":"\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b","name":"Bash","call_id":"call_9","arguments":"{\"command\":\"echo hi\"}"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	})
	var toolName, callID, stop string
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock["type"] == "tool_use" {
			toolName, _ = e.ContentBlock["name"].(string)
			callID, _ = e.ContentBlock["id"].(string)
		}
		if e.Type == "message_delta" {
			stop, _ = e.Delta["stop_reason"].(string)
		}
	}
	if toolName != "Bash" || callID != "call_9" {
		t.Errorf("tool = %q id = %q, want Bash call_9", toolName, callID)
	}
	if stop != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stop)
	}
}

func TestResponsesStreamBareDoneStaysSilent(t *testing.T) {
	// A done marker for an item that never carried anything loses nothing.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash"})
	frames := [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.done", `{"item_id":"zzz"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := conv.DroppedCalls(); len(got) != 0 {
		t.Errorf("DroppedCalls() = %v, want none", got)
	}
}

func TestResponsesStreamNontoolWithArgsAlarms(t *testing.T) {
	// A non-function item that accumulated call arguments is a real call
	// hiding behind another item type: must be recorded, not skipped.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "m")
	conv.RestrictTools([]string{"Bash"})
	frames := [][2]string{
		{"response.output_item.added", `{"item_id":"b","item":{"id":"x1","type":"mcp_call"}}`},
		{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{\"command\":\"echo hi\"}"}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"x1","type":"mcp_call"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}}`},
	}
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := conv.DroppedCalls()
	if len(got) != 1 {
		t.Fatalf("DroppedCalls() = %v, want 1 entry", got)
	}
	if got[0].AddedType != "mcp_call" || got[0].ArgsLen == 0 {
		t.Errorf("drop shape = %+v, want mcp_call with args", got[0])
	}
}
