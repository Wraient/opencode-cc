package server

// Anthropic-over-Responses bridge for Responses-native models.
//
// Meta muse-spark* models are served ONLY on the upstream Responses API, so
// POST /v1/messages for them is translated to upstream /v1/responses here
// (Anthropic request -> Responses request, convert back on the way out)
// instead of failing fast. Shares the museSparkUpstreamSlots semaphore with
// the Responses passthrough path: same upstream pool, same free-tier
// concurrency cap.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Kiowx/opencode-cc/internal/proxy"
)

// bridgeSessionKey returns the sticky key for Zen session headers, falling
// back to the same process-stable canonical session id used by the regular
// proxy path. The current free-tier gate rejects non-canonical random IDs.
func bridgeSessionKey(stickyKey string) string {
	if strings.TrimSpace(stickyKey) != "" {
		return stickyKey
	}
	return fallbackSessionID()
}

// proxyAnthropicViaResponses serves POST /v1/messages for a Responses-native
// model by bridging to upstream /v1/responses.
func (s *Server) proxyAnthropicViaResponses(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	areq *proxy.AnthropicRequest,
	upstream, zenKey string,
	incomingModel, targetModel string,
	stickyKey string,
	timeoutSeconds int,
	start time.Time,
) {
	upBody, err := proxy.ConvertAnthropicToResponsesBody(areq, targetModel, proxy.BridgeOptions{
		PromptCacheKey: stickyKey,
		EffortLevels:   s.levelsForModel(targetModel),
		DefaultEffort:  s.cfg.BridgeDefaultEffort,
	})
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, areq.Stream,
			http.StatusBadRequest, err.Error(), body, time.Since(start))
		return
	}
	// Same sanitize pass as the Responses passthrough path (model rewrite is
	// a no-op here since the builder already set it; dedupe + tool_choice
	// coercion still apply).
	upBody, err = sanitizeResponsesPassthroughBody(upBody, targetModel)
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error",
			"could not prepare upstream Responses request: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, areq.Stream,
			http.StatusInternalServerError, err.Error(), body, time.Since(start))
		return
	}
	upstreamStream := areq.Stream
	if proxy.IsFreeModel(targetModel) {
		upBody, err = proxy.PrepareFreeResponsesBody(upBody)
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
				"could not prepare Zen free-tier Responses request: "+err.Error())
			return
		}
		upstreamStream = true
	}

	select {
	case museSparkUpstreamSlots <- struct{}{}:
		defer func() { <-museSparkUpstreamSlots }()
	case <-r.Context().Done():
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "client went away while queued for upstream slot")
		s.logFailed(r.Context(), r, incomingModel, targetModel, areq.Stream,
			http.StatusBadGateway, "queue wait canceled", body, time.Since(start))
		return
	}

	upURL := strings.TrimRight(upstream, "/") + "/v1/responses"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upURL, bytes.NewReader(upBody))
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error",
			"could not build upstream request: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, areq.Stream,
			http.StatusInternalServerError, err.Error(), body, time.Since(start))
		return
	}
	upReq.Header.Set("Authorization", "Bearer "+zenKey)
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("User-Agent", ocUA())
	setZenSessionHeaders(upReq, r.Header, stickyKey)
	if upstreamStream {
		upReq.Header.Set("Accept", "text/event-stream")
	} else {
		upReq.Header.Set("Accept", "application/json")
	}

	httpClient := s.upstreamClient(upstreamStream, timeoutSeconds)
	resp, err := doUpstreamWithRetry(httpClient, upReq, upBody)
	if err != nil {
		logUpstreamError(r, incomingModel, targetModel, areq.Stream, time.Since(start), err)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, areq.Stream,
			http.StatusBadGateway, err.Error(), body, time.Since(start))
		return
	}
	if newResp, newBody, ok := s.maybeRetryStaleReasoning(httpClient, upReq, upBody, resp,
		incomingModel, targetModel, areq.Stream, start); ok {
		resp, upBody = newResp, newBody
	}
	if newResp, _, ok := s.maybeRetryInvalidEffort(httpClient, upReq, upBody, resp,
		incomingModel, targetModel, areq.Stream, start); ok {
		resp = newResp
	}

	if resp.StatusCode >= http.StatusBadRequest {
		s.passUpstreamError(w, resp, r, incomingModel, targetModel, body, start)
		return
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	// resample re-POSTs the identical upstream request (same body, same
	// headers) for the toolless-resample guard: one extra sample when a
	// completed turn declares tools but returns none. Timeouts/5xx get the
	// same single retry as the first attempt; anything else is the
	// caller's verdict to make.
	resample := func() (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return doUpstreamWithRetry(httpClient, upReq, upBody)
	}
	resampleEnabled := s.cfg.BridgeToollessResample
	if upstreamStream && resp.StatusCode < http.StatusBadRequest &&
		(contentType == "" || strings.Contains(contentType, "text/event-stream")) {
		if areq.Stream {
			s.relayAnthropicResponsesStream(w, resp, r, incomingModel, targetModel, areq, body, start, resample, resampleEnabled)
		} else {
			s.relayAnthropicResponsesAggregated(w, resp, r, incomingModel, targetModel, areq, body, start, resample, resampleEnabled)
		}
		return
	}
	s.relayAnthropicResponsesJSON(w, resp, r, incomingModel, targetModel, areq, body, start, resample, resampleEnabled)
}

