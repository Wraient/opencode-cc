package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	cfgpkg "github.com/Kiowx/opencode-cc/internal/config"
	"github.com/Kiowx/opencode-cc/internal/proxy"
	"github.com/Kiowx/opencode-cc/internal/store"
)

// Proxy handles POST /v1/messages. Native Anthropic-capable target models go
// straight to the upstream Messages API; all other models are translated to
// OpenAI Chat Completions and converted back.
func (s *Server) Proxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body: "+err.Error())
			return
		}

		var areq proxy.AnthropicRequest
		if err := json.Unmarshal(body, &areq); err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request body is not valid Anthropic JSON: "+err.Error())
			return
		}

		// Resolve target model under a short read lock.
		cfg := s.cfg.Snapshot()
		nativeAnthropic := cfg.NativeAnthropic
		timeoutSeconds := cfg.RequestTimeoutSeconds
		targetModel := s.cfg.ResolveModel(areq.Model)
		// Responses-native models (muse-spark*) are served ONLY on upstream
		// /v1/responses: bridge this Messages request through the translation
		// in anthropic_responses.go instead of relaying an opaque upstream 500.
		if proxy.IsResponsesNativeModel(targetModel) {
			// Never empty: the upstream gates free-tier on x-opencode-session.
			stickyHint := bridgeSessionKey(proxy.AnthropicPromptCacheHint(&areq))
			bridgeUpstream, bridgeKey, ok := s.cfg.NextUpstreamForKey(stickyHint)
			if !ok {
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "no upstream API key configured. Set one in the web panel (Settings → upstreams).")
				s.logFailed(ctx, r, areq.Model, targetModel, areq.Stream, http.StatusUnauthorized, "no upstream api key", body, time.Since(start))
				return
			}
			s.proxyAnthropicViaResponses(w, r, body, &areq, bridgeUpstream, bridgeKey,
				areq.Model, targetModel, stickyHint, timeoutSeconds, start)
			return
		}
		hasWebSearch := shouldUseWebSearchShim(&areq)
		webSearchMode := cfg.ResolveWebSearchMode()

		oreq := proxy.ConvertRequest(&areq, func(string) string { return targetModel })
		applyThinkingBudgetMapping(oreq, &areq, targetModel, cfg)
		proxy.ApplyOpenAIPromptCache(oreq, promptCacheOptionsFromConfig(cfg))
		stickyKey := proxy.AnthropicPromptCacheHint(&areq)
		if oreq.PromptCacheKey != "" {
			stickyKey = oreq.PromptCacheKey
		}

		upstream, zenKey, ok := s.cfg.NextUpstreamForKey(stickyKey)
		if !ok {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "no upstream API key configured. Set one in the web panel (Settings → upstreams).")
			s.logFailed(ctx, r, areq.Model, targetModel, areq.Stream, http.StatusUnauthorized, "no upstream api key", body, time.Since(start))
			return
		}
		if hasWebSearch && webSearchMode == cfgpkg.WebSearchModeNative {
			searchUpstream, searchKey := cfg.ResolveWebSearchUpstream(upstream, zenKey)
			searchModel := cfg.ResolveWebSearchModel(targetModel)
			s.proxyNativeAnthropic(w, r, body, &areq, searchUpstream, searchKey, searchModel, timeoutSeconds, stickyKey, start)
			return
		}

		if nativeAnthropic && proxy.IsNativeAnthropicModel(targetModel) &&
			!(hasWebSearch && webSearchMode == cfgpkg.WebSearchModeTranslate) {
			s.proxyNativeAnthropic(w, r, body, &areq, upstream, zenKey, targetModel, timeoutSeconds, stickyKey, start)
			return
		}

		if s.handleWebSearchShim(w, r, body, &areq, oreq, upstream, zenKey, targetModel, cfg, timeoutSeconds, start) {
			return
		}

		// Marshal the upstream request.
		upBody, err := json.Marshal(oreq)
		if err != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", "could not encode upstream request: "+err.Error())
			return
		}
		upstreamStream := areq.Stream
		if proxy.IsFreeModel(targetModel) {
			upBody, err = proxy.PrepareFreeChatBody(upBody)
			if err != nil {
				writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "could not prepare Zen free-tier request: "+err.Error())
				return
			}
			upstreamStream = true
		}

		upURL := strings.TrimRight(upstream, "/") + "/v1/chat/completions"
		upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upURL, bytes.NewReader(upBody))
		if err != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", "could not build upstream request: "+err.Error())
			return
		}
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("Authorization", "Bearer "+zenKey)
		// Match the Accept header to the request mode: Zen (and most OpenAI-
		// compatible servers) gate SSE delivery on Accept: text/event-stream.
		// Sending application/json on a stream:true request makes some upstreams
		// refuse with "streaming not supported".
		if upstreamStream {
			upReq.Header.Set("Accept", "text/event-stream")
		} else {
			upReq.Header.Set("Accept", "application/json")
		}
		// Some upstreams prefer a UA.
		upReq.Header.Set("User-Agent", ocUA())
		setZenSessionHeaders(upReq, r.Header, stickyKey)
		// Propagate the anthropic-version / anthropic-beta for observability
		// on the upstream side (Zen ignores them for the OpenAI path).
		if v := r.Header.Get("anthropic-version"); v != "" {
			upReq.Header.Set("anthropic-version", v)
		}

		httpClient := s.upstreamClient(upstreamStream, timeoutSeconds)

		resp, err := httpClient.Do(upReq)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
			s.logFailed(ctx, r, areq.Model, targetModel, areq.Stream, http.StatusBadGateway, err.Error(), body, time.Since(start))
			return
		}

		// Non-2xx: pass the upstream error back in Anthropic shape.
		if resp.StatusCode >= 400 {
			s.passUpstreamError(w, resp, r, areq.Model, targetModel, body, start)
			return
		}

		// chatResample re-POSTs the identical upstream Chat request for the
		// toolless-resample guard (same body, same headers).
		chatResample := func() (*http.Response, error) {
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			return doUpstreamWithRetry(httpClient, upReq, upBody)
		}

		if areq.Stream {
			s.handleStreamResponse(w, resp, r, areq.Model, targetModel, body, start, chatResample, s.cfg.BridgeToollessResample)
		} else if upstreamStream && resp.StatusCode < http.StatusBadRequest &&
			(strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") || resp.Header.Get("Content-Type") == "") {
			s.handleAggregatedChatResponse(w, resp, r, areq.Model, targetModel, body, start)
		} else {
			s.handleNonStreamResponse(w, resp, r, areq.Model, targetModel, body, start)
		}
	}
}

