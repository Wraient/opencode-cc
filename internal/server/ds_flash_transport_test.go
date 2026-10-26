package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDSFlashTransportExactScopeAndCredential(t *testing.T) {
	calls, baseCalls := 0, 0
	helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Path != "/v1/chat/completions" {
			t.Error("lost credential or wrong helper endpoint")
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"messages"`) {
			t.Error("lost request body")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: reply\n\n")
	}))
	defer helper.Close()
	base := zenFailoverTransport(func(r *http.Request) (*http.Response, error) { baseCalls++; return zenTestResponse(200, "base"), nil })
	tr := newDSFlashTransport(base, helper.URL)
	for _, tc := range []struct {
		host, model string
		local       bool
	}{{"opencode.ai", "deepseek-v4.1-flash-free", true}, {"opencode.ai", "deepseek-v4.1-flash", false}, {"example.com", "deepseek-v4.1-flash-free", false}} {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", "https://"+tc.host+"/inference/v1/chat/completions", strings.NewReader(`{"model":"`+tc.model+`","messages":[]}`))
		req.Header.Set("Authorization", "Bearer fixture")
		original := req.URL.String()
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.URL.String() != original {
			t.Error("mutated original HTTPS origin")
		}
	}
	if calls != 1 || baseCalls != 2 {
		t.Fatalf("helper=%d base=%d", calls, baseCalls)
	}
}

func TestDSFlashTransportRejectsRemoteOrCredentialedHelper(t *testing.T) {
	base := zenFailoverTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("DS must not silently bypass invalid configuration")
		return nil, nil
	})
	for _, address := range []string{"https://127.0.0.1:1234", "http://example.com:1234", "http://user:secret@127.0.0.1:1234", "http://127.0.0.1:1234/private"} {
		tr := newDSFlashTransport(base, address)
		req, _ := http.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4.1-flash-free"}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("accepted unsafe helper origin")
		}
	}
}
