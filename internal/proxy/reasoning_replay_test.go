package proxy

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"
)

func TestRequiresReasoningReplay(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"deepseek-v4.1-flash-free", true},
		{"deepseek-v4-pro", true},
		{"oc/deepseek-v4.1-flash-free", true},
		{"deepseek/deepseek-chat", true},
		{"DEEPSEEK-V4-PRO", true},
		{"glm-5.3-flash-free", false},
		{"kimi-k2.6-free", false},
		{"grok-4.1-fast-free", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := RequiresReasoningReplay(tc.model); got != tc.want {
			t.Errorf("RequiresReasoningReplay(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// deepseekAssistantFixtures builds a Messages request whose assistant history
// mixes a reasoned tool call, a bare tool call (the model produced no thinking
// for that turn, e.g. Claude Code replays it without a thinking block) and a
// text-only turn.
func deepseekAssistantFixtures(model string) *AnthropicRequest {
	return &AnthropicRequest{
		Model:     model,
		MaxTokens: 512,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicMessageContent{IsStr: true, Text: "fix it"}},
			{Role: "assistant", Content: AnthropicMessageContent{Blocks: []AnthropicContent{
				{Type: "thinking", Thinking: "plan the edit"},
				{Type: "tool_use", ID: "call_reasoned", Name: "Edit", Input: jsonRawMessage(`{"file":"a"}`)},
			}}},
			{Role: "user", Content: AnthropicMessageContent{Blocks: []AnthropicContent{
				{Type: "tool_result", ToolUseID: "call_reasoned", Content: &AnthropicMessageContent{IsStr: true, Text: "ok"}},
			}}},
			{Role: "assistant", Content: AnthropicMessageContent{Blocks: []AnthropicContent{
				{Type: "tool_use", ID: "call_bare", Name: "Edit", Input: jsonRawMessage(`{"file":"b"}`)},
			}}},
			{Role: "user", Content: AnthropicMessageContent{Blocks: []AnthropicContent{
				{Type: "tool_result", ToolUseID: "call_bare", Content: &AnthropicMessageContent{IsStr: true, Text: "ok"}},
			}}},
			{Role: "assistant", Content: AnthropicMessageContent{Blocks: []AnthropicContent{
				{Type: "text", Text: "done"},
			}}},
		},
	}
}

func assistantReasoningOnWire(t *testing.T, out *OpenAIRequest) []map[string]json.RawMessage {
	t.Helper()
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	var assistants []map[string]json.RawMessage
	for _, m := range decoded.Messages {
		var role string
		if err := json.Unmarshal(m["role"], &role); err != nil {
			t.Fatalf("decode role: %v", err)
		}
		if role == "assistant" {
			assistants = append(assistants, m)
		}
	}
	return assistants
}

func TestConvertRequestDeepSeekCarriesReasoningContentOnEveryAssistantTurn(t *testing.T) {
	resetReasoningContentCacheForTest()
	out := ConvertRequest(deepseekAssistantFixtures("oc/deepseek-v4.1-flash-free"),
		func(string) string { return "deepseek-v4.1-flash-free" })

	assistants := assistantReasoningOnWire(t, out)
	if len(assistants) != 3 {
		t.Fatalf("assistant messages = %d, want 3", len(assistants))
	}
	for i, msg := range assistants {
		if _, ok := msg["reasoning_content"]; !ok {
			t.Fatalf("assistant[%d] is missing reasoning_content: %s", i, msg["content"])
		}
	}
	var reasoned, bare, textOnly string
	if err := json.Unmarshal(assistants[0]["reasoning_content"], &reasoned); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(assistants[1]["reasoning_content"], &bare); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(assistants[2]["reasoning_content"], &textOnly); err != nil {
		t.Fatal(err)
	}
	if reasoned != "plan the edit" {
		t.Errorf("reasoned turn reasoning_content = %q", reasoned)
	}
	if bare != "" || textOnly != "" {
		t.Errorf("bare/text turns reasoning_content = (%q, %q), want explicit empty", bare, textOnly)
	}
}

func TestConvertRequestNonDeepSeekOmitsAbsentReasoningContent(t *testing.T) {
	resetReasoningContentCacheForTest()
	out := ConvertRequest(deepseekAssistantFixtures("glm-5.3-flash-free"),
		func(string) string { return "glm-5.3-flash-free" })

	assistants := assistantReasoningOnWire(t, out)
	if len(assistants) != 3 {
		t.Fatalf("assistant messages = %d, want 3", len(assistants))
	}
	if _, ok := assistants[0]["reasoning_content"]; !ok {
		t.Errorf("reasoned turn lost reasoning_content")
	}
	for i := 1; i < len(assistants); i++ {
		if _, ok := assistants[i]["reasoning_content"]; ok {
			t.Errorf("assistant[%d] gained reasoning_content for a non-replay model", i)
		}
	}
}

func TestConvertResponsesRequestDeepSeekCarriesReasoningContent(t *testing.T) {
	in := &ResponsesRequest{
		Model: "deepseek-v4.1-flash-free",
		Input: jsonRawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
			{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
		]`),
	}
	out, err := ConvertResponsesRequest(in, func(string) string { return "deepseek-v4.1-flash-free" })
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	assistants := assistantReasoningOnWire(t, out)
	if len(assistants) == 0 {
		t.Fatalf("no assistant messages converted: %+v", out.Messages)
	}
	for i, msg := range assistants {
		if _, ok := msg["reasoning_content"]; !ok {
			t.Fatalf("assistant[%d] is missing reasoning_content", i)
		}
	}
}

func TestEnsureRawReasoningReplay(t *testing.T) {
	payload := map[string]json.RawMessage{
		"model": json.RawMessage(`"deepseek-v4.1-flash-free"`),
		"messages": json.RawMessage(`[
			{"role":"user","content":"hi"},
			{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"run","arguments":"{}"}}]},
			{"role":"assistant","reasoning_content":"real reasoning","content":"why"},
			{"role":"assistant","reasoning_content":null,"content":"null case"}
		]`),
	}
	if err := EnsureRawReasoningReplay(payload); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(payload["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	var bare, nullCase string
	if err := json.Unmarshal(messages[1]["reasoning_content"], &bare); err != nil {
		t.Fatalf("bare assistant lost reasoning_content: %v", err)
	}
	if err := json.Unmarshal(messages[3]["reasoning_content"], &nullCase); err != nil {
		t.Fatalf("null assistant lost reasoning_content: %v", err)
	}
	if bare != "" || nullCase != "" {
		t.Errorf("injected reasoning_content = (%q, %q), want empty strings", bare, nullCase)
	}
	if _, ok := messages[1]["reasoning_content"]; !ok {
		t.Error("bare assistant is missing reasoning_content")
	}
	var kept string
	if err := json.Unmarshal(messages[2]["reasoning_content"], &kept); err != nil || kept != "real reasoning" {
		t.Errorf("existing reasoning_content = %q (%v), want preserved", kept, err)
	}
	if _, ok := payload["messages"]; !ok {
		t.Fatal("messages key vanished")
	}
	// Idempotent: a second pass must not rewrite the array.
	before := string(payload["messages"])
	if err := EnsureRawReasoningReplay(payload); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if after := string(payload["messages"]); after != before {
		t.Errorf("second pass changed messages:\nbefore %s\nafter  %s", before, after)
	}
}

func TestMessageExplicitEmptyReasoningSurvivesJSONRoundTrip(t *testing.T) {
	var message OpenAIMessage
	if err := json.Unmarshal([]byte(`{
		"role":"assistant",
		"content":"",
		"reasoning_content":"",
		"tool_calls":[{
			"id":"call_empty_reasoning",
			"type":"function",
			"function":{"name":"shell_command","arguments":"{}"}
		}]
	}`), &message); err != nil {
		t.Fatal(err)
	}
	if !message.ReasoningContentSet || message.ReasoningContent != "" {
		t.Fatalf("empty reasoning state = (%q, %v), want explicit empty", message.ReasoningContent, message.ReasoningContentSet)
	}
	wire, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"reasoning_content":""`)) {
		t.Fatalf("explicit empty reasoning_content missing after round trip: %s", wire)
	}
	// A message that never carried the field must not gain it.
	var plain OpenAIMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hi"}`), &plain); err != nil {
		t.Fatal(err)
	}
	plainWire, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plainWire, []byte("reasoning_content")) {
		t.Fatalf("absent reasoning_content was invented: %s", plainWire)
	}
}