func (s *Server) proxyNativeAnthropic(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	areq *proxy.AnthropicRequest,
	upstream, zenKey, targetModel string,
	timeoutSeconds int,
	stickyKey string,
	start time.Time,
) {
	upBody, err := proxy.PrepareAnthropicPromptCacheBody(body, targetModel, promptCacheOptionsFromConfig(s.cfg.Snapshot()))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	upURL := strings.TrimRight(upstream, "/") + "/v1/messages"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upURL, bytes.NewReader(upBody))
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "could not build upstream request: "+err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+zenKey)
	upReq.Header.Set("x-api-key", zenKey)
	upReq.Header.Set("User-Agent", ocUA())
	setZenSessionHeaders(upReq, r.Header, stickyKey)
	if areq.Stream {
		upReq.Header.Set("Accept", "text/event-stream")
	} else {
		upReq.Header.Set("Accept", "application/json")
	}
	if version := r.Header.Get("anthropic-version"); version != "" {
		upReq.Header.Set("anthropic-version", version)
	} else {
		upReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		upReq.Header.Set("anthropic-beta", beta)
	}

	httpClient := s.upstreamClient(areq.Stream, timeoutSeconds)
	resp, err := httpClient.Do(upReq)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
		s.logFailed(r.Context(), r, areq.Model, targetModel, areq.Stream, http.StatusBadGateway, err.Error(), body, time.Since(start))
		return
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if areq.Stream && resp.StatusCode < http.StatusBadRequest &&
		(contentType == "" || strings.Contains(contentType, "text/event-stream")) {
		s.relayNativeAnthropicStream(w, resp, r, areq.Model, targetModel, body, start)
		return
	}
	s.relayNativeAnthropicJSON(w, resp, r, areq.Model, targetModel, areq.Stream, body, start)
}

