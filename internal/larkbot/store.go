package larkbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	stateVersion       = 1
	processedRetention = 7 * 24 * time.Hour
)

type stateData struct {
	Version   int                  `json:"version"`
	Sessions  map[string]string    `json:"sessions"`
	Processed map[string]time.Time `json:"processed"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	state stateData
	now   func() time.Time
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("Lark state path is required")
	}

	store := &Store{
		path: filepath.Clean(path),
		state: stateData{
			Version:   stateVersion,
			Sessions:  make(map[string]string),
			Processed: make(map[string]time.Time),
		},
		now: time.Now,
	}

	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Lark state: %w", err)
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("parse Lark state: %w", err)
	}
	if store.state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported Lark state version %d", store.state.Version)
	}
	if store.state.Sessions == nil {
		store.state.Sessions = make(map[string]string)
	}
	if store.state.Processed == nil {
		store.state.Processed = make(map[string]time.Time)
	}
	store.pruneLocked()
	return store, nil
}

func (s *Store) Session(threadKey string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Sessions[threadKey]
}

func (s *Store) Processed(messageID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	processedAt, ok := s.state.Processed[messageID]
	return ok && s.now().Sub(processedAt) <= processedRetention
}

func (s *Store) Complete(messageID, threadKey, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if messageID != "" {
		s.state.Processed[messageID] = s.now().UTC()
	}
	if threadKey != "" && sessionID != "" {
		s.state.Sessions[threadKey] = sessionID
	}
	s.pruneLocked()
	return s.saveLocked()
}

func (s *Store) pruneLocked() {
	cutoff := s.now().Add(-processedRetention)
	for messageID, processedAt := range s.state.Processed {
		if processedAt.Before(cutoff) {
			delete(s.state.Processed, messageID)
		}
	}
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Lark state directory: %w", err)
	}

	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Lark state: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(dir, ".ai-review-lark-state-*")
	if err != nil {
		return fmt.Errorf("create temporary Lark state: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("protect temporary Lark state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary Lark state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary Lark state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary Lark state: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace Lark state: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("protect Lark state: %w", err)
	}
	return nil
}
