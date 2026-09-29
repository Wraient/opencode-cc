package proxy

import "strings"

// Nameless-call recovery.
//
// Zen sometimes streams ONLY response.function_call_arguments.delta frames
// for a call: no output_item.added, no done — so the function name never
// arrives on the wire (verified live: item fc_*, output_index 2, args only).
// Dropping such calls turns them into text-only end_turn responses and
// stalls agent loops ("say continue"). When the accumulated arguments
// uniquely identify one declared tool by its input schema, the call is
// attributed to that tool instead of being dropped.

// toolSchema is the matchable surface of a declared tool's input schema.
type toolSchema struct {
	name       string
	required   map[string]bool
	properties map[string]bool
	hasSchema  bool
}

func parseToolSchema(name string, raw jsonRawMessage) toolSchema {
	s := toolSchema{name: name, required: map[string]bool{}, properties: map[string]bool{}}
	if len(raw) == 0 {
		return s
	}
	var v struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := jsonUnmarshal(raw, &v); err != nil {
		return s
	}
	s.hasSchema = true
	for _, r := range v.Required {
		s.required[r] = true
	}
	for p := range v.Properties {
		s.properties[p] = true
	}
	return s
}

// RecoverNamelessTool attributes accumulated call arguments to a declared
// tool when exactly one declared tool's schema fits: every required
// property is present in args, and every args key is a known property
// (when the schema names properties). ok is false on any ambiguity —
// callers must drop+log rather than guess.
//
// declared carries the client's tool names; schemas maps name -> raw
// input_schema and may be sparse (tools without a parsed schema only match
// by required-emptiness, i.e. they match empty args).
func RecoverNamelessTool(args string, declared []string, schemas map[string]jsonRawMessage) (string, bool) {
	name, _ := MatchNamelessTool(args, declared, schemas)
	return name, name != ""
}

// NamelessFailureReason explains WHY MatchNamelessTool found no unique tool.
// It is empty when a match exists. Callers log it so the next stall carries
// its own diagnosis (empty declared list vs unparseable args vs no fit vs
// tie) instead of a bare drop.
func NamelessFailureReason(args string, declared []string, schemas map[string]jsonRawMessage) string {
	_, reason := MatchNamelessTool(args, declared, schemas)
	return reason
}

// MatchNamelessTool is the shared core behind RecoverNamelessTool and
// NamelessFailureReason. It returns the matched tool name ("" on failure)
// and the failure reason ("" on success).
//
// Matching proceeds in two passes over the declared tools:
//
//  1. Structural fit: every required property present in args, and every
//     args key a known property (when the schema names properties).
//  2. Tie-break among structural fits: highest required-count wins; a
//     required-count tie breaks toward the most arg-key overlap
//     (|args ∩ properties|). A residual tie is a true ambiguity and fails
//     with reason "schema-tie:<names>" — callers must drop+log, never guess.
func MatchNamelessTool(args string, declared []string, schemas map[string]jsonRawMessage) (string, string) {
	args = strings.TrimSpace(args)
	if args == "" || args == "{}" || len(declared) == 0 {
		if len(declared) == 0 {
			return "", "no-declared-tools"
		}
		return "", "args-empty"
	}
	var obj map[string]any
	if err := jsonUnmarshal([]byte(args), &obj); err != nil {
		return "", "args-unparseable"
	}
	keys := map[string]bool{}
	for k := range obj {
		keys[k] = true
	}
	type scored struct {
		name    string
		require int
		overlap int
	}
	var fits []scored
	for _, d := range declared {
		if d == "" {
			continue
		}
		s := parseToolSchema(d, schemas[d])
		fitsTool := true
		for r := range s.required {
			if !keys[r] {
				fitsTool = false
				break
			}
		}
		if !fitsTool {
			continue
		}
		overlap := 0
		if s.hasSchema && len(s.properties) > 0 {
			for k := range keys {
				if s.properties[k] {
					overlap++
				} else {
					fitsTool = false
					break
				}
			}
		}
		if !fitsTool {
			continue
		}
		fits = append(fits, scored{d, len(s.required), overlap})
	}
	if len(fits) == 0 {
		return "", "no-schema-fit"
	}
	// Unique winner: the single candidate, or the strict best by
	// (required-count, overlap) in that order.
	best, tied := fits[0], []scored{}
	for _, b := range fits[1:] {
		switch {
		case b.require > best.require || (b.require == best.require && b.overlap > best.overlap):
			best, tied = b, []scored{}
		case b.require == best.require && b.overlap == best.overlap:
			tied = append(tied, b)
		}
	}
	if len(tied) > 0 {
		names := best.name
		for _, t := range tied {
			names += "," + t.name
		}
		return "", "schema-tie:" + names
	}
	return best.name, ""
}