func (s *Server) relayNativeAnthropicJSON(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	inModel, target string,
	stream bool,
	reqBody []byte,
	start time.Time,
) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not read upstream body: "+err.Error())
		s.logFailed(r.Context(), r, inModel, target, stream, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	if len(raw) > maxResponseBytes {
		const msg = "upstream response exceeded the maximum allowed size"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", msg)
		s.logFailed(r.Context(), r, inModel, target, stream, http.StatusBadGateway, msg, reqBody, time.Since(start))
		return
	}

	copyOpenAIHeaders(w.Header(), resp.Header, false)
	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)

	if status >= http.StatusBadRequest {
		msg := extractAnthropicError(raw)
		if msg == "" {
			msg = extractOpenAIError(raw)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		s.logFailed(r.Context(), r, inModel, target, stream, status, msg, reqBody, time.Since(start))
		return
	}

	var out struct {
		StopReason *string              `json:"stop_reason"`
		Usage      proxy.AnthropicUsage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &out)
	s.logSuccessWithCache(r.Context(), r, inModel, target, stream, status,
		out.Usage.InputTokens, out.Usage.OutputTokens,
		out.Usage.CacheReadInputTokens, out.Usage.CacheCreationInputTokens,
		stopReasonStr(out.StopReason),
		string(reqBody), string(raw), time.Since(start))
}

func (s *Server) relayNativeAnthropicStream(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	inModel, target string,
	reqBody []byte,
	start time.Time,
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

	copyOpenAIHeaders(w.Header(), resp.Header, true)
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	var responseLog strings.Builder
	relay := &nativeAnthropicStreamRelay{
		dst:      w,
		flusher:  flusher,
		log:      &responseLog,
		logLimit: s.cfg.Snapshot().MaxBodyLogBytes,
	}
	if _, err := io.Copy(relay, reader); err != nil {
		errPayload, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "api_error", "message": "upstream stream error: " + err.Error()},
		})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", errPayload)
		flusher.Flush()
		s.logFailed(r.Context(), r, inModel, target, true, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}

	s.logSuccessWithCache(r.Context(), r, inModel, target, true, resp.StatusCode,
		relay.inputTokens, relay.outputTokens,
		relay.cachedInputTokens, relay.cacheCreationInputTokens,
		relay.stopReason,
		string(reqBody), responseLog.String(), time.Since(start))
}

type nativeAnthropicStreamRelay struct {
	dst      io.Writer
	flusher  http.Flusher
	log      *strings.Builder
	logLimit int

	pending                  []byte
	inputTokens              int
	outputTokens             int
	cachedInputTokens        int
	cacheCreationInputTokens int
	stopReason               string
}

func (r *nativeAnthropicStreamRelay) Write(p []byte) (int, error) {
	n, err := r.dst.Write(p)
	if n > 0 {
		appendLimited(r.log, string(p[:n]), r.logLimit)
		r.observe(p[:n])
		r.flusher.Flush()
	}
	return n, err
}