func (s *Server) relayAnthropicResponsesAggregated(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	areq *proxy.AnthropicRequest,
	reqBody []byte,
	start time.Time,
	resample func() (*http.Response, error),
	resampleEnabled bool,
) {
	defer resp.Body.Close()
	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not decompress upstream stream: "+err.Error())
			s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
			return
		}
		defer gz.Close()
		reader = gz
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not read upstream stream: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	final, err := proxy.AggregateResponsesSSE(raw)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	aresp, err := proxy.ConvertResponsesToAnthropicResponse(final, incomingModel)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	removed := filterUndeclaredToolUses(aresp, areq.Tools)
	if len(removed) > 0 {
		log.Printf("opencode-cc: filtered %d undeclared tool_use block(s) name=%.300q model=%s target=%s (bridge aggregated; client must 'continue')",
			len(removed), strings.Join(removed, ","), incomingModel, targetModel)
	}
	if resampled, ok := s.maybeResampleToollessJSON(r, resample, resampleEnabled, areq, aresp, incomingModel, targetModel, true, len(removed)); ok {
		aresp = resampled
	}
	writeJSON(w, http.StatusOK, aresp)
	stop := ""
	if aresp.StopReason != nil {
		stop = *aresp.StopReason
	}
	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, false, http.StatusOK,
		aresp.Usage.InputTokens, aresp.Usage.OutputTokens,
		aresp.Usage.CacheReadInputTokens, aresp.Usage.CacheCreationInputTokens,
		stop, string(reqBody), mustJSON(aresp), time.Since(start))
}

// maybeResampleToollessJSON retries a completed tool-less bridge turn once
// against the identical upstream request (non-streaming relays). It returns
// the replacement response and true only when the resample yields tool_use;
// otherwise the original stands. aggregate selects SSE-aggregate reading
// (forced-stream upstream) vs plain JSON body reading.
func (s *Server) maybeResampleToollessJSON(
	r *http.Request,
	resample func() (*http.Response, error),
	enabled bool,
	areq *proxy.AnthropicRequest,
	aresp *proxy.AnthropicResponse,
	incomingModel, targetModel string,
	aggregate bool,
	removedUndeclared int,
) (*proxy.AnthropicResponse, bool) {
	stop := ""
	if aresp.StopReason != nil {
		stop = *aresp.StopReason
	}
	upstreamStatus := ""
	if stop == "max_tokens" {
		upstreamStatus = "incomplete"
	}
	do, verdict := proxy.ShouldResampleToolless(enabled, len(areq.Tools), stop,
		stop == "tool_use", removedUndeclared, aresp.Usage.OutputTokens, upstreamStatus)
	if !do {
		return nil, false
	}
	log.Printf("opencode-cc: toolless resample (%s) out=%d model=%s target=%s",
		verdict, aresp.Usage.OutputTokens, incomingModel, targetModel)
	r2, err := resample()
	if err != nil {
		log.Printf("opencode-cc: toolless resample failed: %v model=%s target=%s",
			err, incomingModel, targetModel)
		return nil, false
	}
	defer r2.Body.Close()
	if r2.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(r2.Body, 64*1024))
		log.Printf("opencode-cc: toolless resample upstream %d model=%s target=%s",
			r2.StatusCode, incomingModel, targetModel)
		return nil, false
	}
	raw2, err := readUpstreamBody(r2, aggregate)
	if err != nil {
		log.Printf("opencode-cc: toolless resample unreadable: %v model=%s target=%s",
			err, incomingModel, targetModel)
		return nil, false
	}
	aresp2, err := proxy.ConvertResponsesToAnthropicResponse(raw2, incomingModel)
	if err != nil {
		log.Printf("opencode-cc: toolless resample unparseable: %v model=%s target=%s",
			err, incomingModel, targetModel)
		return nil, false
	}
	if removed := filterUndeclaredToolUses(aresp2, areq.Tools); len(removed) > 0 {
		log.Printf("opencode-cc: toolless resample filtered %d undeclared tool_use block(s) model=%s target=%s",
			len(removed), incomingModel, targetModel)
	}
	if aresp2.StopReason == nil || *aresp2.StopReason != "tool_use" {
		log.Printf("opencode-cc: toolless resample still toolless model=%s target=%s",
			incomingModel, targetModel)
		return nil, false
	}
	log.Printf("opencode-cc: toolless resample RESCUED model=%s target=%s",
		incomingModel, targetModel)
	return aresp2, true
}

