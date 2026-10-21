package server

import (
	"strings"

	"github.com/Kiowx/opencode-cc/internal/config"
	"github.com/Kiowx/opencode-cc/internal/proxy"
)

func applyThinkingBudgetMapping(req *proxy.OpenAIRequest, areq *proxy.AnthropicRequest, targetModel string, cfg *config.Config) {
	if req == nil || areq == nil || cfg == nil {
		return
	}
	mapping, ok := cfg.ResolveThinkingBudgetMapping(targetModel)
	if !ok {
		return
	}

	thinkingType := "enabled"
	budgetTokens := 0
	if areq.Thinking != nil {
		thinkingType = strings.ToLower(areq.Thinking.Type)
		budgetTokens = areq.Thinking.BudgetTokens
		if thinkingType == "" {
			return
		}
	}

	switch strings.ToLower(mapping.Field) {
	case "thinking":
		// Anthropic's adaptive mode is equivalent to the provider-specific
		// auto mode accepted by GLM's OpenAI-compatible endpoint.
		if thinkingType == "adaptive" {
			thinkingType = "auto"
		}
		req.Thinking = &proxy.OpenAIThinking{Type: thinkingType}
		if thinkingType == "enabled" && budgetTokens > 0 {
			req.Thinking.BudgetTokens = &budgetTokens
		}
	case "thinking_budget":
		if thinkingType != "enabled" {
			return
		}
		if budget := mappedThinkingBudget(budgetTokens, mapping); budget > 0 {
			req.ThinkingBudget = &budget
		}
	case "reasoning_effort":
		if thinkingType != "enabled" {
			return
		}
		req.ReasoningEffort = thinkingLevel(budgetTokens)
	case "", "none":
		return
	}
}

// outputConfigEffortFor returns the chat `reasoning_effort` implied by a
// client's output_config.effort (the effort-2025-11-24 beta channel used by
// current Claude Code and T3), or "" when the field is absent.
func outputConfigEffortFor(areq *proxy.AnthropicRequest, levels []string) string {
	if areq == nil || areq.OutputConfig == nil {
		return ""
	}
	return proxy.ChatEffortFor(areq.OutputConfig.Effort, levels)
}

// applyOutputConfigEffort carries output_config.effort onto the chat bridge's
// reasoning_effort field. Without this the level is parsed, the request runs
// at the provider default, and the client's "max effort" is silently ignored
// for every non-Responses target (deepseek, glm, kimi, ...).
func applyOutputConfigEffort(req *proxy.OpenAIRequest, areq *proxy.AnthropicRequest, targetModel string, cfg *config.Config, levels []string) {
	if req == nil || areq == nil {
		return
	}
	// A thinking-budget mapping already chose how this model takes its effort
	// (thinking object / budget / reasoning_effort); that is the more specific
	// instruction, so don't stack a second channel on top.
	if req.ReasoningEffort != "" {
		return
	}
	if cfg != nil {
		if _, ok := cfg.ResolveThinkingBudgetMapping(targetModel); ok {
			return
		}
	}
	req.ReasoningEffort = outputConfigEffortFor(areq, levels)
}

func applyResponsesThinkingMapping(req *proxy.OpenAIRequest, targetModel string, cfg *config.Config) {
	if req == nil || cfg == nil {
		return
	}
	mapping, ok := cfg.ResolveThinkingBudgetMapping(targetModel)
	if !ok || strings.ToLower(mapping.Field) != "thinking" {
		return
	}
	req.Thinking = &proxy.OpenAIThinking{
		Type: "enabled",
	}
}

func mappedThinkingBudget(source int, mapping config.ThinkingBudgetMapping) int {
	switch thinkingLevel(source) {
	case "low":
		if mapping.Low > 0 {
			return mapping.Low
		}
	case "medium":
		if mapping.Medium > 0 {
			return mapping.Medium
		}
	case "high":
		if mapping.High > 0 {
			return mapping.High
		}
	case "max":
		if mapping.Max > 0 {
			return mapping.Max
		}
	}
	if source > 0 {
		return source
	}
	return 0
}

func thinkingLevel(budgetTokens int) string {
	switch {
	case budgetTokens <= 0:
		return "max"
	case budgetTokens <= 1024:
		return "low"
	case budgetTokens <= 4096:
		return "medium"
	case budgetTokens <= 8192:
		return "high"
	default:
		return "max"
	}
}

func boolRef(v bool) *bool {
	return &v
}