func (r *nativeAnthropicStreamRelay) observe(p []byte) {
	r.pending = append(r.pending, p...)
	for {
		i := bytes.IndexByte(r.pending, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimSuffix(string(r.pending[:i]), "\r")
		r.pending = r.pending[i+1:]
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Message struct {
				Usage proxy.AnthropicUsage `json:"usage"`
			} `json:"message"`
			Delta struct {
				StopReason *string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if event.Message.Usage.InputTokens > 0 ||
			event.Message.Usage.CacheReadInputTokens > 0 ||
			event.Message.Usage.CacheCreationInputTokens > 0 {
			r.inputTokens = event.Message.Usage.InputTokens
			r.cachedInputTokens = event.Message.Usage.CacheReadInputTokens
			r.cacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
		}
		if event.Usage != nil {
			r.outputTokens = event.Usage.OutputTokens
		}
		if event.Delta.StopReason != nil {
			r.stopReason = *event.Delta.StopReason
		}
	}
}

// passUpstreamError reads the upstream error body and returns an Anthropic-
// shaped error to the client, while logging the failed request.
func (s *Server) passUpstreamError(w http.ResponseWriter, resp *http.Response, r *http.Request, inModel, target string, reqBody []byte, start time.Time) {
	defer resp.Body.Close()
	eb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	msg := strings.TrimSpace(string(eb))
	// Try to extract OpenAI error message.
	if em := extractOpenAIError(eb); em != "" {
		msg = em
	}
	if msg == "" {
		msg = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
	}
	errType := "api_error"
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		errType = "authentication_error"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		errType = "timeout_error"
	}
	writeAnthropicError(w, resp.StatusCode, errType, msg)
	s.logFailed(r.Context(), r, inModel, target, false, resp.StatusCode, msg, reqBody, time.Since(start))
}

// handleNonStreamResponse decodes the upstream JSON, converts it, and writes
// the Anthropic response.
func (s *Server) handleNonStreamResponse(w http.ResponseWriter, resp *http.Response, r *http.Request, inModel, target string, reqBody []byte, start time.Time) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not read upstream body: "+err.Error())
		s.logFailed(r.Context(), r, inModel, target, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	oresp, err := proxy.ParseOpenAIResponse(raw)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not parse upstream response: "+err.Error())
		s.logFailed(r.Context(), r, inModel, target, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	aresp := proxy.ConvertResponse(oresp, inModel)
	if removed := filterUndeclaredToolUses(aresp, areqToolsFromBody(reqBody)); len(removed) > 0 {
		log.Printf("opencode-cc: filtered %d undeclared tool_use block(s) name=%.300q model=%s target=%s (non-streaming; client must 'continue')",
			len(removed), strings.Join(removed, ","), inModel, target)
	}
	writeJSON(w, http.StatusOK, aresp)

	// Log a success row.
	s.logSuccessWithCache(r.Context(), r, inModel, target, false, http.StatusOK,
		aresp.Usage.InputTokens, aresp.Usage.OutputTokens,
		aresp.Usage.CacheReadInputTokens, aresp.Usage.CacheCreationInputTokens,
		stopReasonStr(aresp.StopReason),
		string(reqBody), mustJSON(aresp), time.Since(start))
}

// handleAggregatedChatResponse converts a forced upstream Chat SSE response to
// the ordinary non-streaming Anthropic Messages response shape.
func (s *Server) handleAggregatedChatResponse(w http.ResponseWriter, resp *http.Response, r *http.Request, inModel, target string, reqBody []byte, start time.Time) {
	defer resp.Body.Close()
	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "could not decompress upstream stream: "+err.Error())
			s.logFailed(r.Context(), r, inModel, target, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
			return
		}
		defer gz.Close()
		reader = gz
	}
	chat, err := proxy.AggregateOpenAIStream(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		s.logFailed(r.Context(), r, inModel, target, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	aresp := proxy.ConvertResponse(chat, inModel)
	if removed := filterUndeclaredToolUses(aresp, areqToolsFromBody(reqBody)); len(removed) > 0 {
		log.Printf("opencode-cc: filtered %d undeclared tool_use block(s) name=%.300q model=%s target=%s (aggregated; client must 'continue')",
			len(removed), strings.Join(removed, ","), inModel, target)
	}
	writeJSON(w, http.StatusOK, aresp)
	s.logSuccessWithCache(r.Context(), r, inModel, target, false, http.StatusOK,
		aresp.Usage.InputTokens, aresp.Usage.OutputTokens,
		aresp.Usage.CacheReadInputTokens, aresp.Usage.CacheCreationInputTokens,
		stopReasonStr(aresp.StopReason), string(reqBody), mustJSON(aresp), time.Since(start))
}

// handleStreamResponse proxies the SSE stream, converting each OpenAI chunk to
// Anthropic events. It flushes continuously so the client sees real-time data.
func (s *Server) handleStreamResponse(w http.ResponseWriter, resp *http.Response, r *http.Request, inModel, target string, reqBody []byte, start time.Time, resample func() (*http.Response, error), resampleEnabled bool) {
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming not supported by this server")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Flush headers immediately: without this net/http holds them (plus the
	// first ~2KB of events) until the buffer fills or the stream ends, which
	// makes TTFB equal total time on small responses.
	flusher.Flush()

	var stopSeq *string
	stopReason := ""
	var inputTok, outputTok int
	var cachedInputTok int

	conv, err := proxy.NewStreamConverter(w, inModel, stopSeq)
	if err != nil {
		return
	}
	// message_start + ping are already emitted (converter flushes its own
	// buffer); push them to the client now, before the first upstream chunk.
	flusher.Flush()
	conv.RestrictTools(anthropicToolNamesFromBody(reqBody))
	conv.SetDeclaredSchemas(areqToolsFromBody(reqBody))
	declaredTools := anthropicToolNamesFromBody(reqBody)

	// Read the upstream body, transparently decompressing gzip if needed.
	bodyReader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(resp.Body)
		if gerr == nil {
			defer gz.Close()
			bodyReader = gz
		}
	}

	scanRes, streamErr := proxy.ScanOpenAIStreamWithStatus(bodyReader, func(chunk *proxy.OpenAIStreamChunk) error {
		if chunk.Usage != nil {
			inputTok = chunk.Usage.PromptTokens
			outputTok = chunk.Usage.CompletionTokens
			cachedInputTok = chunk.Usage.CachedPromptTokens()
		}
		if err := conv.HandleChunk(chunk); err != nil {
			return err
		}
		// Push every translated chunk to the client immediately (matches the
		// other stream relays). The converter only flushes its own bufio
		// into the ResponseWriter; without this the HTTP layer holds bytes.
		flusher.Flush()
		return nil
	})
	malformed := scanRes.Malformed

	// Finalize the stream: emit content_block_stop (if open) + message_delta
	// (carrying usage) + message_stop. If the upstream errored mid-stream,
	// first try the aborted-stream resample (strict guards: no tool content
	// may exist yet); only then surface the error event.
	if streamErr != nil && streamErr.Error() != "EOF" {
		if s.maybeResampleAbortedChatStream(r, flusher, conv, resample, resampleEnabled, declaredTools, &inputTok, &outputTok, &cachedInputTok, &malformed, streamErr, inModel, target) {
			// Rescued: fall through to Finalize, which emits tool_use.
		} else {
			_ = conv.EmitError("api_error", "upstream stream error: "+streamErr.Error())
			stopReason = "stream_error"
			_ = conv.Finalize(stopReason)
			_ = conv.Flush()
			flusher.Flush()
			s.logFailed(r.Context(), r, inModel, target, true, http.StatusBadGateway, streamErr.Error(), reqBody, time.Since(start))
			return
		}
	}
	s.maybeResampleToollessChatStream(r, flusher, conv, resample, resampleEnabled, declaredTools, &inputTok, &outputTok, &cachedInputTok, &malformed, inModel, target)
	_ = conv.Finalize("end_turn")
	stopReason = conv.StopReason()
	_ = conv.Flush()
	flusher.Flush()

	if renamed := conv.RenamedToolCalls(); renamed > 0 {
		log.Printf("opencode-cc: resolved %d upstream tool call(s) to declared names model=%s target=%s",
			renamed, inModel, target)
	}
	if recovered := conv.RecoveredCalls(); recovered > 0 {
		log.Printf("opencode-cc: recovered %d nameless upstream tool call(s) by schema model=%s target=%s",
			recovered, inModel, target)
	}
	for _, d := range conv.DroppedCalls() {
		log.Printf("opencode-cc: dropped upstream tool call reason=%q name=%q call_id=%q args_len=%d args=%.160q declared=%.300q model=%s target=%s upstream_finish=%q (client must 'continue')",
			d.Reason, d.Name, d.CallID, d.ArgsLen, d.ArgsHead, d.Declared, inModel, target, conv.UpstreamFinish())
	}
	if stopReason == "end_turn" && len(conv.DroppedCalls()) == 0 && outputTok < 200 {
		// Tool-less short end_turn: log the text head so the next stall
		// carries its own evidence (promise narration vs legit turn end).
		// malformed counts upstream data lines that failed to parse and
		// were skipped: a corrupt tool-call chunk with a tool_calls finish
		// looks exactly like a stall, so the count is logged, never silent.
		log.Printf("opencode-cc: short end_turn out=%d in=%d upstream_finish=%q malformed=%d text=%.400q model=%s target=%s",
			outputTok, inputTok, conv.UpstreamFinish(), malformed, conv.TextHead(), inModel, target)
	}

	// Log.
	s.logSuccessWithCache(r.Context(), r, inModel, target, true, http.StatusOK,
		inputTok, outputTok, cachedInputTok, 0, stopReason, string(reqBody), "[streamed]", time.Since(start))
}

// maybeResampleToollessChatStream retries a completed tool-less Chat turn
// once against the identical upstream request, scanning the second sample
// into the same still-open converter (buffered tools emit once at Finalize,
// so ordering stays valid). Returns true only on rescue. Must run before
// Finalize. A resample that ends length-truncated without tools can
// relabel the stop max_tokens; that mislabel is benign (the client
// continues itself) and is noted here so it never confuses forensics.
func (s *Server) maybeResampleToollessChatStream(
	r *http.Request,
	flusher http.Flusher,
	conv *proxy.StreamConverter,
	resample func() (*http.Response, error),
	enabled bool,
	declaredTools []string,
	inputTok, outputTok, cachedInputTok *int,
	malformed *int,
	inModel, target string,
) bool {
	finish := conv.PendingFinish()
	upstreamStatus := finish
	if finish == "" || finish == "stop" {
		upstreamStatus = ""
	}
	do, verdict := proxy.ShouldResampleToolless(enabled, len(declaredTools),
		"end_turn", conv.HasToolUse(), len(conv.DroppedCalls()),
		*outputTok, upstreamStatus)
	if !do {
		return false
	}
	log.Printf("opencode-cc: toolless resample [chat] (%s) out=%d upstream_finish=%q model=%s target=%s",
		verdict, *outputTok, finish, inModel, target)
	return s.runChatResample(r, flusher, conv, resample, inputTok, outputTok, cachedInputTok, malformed, inModel, target)
}

// maybeResampleAbortedChatStream retries a mid-stream aborted Chat turn
// once, but ONLY when the aborted sample left no tool fragments behind
// (no emitted calls, nothing buffered, no drops). Appending a second
// sample onto partial fragments could duplicate or corrupt calls, so any
// tool content at all disables this path and the error surfaces as before.
func (s *Server) maybeResampleAbortedChatStream(
	r *http.Request,
	flusher http.Flusher,
	conv *proxy.StreamConverter,
	resample func() (*http.Response, error),
	enabled bool,
	declaredTools []string,
	inputTok, outputTok, cachedInputTok *int,
	malformed *int,
	scanErr error,
	inModel, target string,
) bool {
	if !enabled || len(declaredTools) == 0 || conv.HasToolUse() || conv.HasBufferedTools() ||
		len(conv.DroppedCalls()) != 0 || *outputTok > proxy.ToollessResampleMaxOutputTokens {
		return false
	}
	log.Printf("opencode-cc: aborted-stream resample [chat] (%v) out=%d model=%s target=%s",
		scanErr, *outputTok, inModel, target)
	return s.runChatResample(r, flusher, conv, resample, inputTok, outputTok, cachedInputTok, malformed, inModel, target)
}

// runChatResample POSTs once via resample and scans the result into the
// still-open converter. True only when the sample yields tool calls.
func (s *Server) runChatResample(
	r *http.Request,
	flusher http.Flusher,
	conv *proxy.StreamConverter,
	resample func() (*http.Response, error),
	inputTok, outputTok, cachedInputTok *int,
	malformed *int,
	inModel, target string,
) bool {
	r2, err := resample()
	if err != nil {
		log.Printf("opencode-cc: toolless resample [chat] failed: %v model=%s target=%s",
			err, inModel, target)
		return false
	}
	defer r2.Body.Close()
	if r2.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(r2.Body, 64*1024))
		log.Printf("opencode-cc: toolless resample [chat] upstream %d model=%s target=%s",
			r2.StatusCode, inModel, target)
		return false
	}
	reader := io.Reader(r2.Body)
	if strings.EqualFold(r2.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(r2.Body)
		if gerr != nil {
			log.Printf("opencode-cc: toolless resample [chat] undecodable model=%s target=%s",
				inModel, target)
			return false
		}
		defer gz.Close()
		reader = gz
	}
	scanRes, scanErr := proxy.ScanOpenAIStreamWithStatus(reader, func(chunk *proxy.OpenAIStreamChunk) error {
		if chunk.Usage != nil {
			*inputTok = chunk.Usage.PromptTokens
			*outputTok = chunk.Usage.CompletionTokens
			*cachedInputTok = chunk.Usage.CachedPromptTokens()
		}
		if herr := conv.HandleChunk(chunk); herr != nil {
			return herr
		}
		flusher.Flush()
		return nil
	})
	*malformed += scanRes.Malformed
	if scanErr != nil && scanErr.Error() != "EOF" {
		log.Printf("opencode-cc: toolless resample [chat] stream error: %v model=%s target=%s",
			scanErr, inModel, target)
		return false
	}
	flusher.Flush()
	if !conv.HasToolUse() {
		log.Printf("opencode-cc: toolless resample [chat] still toolless model=%s target=%s",
			inModel, target)
		return false
	}
	log.Printf("opencode-cc: toolless resample [chat] RESCUED model=%s target=%s",
		inModel, target)
	return true
}

