package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"sync"
	"time"

	"gitlabvars/internal/gitlab"
)

const cookieName = "gitlabvars_session"

type session struct {
	Token   string
	CSRF    string
	User    gitlab.User
	Demo    bool
	Expires time.Time
}

type loginAttempt struct {
	Count int
	Until time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	items    map[[32]byte]session
	attempts map[string]loginAttempt
	ttl      time.Duration
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *sessionStore) create(token string, user gitlab.User, demo bool) (string, session, bool) {
	id, err := randomString()
	if err != nil {
		return "", session{}, false
	}
	csrf, err := randomString()
	if err != nil {
		return "", session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	if len(s.items) >= 1024 {
		return "", session{}, false
	}
	v := session{Token: token, CSRF: csrf, User: user, Demo: demo, Expires: now.Add(s.ttl)}
	s.items[sha256.Sum256([]byte(id))] = v
	return id, v, true
}

func (s *sessionStore) get(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) != 43 {
		return session{}, false
	}
	k := sha256.Sum256([]byte(c.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[k]
	if ok && !time.Now().Before(v.Expires) {
		delete(s.items, k)
		return session{}, false
	}
	return v, ok
}

func (s *sessionStore) remove(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.items, sha256.Sum256([]byte(c.Value)))
		s.mu.Unlock()
	}
}

func (s *sessionStore) prune(now time.Time) {
	for k, v := range s.items {
		if !now.Before(v.Expires) {
			delete(s.items, k)
		}
	}
	for k, v := range s.attempts {
		if !now.Before(v.Until) {
			delete(s.attempts, k)
		}
	}
}

// PruneExpiredSessions also clears expired credentials when the server is idle.
func (s *Server) PruneExpiredSessions() {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	s.sessions.prune(time.Now())
}

func (s *sessionStore) allowLogin(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	a, ok := s.attempts[host]
	if !ok {
		if len(s.attempts) >= 4096 {
			return false
		}
		a.Until = now.Add(time.Minute)
	}
	a.Count++
	s.attempts[host] = a
	return a.Count <= 10
}