// readUpstreamBody reads a full upstream Responses body, aggregating SSE
// when aggregate is set (forced-stream upstream lane) or reading plain
// JSON otherwise.
func readUpstreamBody(resp *http.Response, aggregate bool) ([]byte, error) {
	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if !aggregate {
		return raw, nil
	}
	return proxy.AggregateResponsesSSE(raw)
}

// relayAnthropicResponsesJSON converts a non-streaming upstream Responses
// body to an Anthropic Messages response.
func (s *Server) relayAnthropicResponsesJSON(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	areq *proxy.AnthropicRequest,
	reqBody []byte,
	start time.Time,
	resample func() (*http.Response, error),
	resampleEnabled bool,
) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not read upstream body: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false,
			http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	if len(raw) > maxResponseBytes {
		const msg = "upstream response exceeded the maximum allowed size"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", msg)
		s.logFailed(r.Context(), r, incomingModel, targetModel, false,
			http.StatusBadGateway, msg, reqBody, time.Since(start))
		return
	}
	aresp, err := proxy.ConvertResponsesToAnthropicResponse(raw, incomingModel)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false,
			http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	removedJSON := filterUndeclaredToolUses(aresp, areq.Tools)
	if len(removedJSON) > 0 {
		log.Printf("opencode-cc: filtered %d undeclared tool_use block(s) name=%.300q model=%s target=%s (bridge JSON; client must 'continue')",
			len(removedJSON), strings.Join(removedJSON, ","), incomingModel, targetModel)
	}
	if resampled, ok := s.maybeResampleToollessJSON(r, resample, resampleEnabled, areq, aresp, incomingModel, targetModel, false, len(removedJSON)); ok {
		aresp = resampled
	}
	writeJSON(w, http.StatusOK, aresp)

	stop := ""
	if aresp.StopReason != nil {
		stop = *aresp.StopReason
	}
	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, false, http.StatusOK,
		aresp.Usage.InputTokens, aresp.Usage.OutputTokens,
		aresp.Usage.CacheReadInputTokens, aresp.Usage.CacheCreationInputTokens,
		stop, string(reqBody), mustJSON(aresp), time.Since(start))
}

