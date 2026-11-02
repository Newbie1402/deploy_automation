package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

const sessionCookieName = "deploy_session"
const sessionLifetime = 12 * time.Hour

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authSession struct {
	username string
	expires  time.Time
}

type sessionManager struct {
	loginUsername string
	password      string
	mu            sync.Mutex
	sessions      map[string]authSession
}

func newSessionManager(username, password string) *sessionManager {
	return &sessionManager{loginUsername: username, password: password, sessions: make(map[string]authSession)}
}

func (s *sessionManager) matches(username, password string) bool {
	return subtle.ConstantTimeCompare([]byte(username), []byte(s.loginUsername)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(s.password)) == 1
}

func (s *sessionManager) create(username string) (string, time.Time, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", time.Time{}, err
	}
	id := hex.EncodeToString(value)
	expires := time.Now().Add(sessionLifetime)
	s.mu.Lock()
	for key, session := range s.sessions {
		if time.Now().After(session.expires) {
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = authSession{username: username, expires: expires}
	s.mu.Unlock()
	return id, expires, nil
}

func (s *sessionManager) username(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return "", false
	}
	if time.Now().After(session.expires) {
		delete(s.sessions, id)
		return "", false
	}
	return session.username, true
}

func (s *sessionManager) valid(id string) bool {
	_, ok := s.username(id)
	return ok
}

func (s *sessionManager) remove(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}
