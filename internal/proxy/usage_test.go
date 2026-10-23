package proxy

import "testing"

// A cached prefix must count exactly once against the client's context window,
// including after converting the response back to OpenAI's inclusive format.
func TestCachedInputAccounting(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		input, cached, wantInput, wantCached int
	}{
		{"uncached", 100, 0, 100, 0},
		{"partly cached", 100, 80, 20, 80},
		{"fully cached", 100, 100, 0, 100},
		{"cache exceeds total", 100, 120, 0, 100},
		{"negative cache", 100, -1, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &OpenAIResponse{Usage: OpenAIUsage{
				PromptTokens: tc.input, CompletionTokens: 7,
				PromptTokensDetails: &OpenAIPromptTokensDetails{CachedTokens: tc.cached},
			}}
			u := ConvertResponse(in, "test").Usage
			if u.InputTokens != tc.wantInput || u.CacheReadInputTokens != tc.wantCached || u.TotalInputTokens() != tc.input {
				t.Fatalf("Anthropic usage: %+v", u)
			}
			back := anthropicResponsesUsage(u)
			if back.InputTokens != tc.input || back.TotalTokens != tc.input+7 || back.InputTokensDetails.CachedTokens != tc.wantCached {
				t.Fatalf("round-trip usage: %+v", back)
			}
		})
	}
}

func TestAnthropicTotalInputIncludesCacheCreation(t *testing.T) {
	u := AnthropicUsage{InputTokens: 10, CacheReadInputTokens: 80, CacheCreationInputTokens: 20, OutputTokens: 3}
	back := anthropicResponsesUsage(u)
	if back.InputTokens != 110 || back.TotalTokens != 113 {
		t.Fatalf("usage: %+v", back)
	}
}
