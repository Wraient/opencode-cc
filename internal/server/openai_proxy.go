package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Kiowx/opencode-cc/internal/proxy"
)

// OpenAIProxy handles POST /v1/chat/completions. Unlike Proxy, this endpoint
// accepts and returns OpenAI wire format, so OpenAI-compatible SDKs can point
// their base URL at opencode-cc directly.
func (s *Server) OpenAIProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method must be POST")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error",
				"could not read request body: "+err.Error())
			return
		}

		clientStream := openAIStreamRequested(body)
		upBody, incomingModel, targetModel, upstreamStream, err := s.prepareOpenAIRequest(body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}

		// Responses-native models are served ONLY on upstream /v1/responses;
		// the chat path would relay an opaque upstream 500, so fail fast.
		if proxy.IsResponsesNativeModel(targetModel) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error",
				targetModel+" is Responses-API-only; use POST /v1/responses")
			return
		}

		cfg := s.cfg.Snapshot()
		cacheKey := promptCacheKeyFromOpenAIBody(upBody)
		upstream, zenKey, ok := s.cfg.NextUpstreamForKey(cacheKey)
		if !ok {
			const msg = "no upstream API key configured. Set one in the web panel (Settings → upstreams)."
			writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", msg)
			s.logFailed(r.Context(), r, incomingModel, targetModel, clientStream,
				http.StatusUnauthorized, "no upstream api key", body, time.Since(start))
			return
		}

		upURL := strings.TrimRight(upstream, "/") + "/v1/chat/completions"
		upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upURL, bytes.NewReader(upBody))
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "api_error",
				"could not build upstream request: "+err.Error())
			return
		}
		upReq.Header.Set("Authorization", "Bearer "+zenKey)
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("User-Agent", ocUA())
		setZenSessionHeaders(upReq, r.Header, cacheKey)
		if upstreamStream {
			upReq.Header.Set("Accept", "text/event-stream")
		} else {
			upReq.Header.Set("Accept", "application/json")
		}

		httpClient := s.upstreamClient(upstreamStream, cfg.RequestTimeoutSeconds)
		// Retry a stalled/5xx upstream once before failing. Zen free-tier
		// intermittently stalls awaiting response headers (>30s) and 503s
		// under concurrency; without this the client sees a hard 502
		// (reported Oct 2026). Safe here because nothing has been written
		// downstream yet and the POST has no side effects.
		resp, err := doUpstreamWithRetry(httpClient, upReq, upBody)
		if err != nil {
			status := upstreamFailureStatus(w, err)
			writeOpenAIError(w, status, "api_error", "upstream request failed: "+err.Error())
			s.logFailed(r.Context(), r, incomingModel, targetModel, clientStream,
				status, err.Error(), body, time.Since(start))
			return
		}

		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if upstreamStream && resp.StatusCode < http.StatusBadRequest &&
			(contentType == "" || strings.Contains(contentType, "text/event-stream")) {
			if clientStream {
				s.relayOpenAIStream(w, resp, r, incomingModel, targetModel, body, start)
			} else {
				s.relayOpenAIAggregated(w, resp, r, incomingModel, targetModel, body, start)
			}
			return
		}
		s.relayOpenAIJSON(w, resp, r, incomingModel, targetModel, clientStream, body, start)
	}
}

