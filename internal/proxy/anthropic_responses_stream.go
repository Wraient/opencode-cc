package proxy

// ResponsesToAnthropicStreamer converts an upstream Responses API SSE stream
// into Anthropic Messages SSE events. Function calls are buffered and emitted
// whole at output_item.done (start + full input_json_delta + stop) so a
// missing output_item.added can never produce a nameless tool_use block;
// text deltas stream live.

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type pendingResponsesTool struct {
	itemID  string
	callID  string
	name    string
	args    strings.Builder
	emitted bool
	// sawAdded/sawDone track which upstream frames referenced this item, so
	// a dropped call can be diagnosed (nameless calls mean the name never
	// arrived in any frame, not that it was undeclared).
	sawAdded bool
	sawDone  bool
	// addedType records the item type seen on output_item.added even when it
	// is not "function_call" (e.g. an MCP/custom tool shape carrying the
	// name elsewhere) — the name may still arrive via deltas/done.
	addedType string
	// notool marks items whose added frame was NOT a function call
	// (e.g. "reasoning" output items): they are never tool candidates and
	// must not be flushed or logged as dropped calls. Cleared if a later
	// done frame identifies the item as a function call.
	notool bool
	// firstFrame keeps the raw JSON of the first upstream frame that
	// referenced this item (truncated), so a drop log shows exactly what
	// shape Zen sent — e.g. whether the name hides in an unparsed field.
	firstFrame string
}

// droppedCall describes an upstream function call dropped as undeclared,
// with enough shape to diagnose WHY the name was missing.
type droppedCall struct {
	ItemID     string
	Name       string
	CallID     string
	ArgsLen    int
	ArgsHead   string
	AddedType  string
	SawAdded   bool
	SawDone    bool
	FirstFrame string
	// Reason is the machine-readable drop cause: "undeclared-name",
	// "nameless:<recovery-failure-reason>" (see NamelessFailureReason), or
	// "" for legacy callers.
	Reason string
	// Declared lists the client-declared tool names the call was matched
	// against (capped), so a drop can be judged without replaying the
	// request: empty means recovery never had a chance.
	Declared string
}

// ResponsesToAnthropicStreamer maintains streaming state. Create with
// NewResponsesToAnthropicStreamer, feed upstream SSE frames with HandleEvent,
// then Finalize. Flushes are the caller's job after each event.
type ResponsesToAnthropicStreamer struct {
	w     *bufio.Writer
	model string

	allowed      map[string]struct{} // nil = allow all tool names
	allowedNames []string
	// declaredSchemas carries raw input_schema per declared tool for
	// nameless-call recovery (see toolrecover.go); may be sparse.
	declaredSchemas map[string]jsonRawMessage
	recoveredCalls  int

	// upstream tool calls that were rewritten to a declared name (marker /
	// namespaced) or dropped (undeclared), for diagnostics logging by the
	// server layer — invisible drops are how agent-loop stalls hide
	renamedToolCalls int
	droppedCalls     []droppedCall
	// badFrames keeps raw upstream frames that failed to parse (capped), so
	// the server log can show the exact shape that lost a tool name.
	badFrames []string
	// textHead keeps the first bytes of assistant text (capped), so a
	// tool-less end_turn can be judged in the log: progress narration that
	// promises more work ("writing them…") vs a legitimate turn end.
	textHead strings.Builder

	msgStarted bool
	msgID      string
	textOpen   bool
	nextIndex  int
	// textLen counts assistant text bytes delivered as live deltas.
	// The done-frame fallback (output_text.done) only fires while this is
	// zero, so a complete text arriving solely in done frames still lands
	// instead of collapsing to an empty end_turn — without ever doubling
	// text that deltas already delivered.
	textLen int

	pending map[string]*pendingResponsesTool
	order   []string // item_ids in first-seen order (stable indexing)

	hasToolUse bool
	incomplete bool
	usage      AnthropicUsage
	failed     string
	finalized  bool
	// frameTypes counts upstream SSE frame types seen on this stream
	// (by embedded type, falling back to event name). Logged on
	// suspicious turns (tool-less end_turn) so a shape the parser
	// doesn't understand shows up as a census, not silence.
	frameTypes map[string]int
	// itemTypes counts output-item types on added/done frames
	// ("added:function_call", "done:(no-item)", ...). A function call
	// that vanishes without a drop log always leaves a trace here:
	// e.g. added:message/done:message with no function_call anywhere
	// means upstream genuinely sent text-only; a function_call that
	// never reaches flushTool means the loss is in this converter.
	itemTypes map[string]int
	// completedStatus records the upstream terminal status
	// (response.completed's status: completed/incomplete/failed...).
	completedStatus string
}

