package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// OpenCodeActiveCredential is an opt-in reference, not a stored bearer token.
const OpenCodeActiveCredential = "opencode://active"

// ResolveUpstreamCredential reads OpenCode v2's active credential on each
// selection. OpenCode alone owns refresh/rotation: never reuse its refresh
// token, write its database, or persist its access token in the relay config.
func ResolveUpstreamCredential(base, configured string) (string, error) {
	if configured != OpenCodeActiveCredential {
		return configured, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() != "opencode.ai" ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil {
		return "", errors.New("active OpenCode credential requires the HTTPS opencode.ai upstream")
	}
	path := os.Getenv("OPENCODE_CC_OPENCODE_DB")
	if path == "" {
		root := os.Getenv("XDG_DATA_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New("cannot locate OpenCode credential database")
			}
			root = filepath.Join(home, ".local", "share")
		}
		path = filepath.Join(root, "opencode", "opencode.db")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid OpenCode credential database path")
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(250)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", errors.New("cannot open OpenCode credential database read-only")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT value FROM credential WHERE integration_id = 'opencode' AND active = 1 LIMIT 2`)
	if err != nil {
		return "", errors.New("cannot read active OpenCode v2 credential")
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return "", errors.New("cannot decode active OpenCode credential")
		}
		values = append(values, value)
	}
	if rows.Err() != nil || len(values) != 1 {
		return "", errors.New("expected one active OpenCode credential; connect OpenCode Console in the CLI")
	}
	var credential struct {
		Type    string `json:"type"`
		Access  string `json:"access"`
		Key     string `json:"key"`
		Expires int64  `json:"expires"`
	}
	if json.Unmarshal([]byte(values[0]), &credential) != nil {
		return "", errors.New("invalid active OpenCode credential")
	}
	switch credential.Type {
	case "oauth":
		if credential.Expires <= time.Now().Add(15*time.Second).UnixMilli() {
			return "", errors.New("OpenCode OAuth access token expired or expiring; let the official CLI renew its connection")
		}
		if credential.Access != "" {
			return credential.Access, nil
		}
	case "key":
		if credential.Key != "" {
			return credential.Key, nil
		}
	}
	return "", errors.New("active OpenCode credential contains no usable bearer token")
}