// prepareOpenAIRequest validates the JSON object and rewrites only its model
// field, preserving extensions used by different OpenAI-compatible clients.
func (s *Server) prepareOpenAIRequest(body []byte) ([]byte, string, string, bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", "", false, fmt.Errorf("request body is not valid OpenAI JSON: %w", err)
	}
	if payload == nil {
		return nil, "", "", false, fmt.Errorf("request body must be a JSON object")
	}

	var incomingModel string
	if raw := payload["model"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &incomingModel); err != nil {
			return nil, "", "", false, fmt.Errorf("model must be a string")
		}
	}
	var stream bool
	if raw := payload["stream"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, "", "", false, fmt.Errorf("stream must be a boolean")
		}
	}
	if stream {
		// Zen supports OpenAI's usage trailer. Request it when the client did
		// not specify stream_options so API-key quotas and dashboard stats stay
		// accurate without overriding an explicit client preference.
		if raw, ok := payload["stream_options"]; !ok || string(raw) == "null" {
			payload["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		}
	}

	targetModel := s.cfg.ResolveModel(incomingModel)
	payload["model"], _ = json.Marshal(targetModel)
	proxy.ApplyRawOpenAIPromptCache(payload, promptCacheOptionsFromConfig(s.cfg.Snapshot()))
	upBody, err := json.Marshal(payload)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("could not encode upstream request: %w", err)
	}
	if proxy.IsFreeModel(targetModel) {
		upBody, err = proxy.PrepareFreeChatBody(upBody)
		if err != nil {
			return nil, "", "", false, fmt.Errorf("could not prepare Zen free-tier request: %w", err)
		}
		// Zen's free lane only accepts the official streaming/tool shape. The
		// handler still uses the original body to decide how to answer a
		// non-streaming client; this returned flag describes the upstream hop.
		stream = true
	}
	return upBody, incomingModel, targetModel, stream, nil
}

func openAIStreamRequested(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Stream
}

func filterUndeclaredOpenAITools(out *proxy.OpenAIResponse, reqBody []byte) {
	if out == nil {
		return
	}
	var request struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(reqBody, &request) != nil {
		return
	}
	declared := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Function.Name != "" {
			declared[tool.Function.Name] = struct{}{}
		}
	}
	if len(out.Choices) == 0 || out.Choices[0].Message == nil {
		return
	}
	calls := out.Choices[0].Message.ToolCalls[:0]
	for _, call := range out.Choices[0].Message.ToolCalls {
		if _, ok := declared[call.Function.Name]; ok {
			calls = append(calls, call)
		}
	}
	out.Choices[0].Message.ToolCalls = calls
}

// relayOpenAIAggregated adapts a forced upstream SSE response to the
// non-streaming wire format requested by ordinary OpenAI SDKs. It is used
// only for models whose free-tier gate requires stream:true upstream.
func (s *Server) relayOpenAIAggregated(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	reqBody []byte,
	start time.Time,
) {
	defer resp.Body.Close()
	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "api_error", "could not decompress upstream stream: "+err.Error())
			s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
			return
		}
		defer gz.Close()
		reader = gz
	}
	agg, err := proxy.AggregateOpenAIStream(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "api_error", err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, false, http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	filterUndeclaredOpenAITools(agg, reqBody)
	writeJSON(w, http.StatusOK, agg)
	stopReason := ""
	if len(agg.Choices) > 0 && agg.Choices[0].FinishReason != nil {
		stopReason = *agg.Choices[0].FinishReason
	}
	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, false, http.StatusOK,
		agg.Usage.PromptTokens, agg.Usage.CompletionTokens, agg.Usage.CachedPromptTokens(), 0, stopReason,
		string(reqBody), mustJSON(agg), time.Since(start))
}

func promptCacheKeyFromOpenAIBody(body []byte) string {
	var payload struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.PromptCacheKey)
}

