package main

// Soft session affinity is separate from physical-account admission. A session
// prefers its last executing account, but may move to a free peer in the same
// realm when that account has no slot. Never store a client credential or raw
// prompt: keys are SHA-256 digests and values are physical account identities.
// This is process-local, like the account gate; host routing can still reject
// a request before the executor is invoked.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const sessionBindingTTL = time.Hour
const maxSessionBindings = 4096

type sessionBinding struct {
	account string
	version uint64
	expires time.Time
}

type sessionBindingStore struct {
	mu      sync.Mutex
	entries map[string]sessionBinding
	version uint64
}

var chatSessions = sessionBindingStore{entries: make(map[string]sessionBinding)}

// The client credential scopes affinity, not account capacity. Without a
// client identity, identical first prompts from different callers must not
// accidentally share a binding. X-Client-Request-Id is intentionally ignored:
// it may rotate every turn. The same stable conversation prefix used for the
// upstream CID is the fallback when a client has no explicit session header.
func requestSessionKey(req pluginapi.ExecutorRequest, upstreamBody []byte, realm string) string {
	if len(upstreamBody) == 0 || realm == "" {
		return ""
	}
	client := strings.TrimSpace(req.Headers.Get("Authorization"))
	if client == "" {
		client = strings.TrimSpace(req.Headers.Get("X-Api-Key"))
	}
	if client == "" {
		client = strings.TrimSpace(req.Headers.Get("Api-Key"))
	}
	if client == "" {
		return ""
	}
	session := strings.TrimSpace(req.Headers.Get("X-Session-ID"))
	if session == "" {
		session = strings.TrimSpace(req.Headers.Get("Session-Id"))
	}
	if session == "" {
		var body struct {
			ConversationID string            `json:"conversation_id"`
			Messages       []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(upstreamBody, &body) != nil {
			return ""
		}
		if body.ConversationID != "" {
			session = body.ConversationID
		} else if len(body.Messages) > 0 {
			session = stableConversationID(upstreamBody)
		}
	}
	if session == "" {
		return ""
	}
	model := strings.TrimSpace(req.Model)
	// Model-scoped like the host selector: a conversation may use models with
	// different availability. The executing account still must be in the
	// current realm's eligible candidate pool.
	digest := sha256.Sum256([]byte(realm + "\x00" + model + "\x00" + client + "\x00" + session))
	return hex.EncodeToString(digest[:])
}

func (s *sessionBindingStore) preferred(key string, candidates []*failoverCandidate) []*failoverCandidate {
	if key == "" || len(candidates) < 2 {
		return candidates
	}
	s.mu.Lock()
	entry, ok := s.entries[key]
	if ok && !entry.expires.After(time.Now()) {
		delete(s.entries, key)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return candidates
	}
	for i, cand := range candidates {
		if cand != nil && cand.Auth != nil && accountGateKey(cand) == entry.account {
			if i == 0 {
				return candidates
			}
			ordered := make([]*failoverCandidate, 0, len(candidates))
			ordered = append(ordered, cand)
			ordered = append(ordered, candidates[:i]...)
			ordered = append(ordered, candidates[i+1:]...)
			return ordered
		}
	}
	// Disabled, missing, wrong-realm or otherwise ineligible: the next
	// successful admission will rebind. Never synthesize an auth here.
	return candidates
}

// Bind at slot admission, before the upstream call, so overlapping turns of
// the same session see the same account. A later overflow wins over an older
// in-flight request; its completion cannot flip the binding back.
func (s *sessionBindingStore) begin(key, account string) uint64 {
	if key == "" || account == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]sessionBinding)
	}
	if len(s.entries) >= maxSessionBindings {
		now := time.Now()
		oldestKey := ""
		var oldest time.Time
		for k, entry := range s.entries {
			if !entry.expires.After(now) {
				delete(s.entries, k)
				continue
			}
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = k, entry.expires
			}
		}
		if len(s.entries) >= maxSessionBindings && oldestKey != "" {
			delete(s.entries, oldestKey)
		}
	}
	s.version++
	s.entries[key] = sessionBinding{account: account, version: s.version, expires: time.Now().Add(sessionBindingTTL)}
	return s.version
}

func (s *sessionBindingStore) finish(key string, version uint64, keep bool) {
	if key == "" || version == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.entries[key]
	if !ok || current.version != version {
		return
	}
	if !keep {
		delete(s.entries, key)
		return
	}
	current.expires = time.Now().Add(sessionBindingTTL)
	s.entries[key] = current
}