// NewResponsesToAnthropicStreamer wraps w and notes the model to echo in
// message_start. Call RestrictTools to drop undeclared function names.
func NewResponsesToAnthropicStreamer(w *bufio.Writer, model string) *ResponsesToAnthropicStreamer {
	return &ResponsesToAnthropicStreamer{
		w:          w,
		model:      model,
		msgID:      "msg_" + randHex(24),
		pending:    map[string]*pendingResponsesTool{},
		frameTypes: map[string]int{},
		itemTypes:  map[string]int{},
	}
}

// RestrictTools limits emitted tool_use blocks to names declared by the
// incoming request. Upstream names are resolved through
// ResolveDeclaredToolName first, so marker tools the proxy itself injected
// ("shell"/"read") and OpenCode-namespaced names ("default.Bash") are
// rewritten to the declared equivalent instead of being dropped.
func (c *ResponsesToAnthropicStreamer) RestrictTools(names []string) {
	if names == nil {
		return
	}
	c.allowed = map[string]struct{}{}
	c.allowedNames = make([]string, 0, len(names))
	for _, n := range names {
		c.allowed[n] = struct{}{}
		if n != "" {
			c.allowedNames = append(c.allowedNames, n)
		}
	}
}

// SetDeclaredSchemas records the client's tool input schemas so a function
// call whose name never arrived on the wire can be attributed by its
// arguments (RecoverNamelessTool) instead of stalling the agent loop.
func (c *ResponsesToAnthropicStreamer) SetDeclaredSchemas(tools []AnthropicTool) {
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

// RecoveredCalls counts nameless upstream calls attributed by schema.
func (c *ResponsesToAnthropicStreamer) RecoveredCalls() int { return c.recoveredCalls }

func (c *ResponsesToAnthropicStreamer) pendingFor(itemID string) *pendingResponsesTool {
	p, ok := c.pending[itemID]
	if !ok {
		p = &pendingResponsesTool{itemID: itemID}
		c.pending[itemID] = p
		c.order = append(c.order, itemID)
	}
	return p
}

// HandleEvent processes one upstream SSE frame (event name without the
// "event:" prefix, raw data payload). "ping" frames are skipped.
func (c *ResponsesToAnthropicStreamer) HandleEvent(event string, data []byte) error {
	if event == "ping" {
		return nil
	}
	var payload responsesStreamFrame
	if err := jsonUnmarshal(data, &payload); err != nil {
		// Tolerate unknown/heartbeat frames, but keep the raw bytes
		// (capped): a frame carrying a tool name in an unexpected shape
		// parses as nothing today and loses the call downstream.
		if len(c.badFrames) < 8 {
			s := string(data)
			if len(s) > 300 {
				s = s[:300] + "…"
			}
			c.badFrames = append(c.badFrames, event+" | "+s)
		}
		return nil
	}
	// Prefer the embedded type; fall back to the SSE event name so a frame
	// carrying only one of the two still routes correctly.
	typ := payload.Type
	if typ == "" {
		typ = event
	}
	c.frameTypes[typ]++
	switch typ {
	case "response.output_text.delta":
		if err := c.ensureTextBlock(); err != nil {
			return err
		}
		if c.textHead.Len() < 400 {
			rest := 400 - c.textHead.Len()
			if len(payload.Delta) > rest {
				c.textHead.WriteString(payload.Delta[:rest])
			} else {
				c.textHead.WriteString(payload.Delta)
			}
		}
		c.textLen += len(payload.Delta)
		return c.writeEvent("content_block_delta", streamContentBlockDelta{
			Type: "content_block_delta", Index: c.nextIndex - 1,
			Delta: streamDelta{Type: "text_delta", Text: payload.Delta},
		})
	case "response.output_text.done":
		// The done frame carries the COMPLETE text. Deltas normally deliver
		// it first, but when a turn arrives with empty/missing deltas the
		// done text is the only copy — feed it if nothing arrived yet, so
		// the turn doesn't collapse to an empty end_turn. Never append when
		// deltas already delivered content (that would double the text).
		if payload.Text != "" && c.textLen == 0 {
			if err := c.ensureTextBlock(); err != nil {
				return err
			}
			c.textLen += len(payload.Text)
			if c.textHead.Len() < 400 {
				rest := 400 - c.textHead.Len()
				if len(payload.Text) > rest {
					c.textHead.WriteString(payload.Text[:rest])
				} else {
					c.textHead.WriteString(payload.Text)
				}
			}
			if err := c.writeEvent("content_block_delta", streamContentBlockDelta{
				Type: "content_block_delta", Index: c.nextIndex - 1,
				Delta: streamDelta{Type: "text_delta", Text: payload.Text},
			}); err != nil {
				return err
			}
		}
		return c.closeTextBlock()
	case "response.function_call_arguments.delta":
		p := c.pendingFor(payload.ItemID)
		p.snapFrame(data)
		// Top-level fallbacks (Zen flavor): name/call identity may ride
		// on the frame itself rather than a preceding added frame.
		if payload.Name != "" && p.name == "" {
			p.name = payload.Name
		}
		if payload.CallID != "" && p.callID == "" {
			p.callID = payload.CallID
		}
		p.args.WriteString(payload.Delta)
		return nil
	case "response.output_item.added":
		if payload.Item == nil {
			c.itemTypes["added:(no-item)"]++
			return nil
		}
		c.itemTypes["added:"+orNoType(payload.Item.Type)]++
		p := c.pendingFor(payload.ItemID)
		p.snapFrame(data)
		p.sawAdded = true
		p.addedType = payload.Item.Type
		if payload.Item.Type != "function_call" {
			// Reasoning/MCP/custom items are never tool candidates; skip
			// them at flush so they are not logged as dropped calls.
			p.notool = true
			return nil
		}
		if payload.Item.CallID != "" {
			p.callID = payload.Item.CallID
		}
		if payload.Item.Name != "" {
			p.name = payload.Item.Name
		}
		if payload.Item.Arguments != "" {
			p.args.WriteString(payload.Item.Arguments)
		}
		return nil
	case "response.function_call_arguments.done", "response.output_item.done":
		// Always track the item (even when the frame carries no usable
		// fields): a done for an unknown item is itself suspicious and must
		// be recorded at flush, never silently ignored.
		p := c.pendingFor(payload.ItemID)
		p.snapFrame(data)
		name, callID, args := payload.Name, payload.CallID, payload.Arguments
		isFunc := true
		if payload.Item != nil {
			c.itemTypes["done:"+orNoType(payload.Item.Type)]++
			if payload.Item.Type != "" && payload.Item.Type != "function_call" {
				isFunc = false
			}
			if payload.Item.CallID != "" {
				callID = payload.Item.CallID
			}
			if payload.Item.Name != "" {
				name = payload.Item.Name
			}
			if payload.Item.Arguments != "" {
				args = payload.Item.Arguments
			}
		} else {
			c.itemTypes["done:(no-item)"]++
		}
		if isFunc {
			p.sawDone = true
			// Only clear the non-tool marker on positive function-call
			// evidence. An item-less done frame (no name/call/args, nothing
			// accumulated) must NOT resurrect a reasoning/message item into
			// a tool candidate — that shape previously surfaced as phantom
			// "dropped upstream tool call" lines for reasoning items.
			if payload.Item != nil && payload.Item.Type == "function_call" ||
				name != "" || callID != "" || args != "" || strings.TrimSpace(p.args.String()) != "" {
				p.notool = false
			}
			if callID != "" {
				p.callID = callID
			}
			if name != "" {
				p.name = name
			}
			if args != "" {
				// done carries the COMPLETE arguments (per the Responses
				// API), while deltas streamed the same content as fragments
				// — replace, never append, or the tool receives doubled
				// JSON ("{...}{...}") that clients cannot parse.
				p.args.Reset()
				p.args.WriteString(args)
			}
		}
		return c.flushTool(payload.ItemID)
	case "response.completed":
		if payload.Response != nil {
			c.applyUsage(payload.Response.Usage)
			c.completedStatus = payload.Response.Status
			if payload.Response.Status == "incomplete" {
				c.incomplete = true
			}
		}
		// Deliberately no finalize here: the server calls Finalize() after
		// the scan, which also gives the toolless-resample guard a chance
		// to append a rescued sample into the still-open message before
		// message_delta/message_stop go out. Finalizing here would strand
		// any rescue behind an already-sent message_stop.
		return nil
	case "response.incomplete":
		c.incomplete = true
		if payload.Response != nil {
			c.applyUsage(payload.Response.Usage)
		}
		return nil
	case "response.failed":
		msg := "upstream response failed"
		if payload.Response != nil && payload.Response.Error != "" {
			msg = payload.Response.Error
		}
		if payload.Error != "" {
			msg = payload.Error
		}
		c.failed = msg
		return nil
	default:
		// response.created, response.in_progress, content_part.*,
		// reasoning summaries, output_text.done handled above: ignore.
		return nil
	}
}

// EmitMessageStart writes message_start immediately, before any content.
// Call right after the upstream connection is established so the client sees
// activity during the upstream reasoning phase instead of a silent stall
// (Claude Code cancels streams that stay quiet too long). Idempotent: later
// content paths call ensureMsgStarted anyway.
func (c *ResponsesToAnthropicStreamer) EmitMessageStart() error {
	return c.ensureMsgStarted()
}

func (c *ResponsesToAnthropicStreamer) ensureMsgStarted() error {
	if c.msgStarted {
		return nil
	}
	c.msgStarted = true
	return c.writeEvent("message_start", streamMessageStart{
		Type: "message_start",
		Message: streamMessage{
			ID: c.msgID, Type: "message", Role: "assistant",
			Model: c.model, Usage: AnthropicUsage{}, Content: []any{},
		},
	})
}

func (c *ResponsesToAnthropicStreamer) ensureTextBlock() error {
	if err := c.ensureMsgStarted(); err != nil {
		return err
	}
	if c.textOpen {
		return nil
	}
	if err := c.closeTextBlock(); err != nil {
		return err
	}
	c.textOpen = true
	// Claim the index at open (like flushTool): deltas and the later stop
	// both address nextIndex-1 while this block is open. Incrementing only
	// at close made every text delta emit index -1, which strict clients
	// (Claude Code SDK) reject by aborting the whole stream.
	idx := c.nextIndex
	c.nextIndex++
	empty := ""
	return c.writeEvent("content_block_start", streamContentBlockStart{
		Type:  "content_block_start",
		Index: idx,
		ContentBlock: streamContentRef{
			Type: "text", Text: &empty,
		},
	})
}

func (c *ResponsesToAnthropicStreamer) closeTextBlock() error {
	if !c.textOpen {
		return nil
	}
	c.textOpen = false
	return c.writeEvent("content_block_stop", streamContentBlockStop{
		Type: "content_block_stop", Index: c.nextIndex - 1,
	})
}

// flushTool emits a buffered function call whole, unless already emitted or
// restricted. Unopened text is closed first so block indexes stay ordered.
func (c *ResponsesToAnthropicStreamer) flushTool(itemID string) error {
	p, ok := c.pending[itemID]
	if !ok || p.emitted {
		return nil
	}
	p.emitted = true
	if p.notool {
		// A non-function output item (e.g. a reasoning summary): never a
		// tool candidate — unless it accumulated call arguments, which
		// means a real call is hiding behind a non-function item type.
		// That shape used to vanish without a trace; record it.
		if strings.TrimSpace(p.args.String()) != "" {
			c.droppedCalls = append(c.droppedCalls, p.describeDrop())
		}
		return nil
	}
	if p.name == "" && strings.TrimSpace(p.args.String()) == "" && !p.sawAdded && p.callID == "" {
		// A bare done marker for an item that never carried anything:
		// nothing was lost, stay silent.
		return nil
	}
	emitName := p.name
	if emitName == "" {
		// The name never arrived on the wire (Zen streams bare
		// function_call_arguments.delta frames with no added/done). Try to
		// attribute the call by its arguments before giving up.
		args := strings.TrimSpace(p.args.String())
		if recovered, reason := MatchNamelessTool(args, c.allowedNames, c.declaredSchemas); recovered != "" {
			emitName = recovered
			c.recoveredCalls++
		} else {
			drop := p.describeDrop()
			drop.Reason = "nameless:" + reason
			drop.Declared = cappedDeclaredNames(c.allowedNames)
			c.droppedCalls = append(c.droppedCalls, drop)
			return nil
		}
	} else if c.allowed != nil {
		resolved, ok := ResolveDeclaredToolName(p.name, c.allowedNames)
		if !ok {
			// Undeclared (e.g. a hallucinated tool): drop, but record the
			// full shape so the server layer can log WHY.
			drop := p.describeDrop()
			drop.Reason = "undeclared-name"
			drop.Declared = cappedDeclaredNames(c.allowedNames)
			c.droppedCalls = append(c.droppedCalls, drop)
			return nil
		}
		if resolved != p.name {
			c.renamedToolCalls++
		}
		emitName = resolved
	}
	if emitName == "" {
		return nil
	}
	if err := c.ensureMsgStarted(); err != nil {
		return err
	}
	if err := c.closeTextBlock(); err != nil {
		return err
	}
	callID := p.callID
	if callID == "" {
		callID = "call_" + randHex(24)
	}
	args := strings.TrimSpace(p.args.String())
	if args == "" {
		args = "{}"
	}
	idx := c.nextIndex
	c.nextIndex++
	if err := c.writeEvent("content_block_start", streamContentBlockStart{
		Type:  "content_block_start",
		Index: idx,
		ContentBlock: streamContentRef{
			Type: "tool_use", ID: callID, Name: emitName,
			Input: jsonRawMessage("{}"),
		},
	}); err != nil {
		return err
	}
	if err := c.writeEvent("content_block_delta", streamContentBlockDelta{
		Type: "content_block_delta", Index: idx,
		Delta: streamDelta{Type: "input_json_delta", PartialJSON: args},
	}); err != nil {
		return err
	}
	c.hasToolUse = true
	return c.writeEvent("content_block_stop", streamContentBlockStop{
		Type: "content_block_stop", Index: idx,
	})
}

func (c *ResponsesToAnthropicStreamer) applyUsage(u *responsesStreamUsage) {
	if u == nil {
		return
	}
	c.usage.InputTokens = u.InputTokens
	c.usage.OutputTokens = u.OutputTokens
	c.usage.CacheReadInputTokens = u.EffectiveCachedTokens()
}

// Finalize emits message_delta/message_stop. Call once after the upstream
// stream ends; safe to call twice.
func (c *ResponsesToAnthropicStreamer) Finalize() error {
	return c.finalizeWith("stream_end")
}

func (c *ResponsesToAnthropicStreamer) finalizeWith(reason string) error {
	if c.finalized {
		return nil
	}
	c.finalized = true
	if c.failed != "" {
		return c.writeEvent("error", streamErrorEvent{
			Type:  "error",
			Error: AnthropicError{Type: "api_error", Message: c.failed},
		})
	}
	if err := c.ensureMsgStarted(); err != nil {
		return err
	}
	for _, itemID := range c.order {
		if err := c.flushTool(itemID); err != nil {
			return err
		}
	}
	if err := c.closeTextBlock(); err != nil {
		return err
	}
	stop := "end_turn"
	switch {
	case c.hasToolUse:
		stop = "tool_use"
	case c.incomplete:
		stop = "max_tokens"
	case reason == "upstream_error":
		stop = "stream_error"
	}
	if err := c.writeEvent("message_delta", streamMessageDelta{
		Type:  "message_delta",
		Delta: streamMessageBody{StopReason: &stop},
		Usage: streamDeltaUsage{
			InputTokens:              c.usage.InputTokens,
			CacheCreationInputTokens: c.usage.CacheCreationInputTokens,
			CacheReadInputTokens:     c.usage.CacheReadInputTokens,
			OutputTokens:             c.usage.OutputTokens,
		},
	}); err != nil {
		return err
	}
	return c.writeEvent("message_stop", streamMessageStop{Type: "message_stop"})
}

// EmitError writes an Anthropic error event (mid-stream failure surface).
func (c *ResponsesToAnthropicStreamer) EmitError(typ, msg string) error {
	c.finalized = true
	return c.writeEvent("error", streamErrorEvent{
		Type:  "error",
		Error: AnthropicError{Type: typ, Message: msg},
	})
}

// InputTokens, OutputTokens and CachedTokens report the usage observed on
// the upstream stream (zeros when the run never reached response.completed).
func (c *ResponsesToAnthropicStreamer) InputTokens() int  { return c.usage.InputTokens }
func (c *ResponsesToAnthropicStreamer) OutputTokens() int { return c.usage.OutputTokens }
func (c *ResponsesToAnthropicStreamer) CachedTokens() int { return c.usage.CacheReadInputTokens }

// snapFrame records the raw JSON of the first upstream frame referencing an
// item (truncated),so drop logs show exactly what shape Zen sent. Callers
// pass the frame's raw data payload.
func (p *pendingResponsesTool) snapFrame(data []byte) {
	if p.firstFrame != "" {
		return
	}
	s := string(data)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	p.firstFrame = s
}

// describeDrop snapshots a dropped call for diagnostics. ArgsHead is capped
// so tool payloads (possibly file contents) never flood the log.
func (p *pendingResponsesTool) describeDrop() droppedCall {
	args := p.args.String()
	head := args
	if len(head) > 160 {
		head = head[:160] + "…"
	}
	return droppedCall{
		ItemID: p.itemID, Name: p.name, CallID: p.callID,
		ArgsLen: len(args), ArgsHead: head,
		AddedType: p.addedType, SawAdded: p.sawAdded, SawDone: p.sawDone,
		FirstFrame: p.firstFrame,
	}
}

// cappedDeclaredNames joins declared tool names for drop-log diagnostics,
// capped so a 40-tool client doesn't flood the log.
func cappedDeclaredNames(names []string) string {
	joined := strings.Join(names, ",")
	if len(joined) > 300 {
		joined = joined[:300] + "…"
	}
	return joined
}

// RenamedToolCalls counts upstream function calls rewritten to a declared
// tool name; DroppedCalls lists upstream function calls dropped as
// undeclared or nameless, for diagnostics logging by the server layer.
func (c *ResponsesToAnthropicStreamer) RenamedToolCalls() int { return c.renamedToolCalls }
func (c *ResponsesToAnthropicStreamer) DroppedCalls() []droppedCall {
	return append([]droppedCall(nil), c.droppedCalls...)
}

// BadFrames returns raw upstream frames that failed to parse (capped at 8
// per stream), for diagnostics logging by the server layer.
func (c *ResponsesToAnthropicStreamer) BadFrames() []string {
	return append([]string(nil), c.badFrames...)
}

// TextHead returns the first ~400 bytes of assistant text in the stream,
// for judging tool-less end_turn responses in the server log.
func (c *ResponsesToAnthropicStreamer) TextHead() string { return c.textHead.String() }

// EmptyText reports whether no assistant text arrived on the stream.
func (c *ResponsesToAnthropicStreamer) EmptyText() bool { return c.textLen == 0 }

// FrameCensus summarizes upstream SSE frame types seen on this stream as
// "type:count,..." (sorted), followed by output-item types
// ("items:added:function_call:1,...") and the terminal status
// ("status:completed"). Logged on suspicious turns so an upstream shape the
// parser doesn't understand is diagnosable without dumping content.
func (c *ResponsesToAnthropicStreamer) FrameCensus() string {
	if len(c.frameTypes) == 0 {
		return "(no frames)"
	}
	types := make([]string, 0, len(c.frameTypes))
	for typ := range c.frameTypes {
		types = append(types, typ)
	}
	sort.Strings(types)
	var sb strings.Builder
	for i, typ := range types {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(typ)
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(c.frameTypes[typ]))
	}
	items := make([]string, 0, len(c.itemTypes))
	for typ := range c.itemTypes {
		items = append(items, typ)
	}
	sort.Strings(items)
	for _, typ := range items {
		sb.WriteString(",items:")
		sb.WriteString(typ)
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(c.itemTypes[typ]))
	}
	if c.completedStatus != "" {
		sb.WriteString(",status:")
		sb.WriteString(c.completedStatus)
	}
	return sb.String()
}

