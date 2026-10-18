package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// net/http caches proxy environment settings process-wide, so exercise the
// production transport in a child rather than depending on test order.
func TestUpstreamProxyEnvironment(t *testing.T) {
	if os.Getenv("OPENCODE_CC_PROXY_TEST_CHILD") == "1" {
		s := New(nil, nil)
		s.httpClient.Timeout = 5 * time.Second
		defer s.httpClient.CloseIdleConnections()
		for _, tc := range []struct{ url, want string }{
			{"http://upstream.invalid/completion", "proxied"},
			{os.Getenv("OPENCODE_CC_PROXY_TEST_LOCAL_URL"), "direct"},
		} {
			resp, err := s.httpClient.Get(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || string(body) != tc.want {
				t.Fatalf("request %s: status=%d body=%q err=%v", tc.url, resp.StatusCode, body, err)
			}
		}
		return
	}
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "upstream.invalid" {
			t.Errorf("unexpected proxy destination: %s", r.URL.Host)
		}
		io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "direct")
	}))
	defer local.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestUpstreamProxyEnvironment$")
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "REQUEST_METHOD":
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "OPENCODE_CC_PROXY_TEST_CHILD=1",
		"OPENCODE_CC_PROXY_TEST_LOCAL_URL="+local.URL, "HTTP_PROXY="+proxy.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proxy test child: %v\n%s", err, output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("proxy received %d requests; want only the non-loopback request", got)
	}
}
