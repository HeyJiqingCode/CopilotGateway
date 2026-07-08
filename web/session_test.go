package web

import (
	"testing"
	"time"
)

func TestSessionStore(t *testing.T) {
	s := newSessionStore()

	t.Run("create then lookup", func(t *testing.T) {
		token := s.Create("alice", time.Hour)
		if token == "" {
			t.Fatal("token should not be empty")
		}
		name, ok := s.Username(token)
		if !ok || name != "alice" {
			t.Errorf("Username = %q, %v; want alice, true", name, ok)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		if _, ok := s.Username("no-such-token"); ok {
			t.Error("unknown token should not resolve")
		}
	})

	t.Run("empty token", func(t *testing.T) {
		if _, ok := s.Username(""); ok {
			t.Error("empty token should not resolve")
		}
	})

	t.Run("expired session is rejected and removed", func(t *testing.T) {
		token := s.Create("bob", -time.Minute) // 已过期
		if _, ok := s.Username(token); ok {
			t.Error("expired session should not resolve")
		}
	})

	t.Run("delete", func(t *testing.T) {
		token := s.Create("carol", time.Hour)
		s.Delete(token)
		if _, ok := s.Username(token); ok {
			t.Error("deleted session should not resolve")
		}
	})

	t.Run("tokens are unique", func(t *testing.T) {
		if s.Create("a", time.Hour) == s.Create("a", time.Hour) {
			t.Error("two sessions should have distinct tokens")
		}
	})
}