// CountTokens handles POST /v1/messages/count_tokens with a rough estimate
// (~4 chars/token), which is all Claude Code uses it for.
func (s *Server) CountTokens() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
		var areq proxy.AnthropicRequest
		_ = json.Unmarshal(body, &areq)
		tokens := estimateTokens(&areq)
		writeJSON(w, http.StatusOK, proxy.CountTokensResponse{InputTokens: tokens})
	}
}

// estimateTokens returns a rough token count for the request (~4 chars/tok).
func estimateTokens(areq *proxy.AnthropicRequest) int {
	total := 0
	for _, b := range areq.System.Blocks {
		total += len(b.Text)
	}
	for _, m := range areq.Messages {
		if m.Content.IsStr {
			total += len(m.Content.Text)
			continue
		}
		for _, b := range m.Content.Blocks {
			total += len(b.Text)
		}
	}
	if total == 0 {
		return 1
	}
	return total/4 + 1
}

// ---- logging helpers ----

func (s *Server) logSuccess(ctx context.Context, r *http.Request, inModel, target string, stream bool, status, inputTok, outputTok int, stopReason, reqBody, respBody string, dur time.Duration) {
	s.logSuccessWithCache(ctx, r, inModel, target, stream, status, inputTok, outputTok, 0, 0, stopReason, reqBody, respBody, dur)
}

