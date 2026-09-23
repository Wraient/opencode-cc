package proxy

import (
	"strings"
	"testing"
)

func TestAggregateOpenAIStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"c1","model":"big-pickle","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`,
		`data: {"id":"c1","model":"big-pickle","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","model":"big-pickle","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	out, err := AggregateOpenAIStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("AggregateOpenAIStream: %v", err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message == nil || out.Choices[0].Message.Content != "hello" {
		t.Fatalf("aggregated message = %#v", out.Choices)
	}
	if out.Usage.PromptTokens != 4 || out.Usage.CompletionTokens != 2 {
		t.Fatalf("aggregated usage = %#v", out.Usage)
	}
}

func TestAggregateResponsesSSE(t *testing.T) {
	raw := []byte("event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n")
	out, err := AggregateResponsesSSE(raw)
	if err != nil {
		t.Fatalf("AggregateResponsesSSE: %v", err)
	}
	if !strings.Contains(string(out), `"id":"resp_1"`) || !strings.Contains(string(out), `"text":"ok"`) {
		t.Fatalf("aggregated response = %s", out)
	}
}