func (s *Server) relayOpenAIJSON(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	stream bool,
	reqBody []byte,
	start time.Time,
) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "api_error", "could not read upstream body: "+err.Error())
		s.logFailed(r.Context(), r, incomingModel, targetModel, stream,
			http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	if len(raw) > maxResponseBytes {
		const msg = "upstream response exceeded the maximum allowed size"
		writeOpenAIError(w, http.StatusBadGateway, "api_error", msg)
		s.logFailed(r.Context(), r, incomingModel, targetModel, stream,
			http.StatusBadGateway, msg, reqBody, time.Since(start))
		return
	}

	copyOpenAIHeaders(w.Header(), resp.Header, false)
	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	// Stamp the cooldown before WriteHeader: headers set afterwards are
	// ignored. copyOpenAIHeaders already relayed an upstream Retry-After.
	if status == http.StatusTooManyRequests {
		s.stampCooldown(w, resp.Header, targetModel)
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)

	if status >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(raw))
		if extracted := extractOpenAIError(raw); extracted != "" {
			msg = extracted
		}
		s.logFailed(r.Context(), r, incomingModel, targetModel, stream,
			status, msg, reqBody, time.Since(start))
		return
	}

	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage proxy.OpenAIUsage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &out)
	stopReason := ""
	if len(out.Choices) > 0 {
		stopReason = out.Choices[0].FinishReason
	}
	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, stream, status,
		out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.CachedPromptTokens(), 0, stopReason,
		string(reqBody), string(raw), time.Since(start))
}

func (s *Server) relayOpenAIStream(
	w http.ResponseWriter,
	resp *http.Response,
	r *http.Request,
	incomingModel, targetModel string,
	reqBody []byte,
	start time.Time,
) {
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "api_error",
			"streaming not supported by this server")
		return
	}

	reader := io.Reader(resp.Body)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "api_error",
				"could not decompress upstream stream: "+err.Error())
			return
		}
		defer gz.Close()
		reader = gz
	}

	copyOpenAIHeaders(w.Header(), resp.Header, true)
	// An upstream 429 relayed verbatim keeps its own Retry-After (copied above);
	// when the upstream sent none, synthesise a real cooldown so the next hop
	// backs off instead of hot-looping.
	if resp.StatusCode == http.StatusTooManyRequests {
		s.stampCooldown(w, resp.Header, targetModel)
	}
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	var responseLog strings.Builder
	// Phantom free-tier marker tools ("shell"/"read") are injected upstream but
	// never declared by this client. Resolve them to the client's real tools so
	// an OpenAI-compatible client never sees a tool it does not have.
	relay := &openAIStreamRelay{
		dst:      w,
		flusher:  flusher,
		log:      &responseLog,
		logLimit: s.cfg.Snapshot().MaxBodyLogBytes,
		filter:   newChatToolCallFilter(declaredOpenAIToolNames(reqBody)),
	}
	if _, err := io.Copy(relay, reader); err != nil {
		errPayload, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": "upstream stream error: " + err.Error(),
				"type":    "api_error",
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", errPayload)
		flusher.Flush()
		s.logFailed(r.Context(), r, incomingModel, targetModel, true,
			http.StatusBadGateway, err.Error(), reqBody, time.Since(start))
		return
	}
	_ = relay.Flush()

	if summary := relay.filter.summary(); summary != "" {
		log.Printf("opencode-cc: chat stream tool filter %s model=%s target=%s",
			summary, incomingModel, targetModel)
	}

	s.logSuccessWithCache(r.Context(), r, incomingModel, targetModel, true, resp.StatusCode,
		relay.inputTokens, relay.outputTokens, relay.cachedInputTokens, 0, relay.stopReason,
		string(reqBody), responseLog.String(), time.Since(start))
}

// openAIStreamRelay writes upstream bytes to the client and flushes every
// write. It re-encodes only tool-bearing SSE data lines (to resolve phantom
// free-tier marker tool names into the client's declared tools); every other
// byte is relayed untouched.
type openAIStreamRelay struct {
	dst      io.Writer
	flusher  http.Flusher
	log      *strings.Builder
	logLimit int
	filter   *chatToolCallFilter

	pending           []byte
	inputTokens       int
	outputTokens      int
	cachedInputTokens int
	stopReason        string
}