func (s *Server) logSuccessWithCache(ctx context.Context, r *http.Request, inModel, target string, stream bool, status, inputTok, outputTok, cachedInputTok, cacheCreationInputTok int, stopReason, reqBody, respBody string, dur time.Duration) {
	apiKey := APIKeyFromContext(ctx)
	if !s.shouldLog() {
		// Still record usage for quota even if request logging is off.
		s.recordUsage(apiKey, inputTok+outputTok, 1)
		return
	}
	if s.cfg.Snapshot().MaxBodyLogBytes > 0 {
		reqBody = truncate(reqBody, s.cfg.Snapshot().MaxBodyLogBytes)
		respBody = truncate(respBody, s.cfg.Snapshot().MaxBodyLogBytes)
	}
	var keyID int64
	if apiKey != nil {
		keyID = apiKey.ID
	}
	go func() {
		bg := context.Background()
		_ = s.store.InsertRequest(bg, &store.RequestRow{
			Ts:                       time.Now(),
			Method:                   r.Method,
			Path:                     r.URL.Path,
			IncomingModel:            inModel,
			TargetModel:              target,
			Stream:                   stream,
			Status:                   status,
			DurationMs:               dur.Milliseconds(),
			InputTokens:              inputTok,
			OutputTokens:             outputTok,
			CachedInputTokens:        cachedInputTok,
			CacheCreationInputTokens: cacheCreationInputTok,
			StopReason:               stopReason,
			ReqBody:                  reqBody,
			RespBody:                 respBody,
			APIKeyID:                 keyID,
		})
	}()
	s.recordUsage(apiKey, inputTok+outputTok, 1)
}

