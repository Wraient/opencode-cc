package config

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func credentialFixture(t *testing.T) (*sql.DB, func(string, string, int)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native credentials.db")
	t.Setenv("OPENCODE_CC_OPENCODE_DB", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE credential (id TEXT PRIMARY KEY, integration_id TEXT, active INTEGER, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db, func(id, value string, active int) {
		t.Helper()
		if _, err := db.Exec(`INSERT OR REPLACE INTO credential VALUES (?, 'opencode', ?, ?)`, id, active, value); err != nil {
			t.Fatal(err)
		}
	}
}

func oauthFixture(t *testing.T, access string, expires time.Time) string {
	t.Helper()
	value, err := json.Marshal(map[string]any{"type": "oauth", "access": access,
		"refresh": "DO-NOT-USE-REFRESH-FIXTURE", "expires": expires.UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestActiveOpenCodeCredentialTracksRotationWithoutPersistingTokens(t *testing.T) {
	db, set := credentialFixture(t)
	set("old", `{"type":"key","key":"expired-legacy-fixture"}`, 0)
	set("current", oauthFixture(t, "access-one-fixture", time.Now().Add(time.Hour)), 1)
	c := Default()
	c.Upstreams = []Upstream{{BaseURL: "https://opencode.ai/inference", APIKey: OpenCodeActiveCredential, Enabled: true}}
	for _, sticky := range []string{"", "same-session"} {
		_, key, ok := c.NextUpstreamForKey(sticky)
		if !ok || key != "access-one-fixture" {
			t.Fatal("active credential not selected")
		}
	}
	rotated := oauthFixture(t, "access-two-fixture", time.Now().Add(time.Hour))
	set("current", rotated, 1)
	_, key, ok := c.NextUpstream()
	if !ok || key != "access-two-fixture" {
		t.Fatal("rotated credential not picked up on the next request")
	}
	if c.Snapshot().Upstreams[0].APIKey != OpenCodeActiveCredential {
		t.Fatal("resolved token leaked into saved configuration")
	}
	var after string
	if err := db.QueryRow(`SELECT value FROM credential WHERE id = 'current'`).Scan(&after); err != nil || after != rotated {
		t.Fatal("native credential was modified")
	}
	// Legacy single-upstream mode resolves references too.
	c.Upstreams = nil
	c.UpstreamBase, c.ZenAPIKey = "https://opencode.ai/inference", OpenCodeActiveCredential
	if _, key, ok := c.NextUpstream(); !ok || key != "access-two-fixture" {
		t.Fatal("legacy reference did not resolve")
	}
}

func TestActiveOpenCodeCredentialFailsClosed(t *testing.T) {
	_, set := credentialFixture(t)
	c := Default()
	c.Upstreams = []Upstream{{BaseURL: "https://opencode.ai/inference", APIKey: OpenCodeActiveCredential, Enabled: true}}
	c.ZenAPIKey = "must-not-fall-back"
	for _, value := range []string{
		oauthFixture(t, "expired-fixture", time.Now().Add(-time.Minute)),
		oauthFixture(t, "expiring-fixture", time.Now()),
		`not-json`, `{"type":"oauth","expires":9999999999999}`, `{"type":"unknown","key":"fixture"}`,
	} {
		set("current", value, 1)
		if _, key, ok := c.NextUpstreamForKey("session"); ok || key != "" {
			t.Fatal("invalid active credential was used or legacy fallback was selected")
		}
	}
	set("current", `{"type":"key","key":"current-api-key-fixture"}`, 1)
	if _, key, ok := c.NextUpstream(); !ok || key != "current-api-key-fixture" {
		t.Fatal("native active API-key credential not supported")
	}
	set("duplicate", `{"type":"key","key":"ambiguous-fixture"}`, 1)
	if _, _, ok := c.NextUpstream(); ok {
		t.Fatal("ambiguous native credentials should not be selected")
	}
}

func TestActiveOpenCodeCredentialMissingDatabaseIsNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	t.Setenv("OPENCODE_CC_OPENCODE_DB", path)
	if _, err := ResolveUpstreamCredential("https://opencode.ai/inference", OpenCodeActiveCredential); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only lookup created a database")
	}
}

func TestActiveOpenCodeCredentialRestrictedToOfficialHTTPSOrigin(t *testing.T) {
	for _, base := range []string{"http://opencode.ai/inference", "https://other.example", "https://opencode.ai.attacker.example", "https://opencode.ai:444", "https://user@opencode.ai"} {
		if _, err := ResolveUpstreamCredential(base, OpenCodeActiveCredential); err == nil {
			t.Fatalf("native credential reference accepted for %s", base)
		}
	}
	if key, err := ResolveUpstreamCredential("https://custom.example", "literal-key-fixture"); err != nil || key != "literal-key-fixture" {
		t.Fatal("ordinary configured API key behavior changed")
	}
}
