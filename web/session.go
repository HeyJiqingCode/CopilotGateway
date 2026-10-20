// Package web 提供模型信息页:登录会话、静态页面与数据端点。
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"sync"
	"time"
)

// sessionStore 是进程内会话表(token → 用户名+过期时间),重启即失效。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
}

type sessionEntry struct {
	username  string
	expiresAt time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]sessionEntry)}
}

// Create 生成随机会话 token 并登记,顺带清理已过期会话保持表有界。
func (s *sessionStore) Create(username string, ttl time.Duration) string {
	token := rand.Text() + rand.Text() // 2×128bit 随机文本,足够会话用途
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for t, e := range s.sessions {
		if !e.expiresAt.After(now) {
			delete(s.sessions, t)
		}
	}
	s.sessions[token] = sessionEntry{username: username, expiresAt: now.Add(ttl)}
	return token
}

// Username 由 token 解析用户名;过期会话顺带删除。
func (s *sessionStore) Username(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[token]
	if !ok {
		return "", false
	}
	if !e.expiresAt.After(time.Now()) {
		delete(s.sessions, token)
		return "", false
	}
	return e.username, true
}

// Delete 删除一个会话(登出);token 不存在时无操作。
func (s *sessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// constantTimeEquals 常量时间字符串比较,防时序侧信道。
func constantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
