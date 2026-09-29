package proxy

// Stall-repro baselines: deterministic replays of the upstream frame shapes
// behind the "says it, doesn't do it, needs continue" agent-loop stall.
//
// Each test feeds a recorded failure shape through one of the two streaming
// translators and asserts the fixed behavior. Pre-fix these fail with a
// text-only end_turn (the stall); post-fix they emit tool_use. They run
// without any network and always reproduce — the live `claude-oc -p` probe
// loop in the plan measures the same thing end to end.

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

var (
	stallBashTool = AnthropicTool{
		Name:        "Bash",
		InputSchema: jsonRawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
	}
	stallReadTool = AnthropicTool{
		Name:        "Read",
		InputSchema: jsonRawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
	}
)

// runBridgeStream feeds Responses frames through the bridge streamer with
// declared tools + schemas wired (as the server does) and returns the
// converter for diagnostics assertions plus the emitted Anthropic events.
func runBridgeStream(t *testing.T, tools []AnthropicTool, frames [][2]string) (*ResponsesToAnthropicStreamer, []anthropicStreamEvent) {
	t.Helper()
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	conv := NewResponsesToAnthropicStreamer(bw, "muse-spark-test")
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	conv.RestrictTools(names)
	conv.SetDeclaredSchemas(tools)
	feedResponsesStream(t, conv, frames)
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return conv, collectAnthropicEvents(t, &buf)
}

func stopOf(events []anthropicStreamEvent) string {
	for _, e := range events {
		if e.Type == "message_delta" {
			if s, ok := e.Delta["stop_reason"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func toolUseNames(events []anthropicStreamEvent) []string {
	var out []string
	for _, e := range events {
		if e.Type == "content_block_start" {
			if typ, _ := e.ContentBlock["type"].(string); typ == "tool_use" {
				name, _ := e.ContentBlock["name"].(string)
				out = append(out, name)
			}
		}
	}
	return out
}

// The journal shape: bare function_call_arguments.delta frames, no
// output_item.added, no done — the name never arrives on the wire. Text
// narrates the action ("let me check that:"), then response.completed ends
// the stream. Must recover Bash by schema, not stall as end_turn.
func TestStallReproNamelessDeltaOnlyCallRecovered(t *testing.T) {
	conv, events := runBridgeStream(t, []AnthropicTool{stallBashTool, stallReadTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Let me check that:"}`},
		{"response.function_call_arguments.delta", `{"item_id":"fc_01","output_index":2,"delta":"{\"command\":"}`},
		{"response.function_call_arguments.delta", `{"item_id":"fc_01","output_index":2,"delta":"\"echo hi\"}"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":70000,"output_tokens":40}}}}`},
	})
	if got := toolUseNames(events); len(got) != 1 || got[0] != "Bash" {
		t.Fatalf("tool_use blocks = %v, want [Bash]", got)
	}
	if got := stopOf(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
	if conv.RecoveredCalls() != 1 {
		t.Fatalf("recovered = %d, want 1", conv.RecoveredCalls())
	}
	if len(conv.DroppedCalls()) != 0 {
		t.Fatalf("dropped = %+v, want none", conv.DroppedCalls())
	}
}

// The orphan shape: a single tiny delta whose args fit no declared schema.
// Must still drop (never guess) — but the drop must explain itself.
func TestStallReproOrphanCallDropIsDiagnosed(t *testing.T) {
	conv, events := runBridgeStream(t, []AnthropicTool{stallBashTool, stallReadTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Doing this:"}`},
		{"response.function_call_arguments.delta", `{"item_id":"fc_02","output_index":0,"sequence_number":3,"delta":"{\"task_id\":\"bj0vlq73z\"}"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":70000,"output_tokens":10}}}}`},
	})
	if got := stopOf(events); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", got)
	}
	dropped := conv.DroppedCalls()
	if len(dropped) != 1 {
		t.Fatalf("dropped = %+v, want exactly one", dropped)
	}
	if dropped[0].Reason != "nameless:no-schema-fit" {
		t.Fatalf("reason = %q, want nameless:no-schema-fit", dropped[0].Reason)
	}
	if !strings.Contains(dropped[0].Declared, "Bash") || !strings.Contains(dropped[0].Declared, "Read") {
		t.Fatalf("declared = %q, want Bash+Read", dropped[0].Declared)
	}
}

// OpenCode-namespaced upstream names rewrite to the declared tool.
func TestStallReproNamespacedNameRewritten(t *testing.T) {
	conv, events := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"default.Bash"}}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"default.Bash","arguments":"{\"command\":\"pwd\"}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	})
	if got := toolUseNames(events); len(got) != 1 || got[0] != "Bash" {
		t.Fatalf("tool_use blocks = %v, want [Bash]", got)
	}
	if conv.RenamedToolCalls() != 1 {
		t.Fatalf("renamed = %d, want 1", conv.RenamedToolCalls())
	}
}

// A reasoning item whose done frame carries no item payload must stay a
// non-tool: no phantom "dropped upstream tool call" for it.
func TestStallReproReasoningDoneWithoutItemNotDropped(t *testing.T) {
	conv, _ := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"r","item":{"id":"rs1","type":"reasoning"}}`},
		{"response.output_item.done", `{"item_id":"r"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	})
	if len(conv.DroppedCalls()) != 0 {
		t.Fatalf("dropped = %+v, want none", conv.DroppedCalls())
	}
}