// recordUsage bumps the key's quota counters (lifetime + daily). No-op if no
// authenticated key is present.
func (s *Server) recordUsage(apiKey *store.APIKey, tokens, requests int) {
	if apiKey == nil {
		return
	}
	id := apiKey.ID
	go func() { _ = s.store.RecordUsage(context.Background(), id, tokens, requests) }()
}

func (s *Server) logFailed(ctx context.Context, r *http.Request, inModel, target string, stream bool, status int, errMsg string, reqBody []byte, dur time.Duration) {
	apiKey := APIKeyFromContext(ctx)
	// A failed request still counts as one request against quota (but we don't
	// charge tokens for requests that never produced a model response).
	s.recordUsage(apiKey, 0, 1)
	if !s.shouldLog() {
		return
	}
	mb := s.cfg.Snapshot().MaxBodyLogBytes
	reqStr := string(reqBody)
	if mb > 0 {
		reqStr = truncate(reqStr, mb)
	}
	var keyID int64
	if apiKey != nil {
		keyID = apiKey.ID
	}
	go func() {
		_ = s.store.InsertRequest(context.Background(), &store.RequestRow{
			Ts:            time.Now(),
			Method:        r.Method,
			Path:          r.URL.Path,
			IncomingModel: inModel,
			TargetModel:   target,
			Stream:        stream,
			Status:        status,
			DurationMs:    dur.Milliseconds(),
			StopReason:    "error",
			Error:         truncate(errMsg, 2048),
			ReqBody:       reqStr,
			APIKeyID:      keyID,
		})
	}()
}

