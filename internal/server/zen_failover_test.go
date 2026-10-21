package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type zenFailoverTransport func(*http.Request) (*http.Response, error)

func (f zenFailoverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const unavailableZenEndpoint = `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`

func zenTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func zenTestRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer fixture-"+t.Name())
	setZenSessionHeaders(r, http.Header{}, t.Name())
	return r
}

func TestZenUnavailableEndpointRecoversAndKeepsAffinity(t *testing.T) {
	r := zenTestRequest(t)
	original := r.Header.Get(zenHeaderSession)
	body := []byte(`{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"hi"}]}`)
	var recovered string
	calls := 0
	client := &http.Client{Transport: zenFailoverTransport(func(up *http.Request) (*http.Response, error) {
		calls++
		got, _ := io.ReadAll(up.Body)
		if !bytes.Equal(got, body) {
			t.Errorf("retry changed request body: %s", got)
		}
		if up.Header.Get("Authorization") != r.Header.Get("Authorization") {
			t.Error("changed credential")
		}
		session := up.Header.Get(zenHeaderSession)
		for _, h := range []string{zenHeaderAffinity, zenHeaderSessionID, zenHeaderActualSession} {
			if up.Header.Get(h) != session {
				t.Errorf("inconsistent %s", h)
			}
		}
		if session == original {
			return zenTestResponse(503, unavailableZenEndpoint), nil
		}
		if !isForwardableSession(session, time.Now().UnixMilli()) {
			t.Errorf("invalid alternate session: %s", session)
		}
		if recovered != "" && recovered != session {
			t.Error("tool follow-up lost recovered affinity")
		}
		recovered = session
		return zenTestResponse(200, "ok"), nil
	})}
	for i := 0; i < 2; i++ {
		resp, err := doUpstreamWithRetry(client, r, body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls != 3 {
		t.Fatalf("calls = %d; expected initial failure, recovery, stable follow-up", calls)
	}
	if r.Header.Get(zenHeaderSession) != original {
		t.Error("mutated downstream request template")
	}
	key := zenAffinityKey(r, body)
	other := r.Clone(t.Context())
	other.Header.Set("Authorization", "Bearer another-credential")
	if cachedZenAffinity(zenAffinityKey(other, body)) != "" {
		t.Error("shared routing across credentials")
	}
	if cachedZenAffinity(zenAffinityKey(r, []byte(`{"model":"another-model"}`))) != "" {
		t.Error("shared routing across models")
	}
	rememberZenAffinity(key, "")
}

func TestZenUnavailableEndpointRecoveryIsBounded(t *testing.T) {
	r := zenTestRequest(t)
	calls := 0
	seen := map[string]bool{}
	client := &http.Client{Transport: zenFailoverTransport(func(up *http.Request) (*http.Response, error) {
		calls++
		seen[up.Header.Get(zenHeaderSession)] = true
		return zenTestResponse(503, unavailableZenEndpoint), nil
	})}
	_, err := doUpstreamWithRetry(client, r, []byte(`{"model":"deepseek-v4.1-flash-free"}`))
	if err == nil || calls != 4 || len(seen) != 4 {
		t.Fatalf("calls=%d distinct=%d err=%v", calls, len(seen), err)
	}
}

func TestZenRecoveryDoesNotRetryAuthorizationOrQuota(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		r := zenTestRequest(t)
		calls := 0
		client := &http.Client{Transport: zenFailoverTransport(func(up *http.Request) (*http.Response, error) {
			calls++
			return zenTestResponse(status, unavailableZenEndpoint), nil
		})}
		resp, err := doUpstreamWithRetry(client, r, []byte(`{"model":"deepseek-v4.1-flash-free"}`))
		if err != nil || calls != 1 || resp.StatusCode != status {
			t.Fatalf("status %d calls=%d err=%v", status, calls, err)
		}
		resp.Body.Close()
	}
}

func TestZenRecoveryLeavesOtherOriginsAndErrorsAlone(t *testing.T) {
	for _, customOrigin := range []bool{false, true} {
		r := zenTestRequest(t)
		if customOrigin {
			r.URL.Host = "custom.example"
		}
		original := r.Header.Get(zenHeaderSession)
		calls := 0
		client := &http.Client{Transport: zenFailoverTransport(func(up *http.Request) (*http.Response, error) {
			calls++
			if up.Header.Get(zenHeaderSession) != original {
				t.Error("changed unrelated affinity")
			}
			if customOrigin {
				return zenTestResponse(503, unavailableZenEndpoint), nil
			}
			return zenTestResponse(503, `{"error":{"type":"server_error","message":"busy"}}`), nil
		})}
		_, err := doUpstreamWithRetry(client, r, []byte(`{"model":"deepseek-v4.1-flash-free"}`))
		if err == nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}

func TestZenRecoveryPreservesChildAndCancelsBackoff(t *testing.T) {
	r := zenTestRequest(t)
	r.Header.Set(zenHeaderActualSession, "distinct-child")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.Clone(ctx)
	calls := 0
	client := &http.Client{Transport: zenFailoverTransport(func(up *http.Request) (*http.Response, error) {
		calls++
		if up.Header.Get(zenHeaderActualSession) != "distinct-child" {
			t.Error("changed actual child session")
		}
		cancel()
		return zenTestResponse(503, unavailableZenEndpoint), nil
	})}
	_, err := doUpstreamWithRetry(client, r, []byte(`{"model":"deepseek-v4.1-flash-free"}`))
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