// maybeResampleToollessStream retries a completed tool-less bridge turn
// once against the identical upstream request, scanning the second sample
// into the same still-open converter. It returns true only when the
// resample yields tool_use (the turn then finalizes tool_use); otherwise
// the caller finalizes the original. Must run before Finalize: after
// message_stop nothing more may be emitted downstream.
func (s *Server) maybeResampleToollessStream(
	r *http.Request,
	bw *bufio.Writer,
	flusher http.Flusher,
	conv *proxy.ResponsesToAnthropicStreamer,
	resample func() (*http.Response, error),
	enabled bool,
	areq *proxy.AnthropicRequest,
	incomingModel, targetModel string,
) bool {
	do, verdict := proxy.ShouldResampleToolless(enabled, len(areq.Tools),
		"end_turn", conv.HasToolUse() || conv.HasPendingTools(), len(conv.DroppedCalls()),
		conv.OutputTokens(), conv.CompletedStatus())
	if !do {
		return false
	}
	log.Printf("opencode-cc: toolless resample (%s) out=%d model=%s target=%s",
		verdict, conv.OutputTokens(), incomingModel, targetModel)
	// Snapshot speculative state: a failed resample must never corrupt
	// the good first attempt (a recorded failure would flip Finalize
	// into an error event; a spurious incomplete would mislabel the
	// stop as max_tokens).
	incompleteBefore := conv.Incomplete()
	restore := func() {
		conv.ClearFailed()
		conv.SetIncomplete(incompleteBefore)
	}
	r2, err := resample()
	if err != nil {
		log.Printf("opencode-cc: toolless resample failed: %v model=%s target=%s",
			err, incomingModel, targetModel)
		restore()
		return false
	}
	defer r2.Body.Close()
	if r2.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(r2.Body, 64*1024))
		log.Printf("opencode-cc: toolless resample upstream %d model=%s target=%s",
			r2.StatusCode, incomingModel, targetModel)
		restore()
		return false
	}
	reader := io.Reader(r2.Body)
	if strings.EqualFold(r2.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(r2.Body)
		if gerr != nil {
			log.Printf("opencode-cc: toolless resample undecodable model=%s target=%s",
				incomingModel, targetModel)
			restore()
			return false
		}
		defer gz.Close()
		reader = gz
	}
	scanErr := scanAnthropicSSE(reader, func(event string, data []byte) error {
		if herr := conv.HandleEvent(event, data); herr != nil {
			return herr
		}
		if herr := bw.Flush(); herr != nil {
			return herr
		}
		flusher.Flush()
		return nil
	})
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		log.Printf("opencode-cc: toolless resample stream error: %v model=%s target=%s",
			scanErr, incomingModel, targetModel)
		restore()
		return false
	}
	_ = bw.Flush()
	flusher.Flush()
	if !conv.HasToolUse() && !conv.HasPendingTools() {
		log.Printf("opencode-cc: toolless resample still toolless model=%s target=%s",
			incomingModel, targetModel)
		restore()
		return false
	}
	// Rescued: a failure flag from the speculative sample must not flip
	// Finalize into an error event (the tools are real; HasToolUse wins).
	conv.ClearFailed()
	log.Printf("opencode-cc: toolless resample RESCUED model=%s target=%s",
		incomingModel, targetModel)
	return true
}