// ToollessResampleMaxOutputTokens caps the resample guard: a completed,
// tool-less turn at or below this size is cheap to resample and matches the
// observed weak-model flake shape (narration instead of a tool call). Above
// it, the text is treated as a deliberate answer and relayed untouched, so
// long legitimate responses are never discarded or doubled.
const ToollessResampleMaxOutputTokens = 500

// ShouldResampleToolless decides whether a completed bridge turn is worth
// one identical upstream resample before relaying. All conditions must hold:
// feature enabled, tools declared, upstream terminal status completed,
// downstream stop end_turn with no tool_use emitted and nothing dropped,
// output within the resample cap. The returned string names the verdict for
// logging ("resample" or the blocking reason).
func ShouldResampleToolless(enabled bool, toolsDeclared int, stopReason string, hasToolUse bool, dropped int, outputTokens int, upstreamStatus string) (bool, string) {
	switch {
	case !enabled:
		return false, "disabled"
	case toolsDeclared == 0:
		return false, "no-tools-declared"
	case hasToolUse || stopReason != "end_turn":
		return false, "not-toolless"
	case dropped != 0:
		return false, "has-drops"
	case upstreamStatus != "" && upstreamStatus != "completed":
		return false, "upstream-status:" + upstreamStatus
	case outputTokens > ToollessResampleMaxOutputTokens:
		return false, "output-too-large"
	default:
		return true, "resample"
	}
}