// ---- Chat lane (StreamConverter) ----

func runChatStream(t *testing.T, tools []AnthropicTool, chunks []OpenAIStreamChunk) (*StreamConverter, string) {
	t.Helper()
	var out bytes.Buffer
	conv, err := NewStreamConverter(&out, "chat-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	conv.RestrictTools(names)
	conv.SetDeclaredSchemas(tools)
	for i := range chunks {
		if err := conv.HandleChunk(&chunks[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := conv.Finalize("end_turn"); err != nil {
		t.Fatal(err)
	}
	if err := conv.Flush(); err != nil {
		t.Fatal(err)
	}
	return conv, out.String()
}

// Chat-lane mirror of the nameless stall: args-only deltas, name never
// arrives, finish says tool_calls. Must recover, not end_turn.
func TestStallReproChatNamelessArgsOnlyRecovered(t *testing.T) {
	finish := "tool_calls"
	conv, got := runChatStream(t, []AnthropicTool{stallBashTool, stallReadTool}, []OpenAIStreamChunk{
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "Doing this:"}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "call_1", Function: OpenAIFunctionCall{Arguments: `{"command"`}},
		}}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "call_1", Function: OpenAIFunctionCall{Arguments: `:"echo hi"}`}},
		}}}}},
		{Choices: []OpenAIChoice{{FinishReason: &finish}}},
	})
	for _, want := range []string{`"type":"tool_use"`, `"name":"Bash"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n---OUTPUT---\n%s", want, got)
		}
	}
	if conv.RecoveredCalls() != 1 {
		t.Fatalf("recovered = %d, want 1", conv.RecoveredCalls())
	}
	if conv.StopReason() != "tool_use" {
		t.Fatalf("StopReason() = %q, want tool_use", conv.StopReason())
	}
	if conv.UpstreamFinish() != "tool_calls" {
		t.Fatalf("UpstreamFinish() = %q, want tool_calls", conv.UpstreamFinish())
	}
}

// Parallel calls with id-less continuation deltas stay separate per index.
func TestStallReproChatParallelCallsStaySeparate(t *testing.T) {
	finish := "tool_calls"
	_, got := runChatStream(t, []AnthropicTool{stallBashTool, stallReadTool}, []OpenAIStreamChunk{
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "a", Function: OpenAIFunctionCall{Name: "Bash", Arguments: `{"command":"x"}`}},
		}}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 1, ID: "b", Function: OpenAIFunctionCall{Name: "Read", Arguments: `{"file_path"`}},
		}}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 1, Function: OpenAIFunctionCall{Arguments: `:"/f"}`}},
		}}}}},
		{Choices: []OpenAIChoice{{FinishReason: &finish}}},
	})
	for _, want := range []string{
		`"name":"Bash"`, `"partial_json":"{\"command\":\"x\"}"`,
		`"name":"Read"`, `"partial_json":"{\"file_path\":\"/f\"}"`,
		`"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n---OUTPUT---\n%s", want, got)
		}
	}
}

