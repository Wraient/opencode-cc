package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestRespHeadersColumnMigratesAndRoundTrips covers the additive migration that
// adds resp_headers to an existing requests table (the production DB predates
// the column) and the insert/read path for it.
func TestRespHeadersColumnMigratesAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Simulate a database created before resp_headers existed.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts INTEGER NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL,
		incoming_model TEXT NOT NULL, target_model TEXT NOT NULL,
		stream INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0, stop_reason TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '', req_body TEXT NOT NULL DEFAULT '',
		resp_body TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer s.Close()

	const headers = `{"Retry-After":"3600"}`
	if err := s.InsertRequest(context.Background(), &RequestRow{
		Ts:          time.Now(),
		Method:      "POST",
		Path:        "/v1/messages",
		Status:      429,
		StopReason:  "error",
		Error:       "Free usage limit reached",
		RespBody:    `{"error":"limit"}`,
		RespHeaders: headers,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := s.ListRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].RespHeaders != headers || rows[0].RespBody != `{"error":"limit"}` {
		t.Errorf("round trip lost the failed response: %+v", rows[0])
	}

	// Re-opening must not fail on the already-applied ALTERs.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	row, err := s2.GetRequest(context.Background(), rows[0].ID)
	if err != nil || row == nil {
		t.Fatalf("get after reopen: %v (row=%v)", err, row)
	}
	if row.RespHeaders != headers {
		t.Errorf("headers lost after reopen: %q", row.RespHeaders)
	}
}
