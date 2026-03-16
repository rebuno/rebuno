package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type SessionStore struct {
	mu      sync.RWMutex
	sessions map[string]domain.Session // sessionID -> Session
	byExec   map[string]string         // executionID -> sessionID
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]domain.Session),
		byExec:   make(map[string]string),
	}
}

func (s *SessionStore) Create(_ context.Context, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	if session.ExecutionID != "" {
		s.byExec[session.ExecutionID] = session.ID
	}
	return nil
}

func (s *SessionStore) Get(_ context.Context, sessionID string) (*domain.Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, false, nil
	}
	c := sess
	return &c, true, nil
}

func (s *SessionStore) GetByExecution(_ context.Context, executionID string) (*domain.Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byExec[executionID]
	if !ok {
		return nil, false, nil
	}
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false, nil
	}
	c := sess
	return &c, true, nil
}

func (s *SessionStore) Extend(_ context.Context, sessionID string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	sess.ExpiresAt = time.Now().Add(duration)
	s.sessions[sessionID] = sess
	return nil
}

func (s *SessionStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if ok {
		delete(s.byExec, sess.ExecutionID)
		delete(s.sessions, sessionID)
	}
	return nil
}

func (s *SessionStore) DeleteExpired(_ context.Context, gracePeriod time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	count := 0
	for id, sess := range s.sessions {
		if sess.ExpiresAt.Add(gracePeriod).Before(now) {
			delete(s.byExec, sess.ExecutionID)
			delete(s.sessions, id)
			count++
		}
	}
	return count, nil
}