// Unknown names drop with a diagnosed reason and normalize to end_turn.
func TestStallReproChatUndeclaredDropIsDiagnosed(t *testing.T) {
	finish := "tool_calls"
	conv, got := runChatStream(t, []AnthropicTool{stallBashTool}, []OpenAIStreamChunk{
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "Doing this:"}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "call_x", Function: OpenAIFunctionCall{Name: "EvilTool", Arguments: `{}`}},
		}}}}},
		{Choices: []OpenAIChoice{{FinishReason: &finish}}},
	})
	if strings.Contains(got, "tool_use") {
		t.Fatalf("undeclared tool leaked:\n%s", got)
	}
	if !strings.Contains(got, `"stop_reason":"end_turn"`) {
		t.Fatalf("want end_turn:\n%s", got)
	}
	dropped := conv.DroppedCalls()
	if len(dropped) != 1 || dropped[0].Reason != "undeclared-name" {
		t.Fatalf("dropped = %+v, want one undeclared-name", dropped)
	}
	if conv.StopReason() != "end_turn" || conv.UpstreamFinish() != "tool_calls" {
		t.Fatalf("StopReason()=%q UpstreamFinish()=%q", conv.StopReason(), conv.UpstreamFinish())
	}
}

// Same index but different call identities must not merge into corrupt JSON.
func TestStallReproChatIndexCollisionSplits(t *testing.T) {
	finish := "tool_calls"
	_, got := runChatStream(t, []AnthropicTool{stallBashTool, stallReadTool}, []OpenAIStreamChunk{
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "a", Function: OpenAIFunctionCall{Name: "Bash", Arguments: `{"command":"x"}`}},
		}}}}},
		{Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCall{
			{Index: 0, ID: "b", Function: OpenAIFunctionCall{Name: "Read", Arguments: `{"file_path":"/f"}`}},
		}}}}},
		{Choices: []OpenAIChoice{{FinishReason: &finish}}},
	})
	for _, want := range []string{`"name":"Bash"`, `"name":"Read"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n---OUTPUT---\n%s", want, got)
		}
	}
}

// A turn whose text arrives ONLY in output_text.done (empty/missing
// deltas) must still surface the text, not an empty end_turn.
func TestStallReproDoneTextFallbackFires(t *testing.T) {
	conv, events := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":""}`},
		{"response.output_text.done", `{"item_id":"a","output_index":0,"text":"Done-text fallback copy"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":70000,"output_tokens":12}}}}`},
	})
	if conv.EmptyText() {
		t.Fatal("done-text fallback did not fire")
	}
	found := false
	for _, e := range events {
		if e.Type == "content_block_delta" {
			if txt, _ := e.Delta["text"].(string); strings.Contains(txt, "Done-text fallback copy") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("fallback text missing in events: %+v", events)
	}
	if got := stopOf(events); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", got)
	}
}

