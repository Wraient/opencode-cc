package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ---- Anthropic SSE wire payloads (exact field names) ----
//
// Each is marshalled and wrapped as:
//   event: <Type>\n
//   data: <json>\n\n

type sseMessageStartData struct {
	Type string `json:"type"`
}

// streamMessageStart is the full {"type":"message_start","message":{...}}.
type streamMessageStart struct {
	Type    string        `json:"type"`
	Message streamMessage `json:"message"`
}

type streamMessage struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	StopReason   *string        `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        AnthropicUsage `json:"usage"`
	Content      []any          `json:"content"`
}

type streamContentBlockStart struct {
	Type         string           `json:"type"`
	Index        int              `json:"index"`
	ContentBlock streamContentRef `json:"content_block"`
}

// streamContentRef is the minimal block description sent on block start. For
// tool_use we send {type,id,name,input:{}}; the body comes via input_json_delta.
type streamContentRef struct {
	Type     string         `json:"type"`
	Text     *string        `json:"text,omitempty"`
	Thinking *string        `json:"thinking,omitempty"`
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    jsonRawMessage `json:"input,omitempty"`
}

type streamContentBlockDelta struct {
	Type  string      `json:"type"`
	Index int         `json:"index"`
	Delta streamDelta `json:"delta"`
}

// streamDelta carries either text_delta or input_json_delta.
type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type streamContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type streamMessageDelta struct {
	Type  string            `json:"type"`
	Delta streamMessageBody `json:"delta"`
	Usage streamDeltaUsage  `json:"usage"`
}

type streamMessageBody struct {
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type streamDeltaUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type streamMessageStop struct {
	Type string `json:"type"`
}

type streamPing struct {
	Type string `json:"type"`
}

type streamErrorEvent struct {
	Type  string         `json:"type"`
	Error AnthropicError `json:"error"`
}

// ---- The converter ----

// blockState tracks one in-progress content block so we can emit
// content_block_stop when it changes or ends.
type blockState struct {
	index int
	kind  string // "text" | "thinking"
}

// pendingChatTool buffers one upstream tool call's fragments until the
// stream ends. Chat Completions has no per-call done frame, so — like the
// Responses streamer — we emit the tool_use block whole at Finalize, once
// the id, name and full arguments are known. Streaming block-start
// immediately (the old design) produced nameless tool_use blocks when the
// name arrived late and silently dropped args-only deltas when no block was
// open; both shapes stall agent loops as text-only end_turn turns.
type pendingChatTool struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// StreamConverter maintains state while translating an OpenAI SSE stream into
// an Anthropic SSE stream.
type StreamConverter struct {
	w       *bufio.Writer
	model   string
	stopSeq *string

	cur              *blockState
	nextIdx          int
	input            int // input tokens (from final usage chunk, if upstream sends one)
	output           int // output tokens tally (from final usage chunk)
	cachedInput      int // cache-read input tokens (from final usage chunk)
	restrictTools    bool
	allowedNames     []string
	acceptedToolCall bool
	reasoning        strings.Builder
	toolIDs          []string

	// Buffered tool calls keyed by upstream index (see pendingChatTool).
	// Legacy function_call deltas use legacyFunctionCallIndex.
	pendingTools map[int]*pendingChatTool
	toolOrder    []int
	minToolKey   int
	// declaredSchemas carries raw input_schema per declared tool for
	// nameless-call recovery (see toolrecover.go); may be sparse.
	declaredSchemas map[string]jsonRawMessage
	// Diagnostics, mirroring ResponsesToAnthropicStreamer: renames,
	// recoveries and drops are logged by the server layer instead of
	// vanishing — invisible drops are how agent-loop stalls hide.
	renamedToolCalls int
	recoveredCalls   int
	droppedCalls     []droppedCall
	// textHead keeps the first bytes of assistant text (capped), so a
	// tool-less end_turn can be judged in the log: progress narration that
	// promises more work ("doing this:…") vs a legitimate turn end.
	textHead strings.Builder

	// OpenAI streams send finish_reason on the last content chunk and usage in
	// a trailing empty-choices chunk. We remember the finish reason and emit
	// message_delta/message_stop only when the stream ends, so usage is always
	// included.
	pendingFinish string
	finalized     bool
	// lastStop / lastUpstreamFinish record what Finalize emitted (Anthropic
	// stop reason) and what the upstream reported (raw finish_reason), so the
	// server layer can log the true outcome instead of a hardcoded guess.
	lastStop           string
	lastUpstreamFinish string
}

// legacyFunctionCallIndex keys buffered legacy function_call deltas.
// Upstream tool_calls indexes are always >= 0, so -1 never collides.
const legacyFunctionCallIndex = -1

// NewStreamConverter wraps w (the HTTP response body writer) and emits the
// leading message_start event immediately. model is echoed in events; stopSeq
// is forwarded as stop_sequence (nil = none).
func NewStreamConverter(w io.Writer, model string, stopSeq *string) (*StreamConverter, error) {
	c := &StreamConverter{
		w:            bufio.NewWriter(w),
		model:        model,
		stopSeq:      stopSeq,
		pendingTools: map[int]*pendingChatTool{},
		minToolKey:   legacyFunctionCallIndex,
	}
	if err := c.emitMessageStart(); err != nil {
		return nil, err
	}
	if err := c.emitPing(); err != nil {
		return nil, err
	}
	return c, nil
}

// RestrictTools limits emitted tool_use blocks to names declared by the
// incoming Anthropic request. Passing an empty slice rejects every upstream
// tool call, which prevents clients from trying to execute hallucinated tools.
// Upstream names resolve through ResolveDeclaredToolName first, so OpenCode-
// namespaced ("default.Bash") and free-tier marker ("shell"/"read") names are
// rewritten to the declared equivalent instead of being dropped mid-loop.
func (c *StreamConverter) RestrictTools(names []string) {
	c.restrictTools = true
	for _, name := range names {
		if name != "" {
			c.allowedNames = append(c.allowedNames, name)
		}
	}
}

// SetDeclaredSchemas records the client's tool input schemas so a call whose
// name never arrived on the wire can be attributed by its arguments
// (RecoverNamelessTool) instead of stalling the agent loop.
func (c *StreamConverter) SetDeclaredSchemas(tools []AnthropicTool) {
	if len(tools) == 0 {
		return
	}
	if c.declaredSchemas == nil {
		c.declaredSchemas = map[string]jsonRawMessage{}
	}
	for _, t := range tools {
		if t.Name != "" && len(t.InputSchema) > 0 {
			c.declaredSchemas[t.Name] = t.InputSchema
		}
	}
}

func (c *StreamConverter) emitMessageStart() error {
	payload := streamMessageStart{
		Type: "message_start",
		Message: streamMessage{
			ID:           "msg_" + randHex(24),
			Type:         "message",
			Role:         "assistant",
			Model:        c.model,
			StopReason:   nil, // null at message_start per spec; set later in message_delta
			StopSequence: c.stopSeq,
			Usage:        AnthropicUsage{InputTokens: 0, OutputTokens: 0},
			Content:      []any{},
		},
	}
	return c.writeEvent("message_start", payload)
}

func (c *StreamConverter) emitPing() error {
	return c.writeEvent("ping", streamPing{Type: "ping"})
}

// HandleChunk processes one decoded OpenAI streaming chunk and emits the
// corresponding Anthropic events. It must be called for every chunk in order.
func (c *StreamConverter) HandleChunk(chunk *OpenAIStreamChunk) error {
	if chunk == nil {
		return nil
	}

	// Usage typically arrives in a final empty-choices chunk.
	if chunk.Usage != nil {
		c.input = chunk.Usage.PromptTokens
		c.output = chunk.Usage.CompletionTokens
		c.cachedInput = chunk.Usage.CachedPromptTokens()
	}

	for _, ch := range chunk.Choices {
		// 1. reasoning_content is provider-required hidden thinking state.
		// Emit it as an Anthropic thinking block so Claude Code can replay it
		// on the next request without mixing it into user-visible text.
		if ch.Delta.ReasoningContent != "" {
			if err := c.handleThinking(ch.Delta.ReasoningContent); err != nil {
				return err
			}
		}
		// 2. Text delta — this is the model's actual reply.
		if ch.Delta.Content != "" {
			if err := c.handleText(ch.Delta.Content); err != nil {
				return err
			}
		}
		// 3. Tool call deltas — buffered per index and emitted whole at
		// Finalize (see pendingChatTool). Names are resolved there, so a
		// name arriving late (or never, for nameless recovery) can't
		// produce corrupt blocks or silent drops mid-stream.
		for _, tc := range ch.Delta.ToolCalls {
			c.bufferTool(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		// Legacy OpenAI-compatible providers may stream function_call instead
		// of tool_calls. Treat it as a single function tool call.
		if ch.Delta.FunctionCall != nil {
			c.bufferTool(legacyFunctionCallIndex, "", ch.Delta.FunctionCall.Name, ch.Delta.FunctionCall.Arguments)
		}
		// 4. Role-only first delta (no content) — nothing to emit.
		if ch.Delta.Role != "" && ch.Delta.Content == "" &&
			len(ch.Delta.ToolCalls) == 0 && ch.Delta.FunctionCall == nil {
			continue
		}
		// 5. Finish reason — remember it; we finalize at stream end so the
		// trailing usage chunk (if any) is captured. Ignore empty
		// finish_reason values so a non-terminal chunk can't clobber the
		// real terminal reason.
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			c.pendingFinish = *ch.FinishReason
		}
	}
	return nil
}

func (c *StreamConverter) handleThinking(text string) error {
	c.reasoning.WriteString(text)
	if c.cur == nil || c.cur.kind != "thinking" {
		if err := c.closeCurrent(); err != nil {
			return err
		}
		idx := c.nextIdx
		c.nextIdx++
		c.cur = &blockState{index: idx, kind: "thinking"}
		if err := c.writeEvent("content_block_start", streamContentBlockStart{
			Type:         "content_block_start",
			Index:        idx,
			ContentBlock: streamContentRef{Type: "thinking", Thinking: stringRef("")},
		}); err != nil {
			return err
		}
	}
	return c.writeEvent("content_block_delta", streamContentBlockDelta{
		Type:  "content_block_delta",
		Index: c.cur.index,
		Delta: streamDelta{Type: "thinking_delta", Thinking: text},
	})
}

// handleText emits text_delta events, opening a text block if needed.
func (c *StreamConverter) handleText(text string) error {
	if c.textHead.Len() < 400 {
		rest := 400 - c.textHead.Len()
		if len(text) > rest {
			c.textHead.WriteString(text[:rest])
		} else {
			c.textHead.WriteString(text)
		}
	}
	if c.cur == nil || c.cur.kind != "text" {
		if err := c.closeCurrent(); err != nil {
			return err
		}
		idx := c.nextIdx
		c.nextIdx++
		c.cur = &blockState{index: idx, kind: "text"}
		if err := c.writeEvent("content_block_start", streamContentBlockStart{
			Type:         "content_block_start",
			Index:        idx,
			ContentBlock: streamContentRef{Type: "text", Text: stringRef("")},
		}); err != nil {
			return err
		}
	}
	return c.writeEvent("content_block_delta", streamContentBlockDelta{
		Type:  "content_block_delta",
		Index: c.cur.index,
		Delta: streamDelta{Type: "text_delta", Text: text},
	})
}

func stringRef(s string) *string {
	return &s
}

// bufferTool accumulates one upstream tool-call delta under its index.
// Fragments for the same call share an index; a delta carrying a different
// non-empty id (with a name) under an already-claimed index is parked under
// a fresh key instead of merging two calls' arguments into corrupt JSON.
func (c *StreamConverter) bufferTool(index int, id, name, args string) {
	key := index
	if st, ok := c.pendingTools[key]; ok && id != "" && st.id != "" && st.id != id && name != "" {
		c.minToolKey--
		key = c.minToolKey
	}
	st := c.pendingTools[key]
	if st == nil {
		st = &pendingChatTool{index: key}
		c.pendingTools[key] = st
		c.toolOrder = append(c.toolOrder, key)
	}
	if id != "" && st.id == "" {
		st.id = id
	}
	if name != "" && st.name == "" {
		st.name = name
	}
	st.args.WriteString(args)
}

// emitBufferedTool resolves one buffered call and emits it whole
// (content_block_start + full input_json_delta + content_block_stop), or
// records a diagnosed drop. It mirrors the Responses streamer's flushTool:
// OpenCode-namespaced and free-tier marker names rewrite to the declared
// equivalent, nameless calls recover by schema, and true ambiguity drops
// with its reason instead of vanishing.
func (c *StreamConverter) emitBufferedTool(st *pendingChatTool) error {
	args := strings.TrimSpace(st.args.String())
	name := st.name
	if name != "" && c.restrictTools {
		resolved, ok := ResolveDeclaredToolName(name, c.allowedNames)
		if !ok {
			c.droppedCalls = append(c.droppedCalls, droppedCall{
				Name: name, CallID: st.id,
				ArgsLen: len(args), ArgsHead: cappedString(args, 160),
				Reason:   "undeclared-name",
				Declared: cappedDeclaredNames(c.allowedNames),
			})
			return nil
		}
		if resolved != name {
			c.renamedToolCalls++
		}
		name = resolved
	}
	if name == "" {
		recovered, reason := MatchNamelessTool(args, c.allowedNames, c.declaredSchemas)
		if recovered == "" {
			c.droppedCalls = append(c.droppedCalls, droppedCall{
				Name: "", CallID: st.id,
				ArgsLen: len(args), ArgsHead: cappedString(args, 160),
				Reason:   "nameless:" + reason,
				Declared: cappedDeclaredNames(c.allowedNames),
			})
			return nil
		}
		name = recovered
		c.recoveredCalls++
	}
	if name == "" {
		return nil
	}
	normalised := ensureToolID(st.id)
	idx := c.nextIdx
	c.nextIdx++
	c.toolIDs = append(c.toolIDs, normalised)
	c.acceptedToolCall = true
	if err := c.writeEvent("content_block_start", streamContentBlockStart{
		Type:  "content_block_start",
		Index: idx,
		ContentBlock: streamContentRef{
			Type:  "tool_use",
			ID:    normalised,
			Name:  name,
			Input: jsonRawMessage(`{}`),
		},
	}); err != nil {
		return err
	}
	if args != "" {
		if err := c.writeEvent("content_block_delta", streamContentBlockDelta{
			Type:  "content_block_delta",
			Index: idx,
			Delta: streamDelta{Type: "input_json_delta", PartialJSON: args},
		}); err != nil {
			return err
		}
	}
	return c.writeEvent("content_block_stop", streamContentBlockStop{
		Type:  "content_block_stop",
		Index: idx,
	})
}

// cappedString truncates s for diagnostics so tool payloads (possibly file
// contents) never flood the log.
func cappedString(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// closeCurrent emits content_block_stop for the active block, if any.
func (c *StreamConverter) closeCurrent() error {
	if c.cur == nil {
		return nil
	}
	err := c.writeEvent("content_block_stop", streamContentBlockStop{
		Type:  "content_block_stop",
		Index: c.cur.index,
	})
	c.cur = nil
	return err
}

// Finalize closes any open block and emits message_delta + message_stop. It
// must be called exactly once when the upstream stream ends (or errors out),
// so the trailing usage chunk — if the upstream sent one — is reflected in the
// message_delta usage. Idempotent.
func (c *StreamConverter) Finalize(stopReason string) error {
	if c.finalized {
		return nil
	}
	c.finalized = true
	if err := c.closeCurrent(); err != nil {
		return err
	}
	// Emit buffered tool calls whole, in first-seen order. Text (if any)
	// already closed above, so block indexes stay ordered.
	for _, key := range c.toolOrder {
		if err := c.emitBufferedTool(c.pendingTools[key]); err != nil {
			return err
		}
	}
	cacheReasoningForToolCalls(c.reasoning.String(), c.toolIDs...)
	// Prefer the finish_reason the upstream reported; fall back to the caller's
	// stopReason (e.g. "stream_error").
	reason := c.pendingFinish
	if reason == "" {
		reason = "stop"
	}
	if c.acceptedToolCall && reason == "stop" {
		reason = "tool_calls"
	}
	if (reason == "tool_calls" || reason == "function_call") && !c.acceptedToolCall {
		// Upstream promised tool calls but every one was dropped: report
		// end_turn (the client has nothing to execute) — but the drops are
		// recorded in DroppedCalls for the server log, never silent.
		reason = "stop"
	}
	stop := mapFinishReason(reason)
	c.lastStop = *stop
	c.lastUpstreamFinish = c.pendingFinish
	payload := streamMessageDelta{
		Type:  "message_delta",
		Delta: streamMessageBody{StopReason: stop, StopSequence: c.stopSeq},
		// Usage is emitted unconditionally (output_tokens may be 0 if the
		// upstream never reported usage). Claude Code relies on its presence.
		Usage: streamDeltaUsage{
			InputTokens:              c.input,
			CacheCreationInputTokens: 0,
			CacheReadInputTokens:     c.cachedInput,
			OutputTokens:             c.output,
		},
	}
	if err := c.writeEvent("message_delta", payload); err != nil {
		return err
	}
	return c.writeEvent("message_stop", streamMessageStop{Type: "message_stop"})
}

// StopReason reports the Anthropic stop reason emitted in message_delta
// ("" before Finalize). The server layer must log this instead of a
// hardcoded guess, or stall forensics goes blind.
func (c *StreamConverter) StopReason() string { return c.lastStop }

// HasToolUse reports whether any tool_use block has been emitted — or is
// buffered for emission at Finalize — so far. The buffered half matters:
// tool deltas accumulate pre-Finalize by design, and the toolless-resample
// verdict must see a rescued sample's calls before Finalize runs.
// (A buffered call can still drop at emit time for undeclared/unrecoverable
// names; Finalize then reports end_turn and the drop is logged with its
// reason, so the optimistic true here never hides a loss.)
func (c *StreamConverter) HasToolUse() bool { return c.acceptedToolCall || len(c.toolOrder) > 0 }

// HasBufferedTools reports whether tool-call fragments are buffered for
// emission at Finalize (whether or not they will survive name resolution).
// The error-path resample guard uses it to avoid appending a second sample
// onto partial fragments (which could duplicate or corrupt calls).
func (c *StreamConverter) HasBufferedTools() bool { return len(c.toolOrder) > 0 }

// PendingFinish returns the raw upstream finish_reason observed so far
// ("" when none arrived, e.g. an empty upstream stream).
func (c *StreamConverter) PendingFinish() string { return c.pendingFinish }

// UpstreamFinish returns the raw upstream finish_reason observed on the
// stream ("" when none arrived). A "content_filter" here with an end_turn
// stop downstream means the turn was filtered, not naturally ended.
func (c *StreamConverter) UpstreamFinish() string { return c.lastUpstreamFinish }

// RenamedToolCalls counts upstream function calls rewritten to a declared
// tool name; RecoveredCalls counts nameless calls attributed by schema;
// DroppedCalls lists calls dropped as undeclared or unrecoverable, for
// diagnostics logging by the server layer.
func (c *StreamConverter) RenamedToolCalls() int { return c.renamedToolCalls }
func (c *StreamConverter) RecoveredCalls() int   { return c.recoveredCalls }
func (c *StreamConverter) DroppedCalls() []droppedCall {
	return append([]droppedCall(nil), c.droppedCalls...)
}

// TextHead returns the first ~400 bytes of assistant text in the stream,
// for judging tool-less end_turn responses in the server log.
func (c *StreamConverter) TextHead() string { return c.textHead.String() }

// mapFinishReason mirrors response.go but returns a pointer.
func mapFinishReason(reason string) *string {
	var s string
	switch reason {
	case "stop":
		s = "end_turn"
	case "length":
		s = "max_tokens"
	case "tool_calls", "function_call":
		s = "tool_use"
	default:
		s = "end_turn"
	}
	return &s
}

// EmitError writes an error event. Used when the upstream stream errors out.
func (c *StreamConverter) EmitError(typ, msg string) error {
	return c.writeEvent("error", streamErrorEvent{
		Type:  "error",
		Error: AnthropicError{Type: typ, Message: msg},
	})
}

// Flush flushes the underlying buffered writer.
func (c *StreamConverter) Flush() error { return c.w.Flush() }

// writeEvent marshals payload and writes the SSE framing for one event.
func (c *StreamConverter) writeEvent(event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return c.w.Flush()
}

// ---- Upstream SSE parser ----

type OpenAIStreamScanResult struct {
	SawChunk  bool
	SawDone   bool
	SawFinish bool
	// Malformed counts data lines that failed to parse and were skipped.
	// A malformed tool-call chunk produces exactly the stall shape
	// (finish tool_calls, no tool_use, no drops), so the count is logged
	// on tool-less turns instead of vanishing.
	Malformed int
}

// ScanOpenAIStream reads an OpenAI SSE stream from r and invokes onChunk for
// each decoded chunk. It returns io.EOF when the stream terminates cleanly.
// Some OpenAI-compatible upstreams end the HTTP stream without a final
// "data: [DONE]"; if at least one valid chunk was seen, that is accepted as a
// clean EOF.
func ScanOpenAIStream(r io.Reader, onChunk func(*OpenAIStreamChunk) error) error {
	_, err := ScanOpenAIStreamWithStatus(r, onChunk)
	return err
}

func ScanOpenAIStreamWithStatus(r io.Reader, onChunk func(*OpenAIStreamChunk) error) (OpenAIStreamScanResult, error) {
	sc := bufio.NewScanner(r)
	// Some upstreams send very large chunks; bump the buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var result OpenAIStreamScanResult
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			// ignore event:/id:/comments
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			result.SawDone = true
			break
		}
		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip malformed line rather than killing the whole stream.
			// Counted, not silent: see Malformed.
			result.Malformed++
			continue
		}
		result.SawChunk = true
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				result.SawFinish = true
			}
		}
		if err := onChunk(&chunk); err != nil {
			return result, err
		}
	}
	if err := sc.Err(); err != nil {
		return result, err
	}
	if !result.SawDone && !result.SawChunk {
		return result, errors.New("upstream stream ended without [DONE]")
	}
	return result, io.EOF
}