// relayAnthropicResponsesStream converts an upstream Responses SSE stream to
// Anthropic SSE events, flushing continuously.
func (s *Server) relayAnthropicResponsesStream(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	areq *proxy.AnthropicRequest,
	reqBody []byte,
	start time.Time,
	resample func() (*http.Response, error),
	resampleEnabled bool,
) {
	defer resp.Body.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming not supported by this server")
		return
	}

	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not decompress upstream stream: "+err.Error())
			return
		}
		defer gz.Close()
		reader = gz
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	bw := bufio.NewWriter(w)
	conv := proxy.NewResponsesToAnthropicStreamer(bw, incomingModel)
	conv.RestrictTools(anthropicToolNames(areq.Tools))
	conv.SetDeclaredSchemas(areq.Tools)

	// First byte now, not at first text: upstream sends response.created
	// immediately but the first convertible content can lag seconds behind
	// (reasoning phase). Idempotent — content paths ensure it anyway.
	if err := conv.EmitMessageStart(); err != nil {
		return
	}
	if err := bw.Flush(); err != nil {
		return
	}
	flusher.Flush()

	scanErr := scanAnthropicSSE(reader, func(event string, data []byte) error {
		if err := conv.HandleEvent(event, data); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		// Close the message first (response.completed no longer finalizes
		// mid-scan), then surface the error, preserving the old event
		// order: message_delta/message_stop before the error event.
		_ = conv.Finalize()
		_ = conv.EmitError("api_error", "upstream stream error: "+scanErr.Error())
		_ = bw.Flush()
		flusher.Flush()
		s.logFailed(r.Context(), r, incomingModel, targetModel, true,
			http.StatusBadGateway, scanErr.Error(), reqBody, time.Since(start))
		return
	}
	// Toolless resample: the first attempt is fully converted but NOT yet
	// finalized (no message_delta/message_stop on the wire), so a rescued
	// second sample appends its text + tool blocks into the same open
	// message and the turn ends tool_use. When the resample is skipped or
	// still tool-less, Finalize runs exactly once below as before.
	if s.maybeResampleToollessStream(r, bw, flusher, conv, resample, resampleEnabled, areq, incomingModel, targetModel) {
		// Rescued: usage now reflects the resample that produced the tools.
	}
	if err := conv.Finalize(); err != nil {
		s.logFailed(r.Context(), r, incomingModel, targetModel, true,
			http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	_ = bw.Flush()
	flusher.Flush()

	stopReason := conv.StopReason()
	if renamed := conv.RenamedToolCalls(); renamed > 0 {
		log.Printf("opencode-cc: resolved %d upstream tool call(s) to declared names model=%s target=%s",
			renamed, incomingModel, targetModel)
	}
	if recovered := conv.RecoveredCalls(); recovered > 0 {
		log.Printf("opencode-cc: recovered %d nameless upstream tool call(s) by schema model=%s target=%s",
			recovered, incomingModel, targetModel)
	}
	dropped := conv.DroppedCalls()
	for _, d := range dropped {
		log.Printf("opencode-cc: dropped upstream tool call reason=%q name=%q call_id=%q item=%q added_type=%q saw_added=%v saw_done=%v args_len=%d args=%.160q declared=%.300q first_frame=%.300q model=%s target=%s (client must 'continue')",
			d.Reason, d.Name, d.CallID, d.ItemID, d.AddedType, d.SawAdded, d.SawDone, d.ArgsLen, d.ArgsHead, d.Declared, d.FirstFrame, incomingModel, targetModel)
	}
	for _, b := range conv.BadFrames() {
		log.Printf("opencode-cc: unparsable upstream frame %.350q model=%s target=%s",
			b, incomingModel, targetModel)
	}
	if stopReason == "end_turn" && len(dropped) == 0 && len(areq.Tools) > 0 {
		// Tools were declared but none came back: always log the frame
		// census (one line, no content) so a stall is diagnosable even
		// when the text is long. The text head is added below for short
		// turns only, to keep long legit answers out of the log.
		// A complete census with message items and no function_call
		// anywhere means upstream genuinely sent text-only; a
		// function_call in the census that produced no tool_use means
		// the loss is inside this converter.
		log.Printf("opencode-cc: toolless end_turn out=%d in=%d empty_text=%v frames=%s model=%s target=%s",
			conv.OutputTokens(), conv.InputTokens(), conv.EmptyText(), conv.FrameCensus(), incomingModel, targetModel)
	}
	if stopReason == "end_turn" && len(dropped) == 0 && conv.OutputTokens() < 200 {
		// Tool-less short end_turn: log the text head plus the upstream
		// frame census so the next stall carries its own evidence
		// (promise narration vs legit turn end vs a shape the parser
		// didn't understand — an empty text with no text deltas in the
		// census means the copy lived only in done frames or never came).
		log.Printf("opencode-cc: short end_turn out=%d in=%d empty_text=%v text=%.400q frames=%s model=%s target=%s",
			conv.OutputTokens(), conv.InputTokens(), conv.EmptyText(), conv.TextHead(), conv.FrameCensus(), incomingModel, targetModel)
	}
	if stopReason == "end_turn" && len(dropped) == 0 && isForcedToolChoice(areq) {
		// The client demanded a tool call (any/tool) but Zen Responses
		// accepts only tool_choice auto (verified live: required/named
		// 400 upstream), so a text-only turn here is a forced stall the
		// client will have to 'continue' out of.
		log.Printf("opencode-cc: forced-tool turn returned tool-less out=%d in=%d text=%.400q model=%s target=%s",
			conv.OutputTokens(), conv.InputTokens(), conv.TextHead(), incomingModel, targetModel)
	}
	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, true, http.StatusOK,
		conv.InputTokens(), conv.OutputTokens(), conv.CachedTokens(), 0,
		stopReason, string(reqBody), "[streamed]", time.Since(start))
}

func anthropicToolNames(tools []proxy.AnthropicTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// isForcedToolChoice reports whether the client demanded a tool call
// (tool_choice any/tool). The bridge must still send upstream auto (Zen
// 400s anything else), so a tool-less turn on such a request is a stall
// worth logging, not a natural stop.
func isForcedToolChoice(areq *proxy.AnthropicRequest) bool {
	if areq == nil {
		return false
	}
	return areq.ToolChoice.Type == "any" || areq.ToolChoice.Type == "tool"
}