// Done text must never double text the deltas already delivered.
func TestStallReproDoneTextNoDoubleWhenDeltasArrived(t *testing.T) {
	_, events := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"live copy"}`},
		{"response.output_text.done", `{"item_id":"a","output_index":0,"text":"live copy"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}}`},
	})
	count := 0
	for _, e := range events {
		if e.Type == "content_block_delta" {
			if txt, _ := e.Delta["text"].(string); strings.Contains(txt, "live copy") {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("text delivered %d times, want exactly once", count)
	}
}

// The frame census reports what upstream actually sent.
func TestStallReproFrameCensus(t *testing.T) {
	conv, _ := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_text.done", `{"item_id":"a"}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}`},
	})
	census := conv.FrameCensus()
	for _, want := range []string{"response.output_text.delta:1", "response.output_text.done:1", "response.completed:1"} {
		if !strings.Contains(census, want) {
			t.Fatalf("census = %q, want %q", census, want)
		}
	}
}

// The item-type census distinguishes "upstream sent text-only" from
// "a function call vanished": added/done types plus terminal status.
func TestStallReproItemCensusDistinguishesTextOnly(t *testing.T) {
	conv, _ := runBridgeStream(t, []AnthropicTool{stallBashTool}, [][2]string{
		{"response.output_text.delta", `{"item_id":"a","delta":"Hi"}`},
		{"response.output_item.added", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"Bash"}}`},
		{"response.output_item.done", `{"item_id":"b","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"pwd\"}"}}`},
		{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}`},
	})
	census := conv.FrameCensus()
	for _, want := range []string{"items:added:function_call:1", "items:done:function_call:1", "status:completed"} {
		if !strings.Contains(census, want) {
			t.Fatalf("census = %q, want %q", census, want)
		}
	}
}

// ---- Recovery core ----

func TestMatchNamelessToolOverlapBreaksSparseTie(t *testing.T) {
	sparse := AnthropicTool{
		Name:        "Sparse",
		InputSchema: jsonRawMessage(`{"type":"object","required":["x"]}`),
	}
	rich := AnthropicTool{
		Name:        "Rich",
		InputSchema: jsonRawMessage(`{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"string"}},"required":["x"]}`),
	}
	schemas := map[string]jsonRawMessage{"Sparse": sparse.InputSchema, "Rich": rich.InputSchema}
	got, reason := MatchNamelessTool(`{"x":"1","y":"2"}`, []string{"Sparse", "Rich"}, schemas)
	if got != "Rich" || reason != "" {
		t.Fatalf("got (%q, %q), want (Rich, \"\")", got, reason)
	}
}

func TestNamelessFailureReasons(t *testing.T) {
	schemas := map[string]jsonRawMessage{"Bash": stallBashTool.InputSchema}
	cases := []struct {
		name     string
		args     string
		declared []string
		want     string
	}{
		{"no declared", `{"command":"x"}`, nil, "no-declared-tools"},
		{"empty args", `{}`, []string{"Bash"}, "args-empty"},
		{"unparseable", `{oops`, []string{"Bash"}, "args-unparseable"},
		{"no fit", `{"task_id":"x"}`, []string{"Bash"}, "no-schema-fit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NamelessFailureReason(tc.args, tc.declared, schemas); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
	if got := NamelessFailureReason(`{"x":"1"}`, []string{"A", "B"},
		map[string]jsonRawMessage{
			"A": jsonRawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`),
			"B": jsonRawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`),
		}); !strings.HasPrefix(got, "schema-tie:") {
		t.Fatalf("reason = %q, want schema-tie:…", got)
	}
}

// Malformed upstream lines are counted, not silent: a corrupt tool-call
// chunk with a tool_calls finish looks exactly like a stall.
func TestScanCountsMalformedLines(t *testing.T) {
	raw := "data: {\"choices\":[]}\n\n" +
		"data: {oops\n\n" +
		"data: [DONE]\n\n"
	res, err := ScanOpenAIStreamWithStatus(strings.NewReader(raw), func(*OpenAIStreamChunk) error {
		return nil
	})
	if err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
	if res.Malformed != 1 {
		t.Fatalf("malformed = %d, want 1", res.Malformed)
	}
}

// ---- Toolless resample verdict ----

func TestShouldResampleToolless(t *testing.T) {
	cases := []struct {
		name           string
		enabled        bool
		tools          int
		stop           string
		hasToolUse     bool
		dropped        int
		out            int
		upstreamStatus string
		wantOK         bool
		wantReason     string
	}{
		{"classic stall", true, 3, "end_turn", false, 0, 149, "completed", true, "resample"},
		{"disabled", false, 3, "end_turn", false, 0, 149, "completed", false, "disabled"},
		{"no tools declared", true, 0, "end_turn", false, 0, 50, "completed", false, "no-tools-declared"},
		{"has tools", true, 3, "tool_use", true, 0, 90, "completed", false, "not-toolless"},
		{"has drops", true, 3, "end_turn", false, 1, 40, "completed", false, "has-drops"},
		{"truncated", true, 3, "end_turn", false, 0, 40, "incomplete", false, "upstream-status:incomplete"},
		{"long answer", true, 3, "end_turn", false, 0, 2000, "completed", false, "output-too-large"},
		{"cap boundary", true, 3, "end_turn", false, 0, 500, "completed", true, "resample"},
		{"unknown status treated as completed-shape", true, 1, "end_turn", false, 0, 10, "", true, "resample"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := ShouldResampleToolless(tc.enabled, tc.tools, tc.stop, tc.hasToolUse, tc.dropped, tc.out, tc.upstreamStatus)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Fatalf("got (%v, %q), want (%v, %q)", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

// ---- Non-streaming truncation ----

func TestNonStreamTruncatedToolCallSurfacesMaxTokens(t *testing.T) {
	finish := "length"
	up := &OpenAIResponse{
		ID: "chatcmpl-trunc",
		Choices: []OpenAIChoice{{
			Message: &OpenAIMessage{
				Role:    "assistant",
				Content: "Doing this:",
				ToolCalls: []OpenAIToolCall{{
					ID:   "call_t",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "Bash",
						Arguments: `{"command":"echo`,
					},
				}},
			},
			FinishReason: &finish,
		}},
	}
	out := ConvertResponse(up, "chat-test")
	for _, b := range out.Content {
		if b.Type == "tool_use" {
			t.Fatalf("truncated call must not surface as tool_use: %+v", b)
		}
	}
	if out.StopReason == nil || *out.StopReason != "max_tokens" {
		t.Fatalf("stop_reason = %v, want max_tokens", out.StopReason)
	}
}
