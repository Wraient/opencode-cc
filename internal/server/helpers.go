// Package server wires the HTTP layer: routes, auth, upstream proxying with
// streaming, and request logging. The panel API lives in package api.
package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// writeJSON writes v as JSON with the given status. Shared by handlers in this
// package (proxy has its own copy to avoid a cycle).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAnthropicError writes an Anthropic-shaped error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": msg},
	})
}

// writeOpenAIError writes an OpenAI-compatible error response.
func writeOpenAIError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
			"param":   nil,
			"code":    nil,
		},
	})
}

func (s *Server) upstreamClient(stream bool, timeoutSeconds int) *http.Client {
	if stream || timeoutSeconds <= 0 {
		return s.httpClient
	}
	// Reuse the hardened shared transport (H1-only, header timeout) so
	// timeout clients don't silently bypass it via http.DefaultTransport.
	return &http.Client{Transport: s.httpClient.Transport, Timeout: time.Duration(timeoutSeconds) * time.Second}
}
