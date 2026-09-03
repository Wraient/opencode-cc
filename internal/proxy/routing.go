package proxy

import "strings"

// IsNativeAnthropicModel reports whether a Zen model id should use the native
// Anthropic Messages upstream path instead of OpenAI-compatible translation.
func IsNativeAnthropicModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	if slash := strings.IndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "qwen")
}

// IsResponsesNativeModel reports whether a Zen model id is served ONLY on the
// upstream Responses API (/v1/responses). Such models 500 upstream on
// /v1/chat/completions and /v1/messages, so callers must use the passthrough
// path (or fail fast) instead of translating.
func IsResponsesNativeModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	if slash := strings.IndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return strings.HasPrefix(model, "muse-spark")
}
