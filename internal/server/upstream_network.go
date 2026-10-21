package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Only connection-establishment failures from the configured proxy qualify.
// Never reclassify an HTTP auth/quota response, proxy permission failure, or
// a failure after response headers have been delivered as a safe replay.
func isProxyConnectivityFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "socks connect") && !strings.Contains(message, "proxyconnect") {
		return false
	}
	for _, permanent := range []string{"authentication", "username", "password", "not allowed", "certificate"} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	for _, temporary := range []string{"host unreachable", "host is unreachable", "network is unreachable",
		"network unreachable", "no route to host", "connection refused", "connection reset",
		"general socks server failure", "timeout", "eof"} {
		if strings.Contains(message, temporary) {
			return true
		}
	}
	return false
}

// A dead tunnel is a transient service failure, not an invalid credential.
// CPA honors Retry-After for 503s, avoiding its default one-minute credential
// cooldown after the proxy path recovers. Real auth/quota responses bypass this.
func upstreamFailureStatus(w http.ResponseWriter, err error) int {
	if isProxyConnectivityFailure(err) {
		w.Header().Set("Retry-After", "2")
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}
