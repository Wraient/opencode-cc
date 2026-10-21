package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const zenFailoverLimit = 3 // alternative affinities, beyond the original attempt

type zenAffinityEntry struct {
	session string
	at      time.Time
}

// Keep recovered routing stable across tool turns, without persisting tokens
// or sharing an affinity across credentials, models, or upstream origins.
var zenAffinities = struct {
	sync.Mutex
	entries map[string]zenAffinityEntry
}{entries: make(map[string]zenAffinityEntry)}

func zenAffinityKey(req *http.Request, body []byte) string {
	if req.URL.Scheme != "https" || req.URL.Hostname() != "opencode.ai" || req.Header.Get(zenHeaderSession) == "" {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(req.Header.Get("Authorization") + "\x00" + req.URL.Host + "\x00" + payload.Model + "\x00" + req.Header.Get(zenHeaderSession)))
	return hex.EncodeToString(sum[:])
}

func cachedZenAffinity(key string) string {
	if key == "" {
		return ""
	}
	zenAffinities.Lock()
	defer zenAffinities.Unlock()
	if entry, ok := zenAffinities.entries[key]; ok {
		if time.Since(entry.at) < time.Hour {
			return entry.session
		}
		delete(zenAffinities.entries, key)
	}
	return ""
}

func rememberZenAffinity(key, session string) {
	if key == "" {
		return
	}
	zenAffinities.Lock()
	defer zenAffinities.Unlock()
	if session == "" {
		delete(zenAffinities.entries, key)
		return
	}
	if len(zenAffinities.entries) >= 512 {
		var oldest string
		var at time.Time
		for k, entry := range zenAffinities.entries {
			if oldest == "" || entry.at.Before(at) {
				oldest, at = k, entry.at
			}
		}
		delete(zenAffinities.entries, oldest)
	}
	zenAffinities.entries[key] = zenAffinityEntry{session, time.Now()}
}

func newZenAffinity() string {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "" // retain the normal retry policy if entropy is unavailable
	}
	return canonicalSessionFromHash(seed, time.Now().UnixMilli())
}

func applyZenAffinity(req *http.Request, session string) {
	if session == "" {
		return
	}
	previous := req.Header.Get(zenHeaderSession)
	for _, header := range []string{zenHeaderSession, zenHeaderAffinity, zenHeaderSessionID} {
		req.Header.Set(header, session)
	}
	// Preserve a distinct actual child session supplied by the client.
	if actual := req.Header.Get(zenHeaderActualSession); actual == previous {
		req.Header.Set(zenHeaderActualSession, session)
	}
}

func zenEndpointUnavailable(status int, body []byte) bool {
	if status != http.StatusServiceUnavailable {
		return false
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Error.Type == "server_error" &&
		strings.TrimSpace(payload.Error.Message) == "Upstream request failed: Endpoint is unavailable."
}
