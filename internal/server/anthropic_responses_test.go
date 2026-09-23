package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kiowx/opencode-cc/internal/config"
)

// mockResponsesZen pretends to be Zen's /v1/responses for a Responses-native
// model. It captures the request body for assertions and answers with a text
// plus function_call response, either as JSON or as an SSE stream.
func mockResponsesZen(t *testing.T, stream bool, gotBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		*gotBody = body
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"id":"resp_test","object":"response","status":"completed",
				"model":"muse-spark-1.3-contributor-free",
				"output":[
					{"id":"msg_1","type":"message","status":"completed","role":"assistant",
					 "content":[{"type":"output_text","text":"the time","annotations":[]}]},
					{"id":"fc_1","type":"function_call","status":"completed",
					 "call_id":"call_7","name":"get_time","arguments":"{}"}
				],
				"usage":{"input_tokens":11,"output_tokens":6,"total_tokens":17,
					"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{}}
			}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		frames := [][2]string{
			{"response.output_text.delta", `{"item_id":"a","delta":"the time"}`},
			{"response.output_item.added", `{"item_id":"b","item":{"id":"fc_1","type":"function_call","call_id":"call_7","name":"get_time"}}`},
			{"response.function_call_arguments.delta", `{"item_id":"b","delta":"{}"}`},
			{"response.output_item.done", `{"item_id":"b","item":{"id":"fc_1","type":"function_call","call_id":"call_7","name":"get_time","arguments":"{}"}}`},
			{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":11,"output_tokens":6}}}`},
		}
		for _, f := range frames {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f[0], f[1])
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func responsesBridgeTestServer(t *testing.T, zenURL string) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.UpstreamBase = strings.TrimRight(zenURL, "/")
	cfg.ZenAPIKey = "test-key"
	cfg.NativeAnthropic = false
	cfg.ModelMappings = []config.ModelMapping{{Match: "*", Target: "muse-spark-1.3-contributor-free"}}
	srv, _ := newTestServerWithCfg(t, cfg)
	return srv
}

func bridgeAnthropicBody(t *testing.T, stream bool) []byte {
	t.Helper()
	req := map[string]any{
		"model":      "client-model",
		"max_tokens": 128,
		"system":     "You are concise.",
		"messages":   []map[string]any{{"role": "user", "content": "what time is it"}},
		"tools": []map[string]any{{
			"name": "get_time", "description": "current time",
			"input_schema": map[string]any{"type": "object"},
		}},
		"stream": stream,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func TestAnthropicViaResponsesNonStream(t *testing.T) {
	var gotBody []byte
	zen := mockResponsesZen(t, false, &gotBody)
	defer zen.Close()
	srv := responsesBridgeTestServer(t, zen.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bridgeAnthropicBody(t, false)))
	srv.Proxy().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var upstream map[string]any
	if err := json.Unmarshal(gotBody, &upstream); err != nil {
		t.Fatalf("upstream body decode: %v", err)
	}
	if upstream["model"] != "muse-spark-1.3-contributor-free" {
		t.Errorf("upstream model not rewritten: %v", upstream["model"])
	}
	if upstream["tool_choice"] != "auto" || upstream["store"] != false {
		t.Errorf("upstream defaults: tool_choice=%v store=%v", upstream["tool_choice"], upstream["store"])
	}
	input, _ := upstream["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("upstream input empty: %s", gotBody)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["type"] != "message" || resp["role"] != "assistant" {
		t.Errorf("envelope: %v", resp)
	}
	content, _ := resp["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected text + tool_use, got %v", resp["content"])
	}
	tool, _ := content[1].(map[string]any)
	if tool["type"] != "tool_use" || tool["id"] != "call_7" || tool["name"] != "get_time" {
		t.Errorf("tool_use block: %v", tool)
	}
	if resp["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason: %v", resp["stop_reason"])
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 11 || usage["output_tokens"].(float64) != 6 {
		t.Errorf("usage: %v", usage)
	}
}

func TestAnthropicViaResponsesStream(t *testing.T) {
	var gotBody []byte
	zen := mockResponsesZen(t, true, &gotBody)
	defer zen.Close()
	srv := responsesBridgeTestServer(t, zen.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bridgeAnthropicBody(t, true)))
	srv.Proxy().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		`"type":"tool_use"`,
		`"name":"get_time"`,
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
}

// TestAnthropicStreamEmitsMessageStartFirst pins the TTFB fix: the bridge must
// send the client message_start as soon as upstream connects (response.created),
// not wait for the first convertible content event (Claude Code treats long
// initial silence as a stall). The mock stalls 3s after response.created; the
// client must still see message_start in well under that.
func TestAnthropicStreamEmitsMessageStartFirst(t *testing.T) {
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", `{"response":{"id":"resp_x"}}`)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(3 * time.Second) // upstream reasoning silence
		for _, f := range [][2]string{
			{"response.output_text.delta", `{"item_id":"a","delta":"hi"}}`},
			{"response.completed", `{"response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":3}}}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f[0], f[1])
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer zen.Close()
	srv := responsesBridgeTestServer(t, zen.URL)
	proxySrv := httptest.NewServer(srv.Proxy())
	defer proxySrv.Close()

	start := time.Now()
	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json",
		bytes.NewReader(bridgeAnthropicBody(t, true)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	// Read client events until the first complete one; it must be message_start
	// and it must arrive before upstream's delayed content.
	reader := bufio.NewReader(resp.Body)
	var firstEvent strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading first event: %v", err)
		}
		firstEvent.WriteString(line)
		if line == "\n" {
			break
		}
	}
	elapsed := time.Since(start)
	if !strings.Contains(firstEvent.String(), "event: message_start") {
		t.Fatalf("first client event must be message_start, got:\n%s", firstEvent.String())
	}
	if elapsed >= 2*time.Second {
		t.Errorf("message_start took %s; must precede delayed upstream content", elapsed.Round(time.Millisecond))
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the handler can finish
}

// TestBridgeThinkingMapsToModelMax pins the effort range end to end:
// ultrathink-class budgets and unknown effort names (e.g. "ultracode")
// reach upstream as the model's real maximum (xhigh for muse-spark),
// while plain turns stay on the fast minimal default.
func TestBridgeThinkingMapsToModelMax(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		seen = append(seen, body.Reasoning.Effort)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_e","object":"response","status":"completed","model":"m",
			"output":[{"id":"a","type":"message","status":"completed","role":"assistant",
				"content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,
				"input_tokens_details":{},"output_tokens_details":{}}}`)
	}))
	defer zen.Close()
	srv := responsesBridgeTestServer(t, zen.URL)

	post := func(thinking string) {
		t.Helper()
		body := `{"model":"client-model","max_tokens":128,"stream":false,` + thinking +
			`"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		srv.Proxy().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	post(`"thinking":{"type":"enabled","budget_tokens":31999},`)
	post(`"thinking":{"type":"enabled","effort":"ultracode"},`)
	post(``) // no thinking: configured xhigh default

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(seen, []string{"xhigh", "xhigh", "xhigh"}) {
		t.Errorf("upstream efforts = %v, want [xhigh xhigh xhigh]", seen)
	}
}