func (s *Server) shouldLog() bool {
	s.cfg.RLock()
	defer s.cfg.RUnlock()
	return s.cfg.LogRequests
}

func stopReasonStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func areqToolsFromBody(body []byte) []proxy.AnthropicTool {
	var req proxy.AnthropicRequest
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	return req.Tools
}

func anthropicToolNamesFromBody(body []byte) []string {
	tools := areqToolsFromBody(body)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func filterUndeclaredToolUses(resp *proxy.AnthropicResponse, tools []proxy.AnthropicTool) []string {
	if resp == nil {
		return nil
	}
	declared := make([]string, 0, len(tools))
	for _, tool := range tools {
		declared = append(declared, tool.Name)
	}
	filtered := resp.Content[:0]
	var removed []string
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			// Same resolution as the streaming paths: OpenCode-namespaced
			// ("default.Bash") and free-tier marker ("shell"/"read") names
			// rewrite to the declared equivalent instead of stalling the
			// agent loop with a text-only end_turn.
			name, ok := proxy.ResolveDeclaredToolName(block.Name, declared)
			if !ok {
				removed = append(removed, block.Name)
				continue
			}
			block.Name = name
		}
		filtered = append(filtered, block)
	}
	resp.Content = filtered
	if len(removed) > 0 && resp.StopReason != nil && *resp.StopReason == "tool_use" {
		stop := "end_turn"
		resp.StopReason = &stop
	}
	return removed
}

// extractOpenAIError pulls the human message out of an OpenAI error envelope.
func extractOpenAIError(b []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ""
	}
	if env.Error.Message != "" {
		return env.Error.Message
	}
	return env.Message
}

func extractAnthropicError(b []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ""
	}
	return env.Error.Message
}

const (
	maxRequestBytes  = 32 << 20 // 32 MiB
	maxResponseBytes = 32 << 20
)