func (r *openAIStreamRelay) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	for {
		i := bytes.IndexByte(r.pending, '\n')
		if i < 0 {
			break
		}
		line := r.pending[:i+1]
		r.pending = r.pending[i+1:]
		if err := r.emitLine(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush forwards any trailing bytes that never got a newline terminator.
func (r *openAIStreamRelay) Flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	line := r.pending
	r.pending = nil
	return r.emitLine(line)
}

// emitLine filters and forwards one complete SSE line (newline included).
func (r *openAIStreamRelay) emitLine(line []byte) error {
	body := line
	trailer := []byte("\n")
	if n := len(body); n > 0 && body[n-1] == '\n' {
		body = body[:n-1]
		trailer = line[n-1:]
	}

	out := body
	if r.filter != nil {
		// Rewrite only the JSON payload, keeping the "data:" prefix and the
		// original line terminator. Dropping either would corrupt the SSE
		// framing: the client would ignore the line as a nameless event.
		if data, ok := sseDataPayload(body); ok {
			if filtered, changed := r.filter.filterChatToolCalls(data); changed {
				rewritten := make([]byte, 0, len(body))
				rewritten = append(rewritten, body[:sseDataOffset(body)]...)
				rewritten = append(rewritten, filtered...)
				if rest := body[sseDataOffset(body)+len(data):]; len(rest) > 0 {
					rewritten = append(rewritten, rest...)
				}
				out = rewritten
			}
		}
	}

	if _, err := r.dst.Write(out); err != nil {
		return err
	}
	if _, err := r.dst.Write(trailer); err != nil {
		return err
	}
	appendLimited(r.log, string(out), r.logLimit)
	appendLimited(r.log, string(trailer), r.logLimit)
	r.observe(body)
	r.flusher.Flush()
	return nil
}

// sseDataPayload returns the JSON payload of an SSE "data:" line.
func sseDataPayload(line []byte) ([]byte, bool) {
	start := sseDataOffset(line)
	if start < 0 {
		return nil, false
	}
	end := bytes.IndexByte(line[start:], '\n')
	if end < 0 {
		end = len(line)
	} else {
		end += start
	}
	data := bytes.TrimSpace(bytes.TrimRight(line[start:end], "\r\n"))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, false
	}
	return data, true
}

// sseDataOffset returns the index where an SSE "data:" line's payload begins,
// or -1 when the line is not a data line. The payload is everything after the
// "data:" field name and its single optional space, matching the SSE spec: a
// line that does not start with "data:" is a field-less event line.
func sseDataOffset(line []byte) int {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return -1
	}
	off := len("data:")
	if off < len(line) && line[off] == ' ' {
		off++
	}
	return off
}

func (r *openAIStreamRelay) observe(line []byte) {
	data, ok := sseDataPayload(line)
	if !ok {
		return
	}
	var chunk proxy.OpenAIStreamChunk
	if json.Unmarshal(data, &chunk) != nil {
		return
	}
	if chunk.Usage != nil {
		r.inputTokens = chunk.Usage.PromptTokens
		r.outputTokens = chunk.Usage.CompletionTokens
		r.cachedInputTokens = chunk.Usage.CachedPromptTokens()
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil {
			r.stopReason = *choice.FinishReason
		}
	}
}

func copyOpenAIHeaders(dst, src http.Header, streaming bool) {
	for key, values := range src {
		lower := strings.ToLower(key)
		if isHopByHopHeader(lower) || lower == "content-length" || lower == "content-encoding" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	if dst.Get("Content-Type") == "" {
		if streaming {
			dst.Set("Content-Type", "text/event-stream")
		} else {
			dst.Set("Content-Type", "application/json")
		}
	}
	if streaming {
		dst.Set("Cache-Control", "no-cache")
		dst.Set("X-Accel-Buffering", "no")
	}
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func appendLimited(dst *strings.Builder, value string, limit int) {
	if limit <= 0 {
		_, _ = dst.WriteString(value)
		return
	}
	if dst.Len() >= limit {
		return
	}
	remaining := limit - dst.Len()
	if len(value) > remaining {
		value = value[:remaining]
	}
	_, _ = dst.WriteString(value)
}