// orNoType labels an empty item type for census purposes.
func orNoType(t string) string {
	if t == "" {
		return "(no-type)"
	}
	return t
}

// StopReason reports the stop reason emitted in message_delta.
func (c *ResponsesToAnthropicStreamer) StopReason() string {
	switch {
	case c.hasToolUse:
		return "tool_use"
	case c.incomplete:
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// HasToolUse reports whether any tool_use block has been emitted so far.
// Unlike StopReason it is meaningful before Finalize, so the server layer
// can decide on a resample while the message is still open.
func (c *ResponsesToAnthropicStreamer) HasToolUse() bool { return c.hasToolUse }

// HasPendingTools reports whether any buffered call could still become a
// tool_use at Finalize. Delta-only calls without done frames flush only in
// finalizeWith, so a pre-Finalize verdict on HasToolUse alone would
// resample over calls that were about to emit — doubling them. Entries
// that flushTool would skip (emitted, non-tool without args, bare done
// markers) don't count.
func (c *ResponsesToAnthropicStreamer) HasPendingTools() bool {
	for _, p := range c.pending {
		if p.emitted {
			continue
		}
		if p.notool && strings.TrimSpace(p.args.String()) == "" {
			continue
		}
		if p.name == "" && strings.TrimSpace(p.args.String()) == "" && !p.sawAdded && p.callID == "" {
			continue
		}
		return true
	}
	return false
}

// CompletedStatus returns the upstream terminal status observed so far
// ("completed", "incomplete", ...; "" when the stream hasn't ended one).
func (c *ResponsesToAnthropicStreamer) CompletedStatus() string { return c.completedStatus }

// Failed reports the upstream failure message recorded so far, if any.
func (c *ResponsesToAnthropicStreamer) Failed() string { return c.failed }

// ClearFailed discards a recorded upstream failure, restoring the
// pre-failure state. Used by the toolless-resample guard so a failed
// speculative sample can never convert the good first attempt into an
// error event at Finalize.
func (c *ResponsesToAnthropicStreamer) ClearFailed() { c.failed = "" }

// Incomplete reports whether an incomplete status was observed.
func (c *ResponsesToAnthropicStreamer) Incomplete() bool { return c.incomplete }

// SetIncomplete restores the incomplete flag (resample guard counterpart).
func (c *ResponsesToAnthropicStreamer) SetIncomplete(v bool) { c.incomplete = v }

func (c *ResponsesToAnthropicStreamer) writeEvent(event string, payload any) error {
	b, err := jsonMarshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return nil
}

// ---- upstream frame shapes (tolerant: unknown fields ignored) ----

type responsesStreamFrame struct {
	Type     string                  `json:"type"`
	ItemID   string                  `json:"item_id"`
	Delta    string                  `json:"delta"`
	Item     *responsesStreamItem    `json:"item"`
	Response *responsesStreamOutcome `json:"response"`
	Error    string                  `json:"error"`
	// Text carries the complete text on terminal frames such as
	// response.output_text.done. It is the fallback source when deltas
	// arrived empty or not at all.
	Text string `json:"text"`
	// Zen's flavor may also carry call identity at the frame top level
	// instead of (or in addition to) nesting it under item. These are
	// fallbacks: item fields win when both are present.
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
}

type responsesStreamItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesStreamOutcome struct {
	Status string                `json:"status"`
	Error  string                `json:"error"`
	Usage  *responsesStreamUsage `json:"usage"`
}

type responsesStreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CachedTokens int `json:"cached_tokens"`
	Details      *struct {
		Cached int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u *responsesStreamUsage) EffectiveCachedTokens() int {
	if u == nil {
		return 0
	}
	if u.Details != nil && u.Details.Cached > 0 {
		return u.Details.Cached
	}
	return u.CachedTokens
}
