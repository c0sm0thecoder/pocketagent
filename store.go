package main

import (
	"encoding/json"
	"os"
	"slices"
	"sync"
)

// ChatState is what we remember per Telegram chat across restarts.
type ChatState struct {
	SessionID   string   `json:"session_id,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Model       string   `json:"model,omitempty"`
	AlwaysAllow []string `json:"always_allow,omitempty"`
	TotalCost   float64  `json:"total_cost_usd,omitempty"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	chats map[int64]*ChatState
}

func openStore(path string) (*Store, error) {
	s := &Store{path: path, chats: map[int64]*ChatState{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, &s.chats)
}

// Get returns a copy of the chat's state.
func (s *Store) Get(chatID int64) ChatState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.chats[chatID]; ok {
		cp := *c
		cp.AlwaysAllow = slices.Clone(c.AlwaysAllow)
		return cp
	}
	return ChatState{}
}

// Update applies fn to the chat's state and persists the result.
func (s *Store) Update(chatID int64, fn func(*ChatState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[chatID]
	if !ok {
		c = &ChatState{}
		s.chats[chatID] = c
	}
	fn(c)
	data, err := json.MarshalIndent(s.chats, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
