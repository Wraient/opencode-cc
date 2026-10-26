package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// dsFlashTransport is opt-in and only carries the exact Zen free model. Keep
// credential resolution on the original HTTPS origin; the local helper never
// reads the OpenCode credential database or chooses another account/model.
type dsFlashTransport struct {
	base     http.RoundTripper
	local    http.RoundTripper
	endpoint *url.URL
	err      error
}

func newDSFlashTransport(base http.RoundTripper, address string) http.RoundTripper {
	if address == "" {
		return base
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return &dsFlashTransport{base: base, err: fmt.Errorf("DS Flash fetch endpoint must be a loopback HTTP origin")}
	}
	u.Path = "/v1/chat/completions"
	return &dsFlashTransport{base: base, endpoint: u, local: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 32, ResponseHeaderTimeout: 90 * time.Second, IdleConnTimeout: 90 * time.Second,
	}}
}

func (t *dsFlashTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.Scheme != "https" || r.URL.Host != "opencode.ai" ||
		!(r.URL.Path == "/inference/v1/chat/completions" || r.URL.Path == "/inference/openai/v1/chat/completions" || r.URL.Path == "/zen/v1/chat/completions") || r.GetBody == nil {
		return t.base.RoundTrip(r)
	}
	body, err := r.GetBody()
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Model string `json:"model"`
	}
	err = json.NewDecoder(io.LimitReader(body, maxRequestBytes)).Decode(&envelope)
	body.Close()
	if err != nil || strings.TrimSpace(envelope.Model) != "deepseek-v4.1-flash-free" {
		return t.base.RoundTrip(r)
	}
	if t.err != nil {
		return nil, t.err
	}
	local := r.Clone(r.Context())
	u := *t.endpoint
	local.URL, local.Host = &u, u.Host
	resp, err := t.local.RoundTrip(local)
	if resp != nil {
		resp.Request = r
	}
	return resp, err
}