// A provider that returns an explicit empty reasoning_content alongside a tool
// call must have that field replayed after compaction instead of being dropped.
func TestStreamExplicitEmptyReasoningReplayedAfterCompaction(t *testing.T) {
	resetReasoningContentCacheForTest()
	var chunk OpenAIStreamChunk
	if err := json.Unmarshal([]byte(`{
		"choices":[{
			"index":0,
			"delta":{
				"reasoning_content":"",
				"tool_calls":[{
					"index":0,
					"id":"call_empty_reasoning",
					"type":"function",
					"function":{"name":"shell_command","arguments":"{\"command\":\"pwd\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}]
	}`), &chunk); err != nil {
		t.Fatal(err)
	}
	if !chunk.Choices[0].Delta.ReasoningContentSet {
		t.Fatal("explicit empty reasoning_content was treated as absent")
	}

	var out bytes.Buffer
	conv, err := NewStreamConverter(&out, "glm-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.HandleChunk(&chunk); err != nil {
		t.Fatal(err)
	}
	if err := conv.Finalize("tool_use"); err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`"type":"tool_use","id":"([^"]+)"`).FindStringSubmatch(out.String())
	if len(match) != 2 {
		t.Fatalf("tool_use id missing in stream:\n%s", out.String())
	}

	// A non-deepseek model exercises the cache path directly: the replayed
	// field survives compaction because the provider marked it present.
	req := ConvertRequest(&AnthropicRequest{
		Model:     "glm-5.3-flash-free",
		MaxTokens: 128,
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: AnthropicMessageContent{Blocks: []AnthropicContent{{
				Type: "tool_use", ID: match[1], Name: "shell_command", Input: jsonRawMessage(`{"command":"pwd"}`),
			}}},
		}},
	}, func(model string) string { return model })
	if !req.Messages[0].ReasoningContentSet || req.Messages[0].ReasoningContent != "" {
		t.Fatalf("empty reasoning replay state = (%q, %v), want explicit empty",
			req.Messages[0].ReasoningContent, req.Messages[0].ReasoningContentSet)
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"reasoning_content":""`)) {
		t.Fatalf("explicit empty reasoning_content missing on wire: %s", wire)
	}
}
