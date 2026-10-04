// Package store persists per-conversation state as a JSON file.
package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"time"
)

// Conversation is one chat, or one forum topic in a chat.
type Conversation struct {
	Agent       string   `json:"agent,omitempty"`
	Model       string   `json:"model,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Project     string   `json:"project,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	AlwaysAllow []string `json:"always_allow,omitempty"`
	Voice       bool     `json:"voice,omitempty"`

	TotalCost float64 `json:"total_cost_usd,omitempty"`

	Sessions    []SessionRecord `json:"sessions,omitempty"`
	Checkpoints []Checkpoint    `json:"checkpoints,omitempty"`
}

// SessionRecord remembers a past session so /sessions can resume it.
type SessionRecord struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Cwd     string    `json:"cwd"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
}

// Checkpoint is a git snapshot taken before a turn.
type Checkpoint struct {
	Cwd    string    `json:"cwd"`
	Commit string    `json:"commit"`
	Prompt string    `json:"prompt"`
	At     time.Time `json:"at"`
}

const (
	maxSessions    = 15
	maxCheckpoints = 20
)

// RecordSession moves id to the front of the recent sessions list.
func (c *Conversation) RecordSession(r SessionRecord) {
	c.Sessions = slices.DeleteFunc(c.Sessions, func(s SessionRecord) bool { return s.ID == r.ID })
	c.Sessions = append([]SessionRecord{r}, c.Sessions...)
	if len(c.Sessions) > maxSessions {
		c.Sessions = c.Sessions[:maxSessions]
	}
}

func (c *Conversation) PushCheckpoint(cp Checkpoint) {
	c.Checkpoints = append(c.Checkpoints, cp)
	if len(c.Checkpoints) > maxCheckpoints {
		c.Checkpoints = c.Checkpoints[len(c.Checkpoints)-maxCheckpoints:]
	}
}

// Project is a project added from chat. Projects from the config file are
// not stored here.
type Project struct {
	Cwd   string    `json:"cwd"`
	Added time.Time `json:"added"`
}

type state struct {
	Conversations map[string]*Conversation      `json:"conversations"`
	Spend         map[string]map[string]float64 `json:"spend"` // user id -> day -> USD
	Projects      map[string]Project            `json:"projects,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string
	s    state
}

func Open(path string) (*Store, error) {
	st := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &st.s); err != nil {
			return nil, err
		}
	}
	if st.s.Conversations == nil {
		st.s.Conversations = map[string]*Conversation{}
	}
	if st.s.Spend == nil {
		st.s.Spend = map[string]map[string]float64{}
	}
	if st.s.Projects == nil {
		st.s.Projects = map[string]Project{}
	}
	return st, nil
}

// Get returns a deep copy of the conversation.
func (st *Store) Get(key string) Conversation {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, ok := st.s.Conversations[key]
	if !ok {
		return Conversation{}
	}
	cp := *c
	cp.AlwaysAllow = slices.Clone(c.AlwaysAllow)
	cp.Sessions = slices.Clone(c.Sessions)
	cp.Checkpoints = slices.Clone(c.Checkpoints)
	return cp
}

// Update applies fn to the conversation and saves.
func (st *Store) Update(key string, fn func(*Conversation)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, ok := st.s.Conversations[key]
	if !ok {
		c = &Conversation{}
		st.s.Conversations[key] = c
	}
	fn(c)
	return st.save()
}

func day(t time.Time) string { return t.Format("2006-01-02") }

// AddSpend records USD spent by a user today.
func (st *Store) AddSpend(user string, usd float64) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	m := st.s.Spend[user]
	if m == nil {
		m = map[string]float64{}
		st.s.Spend[user] = m
	}
	m[day(time.Now())] += usd
	// Keep 31 days.
	cutoff := day(time.Now().AddDate(0, 0, -31))
	for d := range m {
		if d < cutoff {
			delete(m, d)
		}
	}
	return st.save()
}

// Spend returns what a user spent today and over the last 30 days.
func (st *Store) Spend(user string) (today, month float64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := day(time.Now().AddDate(0, 0, -30))
	for d, v := range st.s.Spend[user] {
		if d == day(time.Now()) {
			today = v
		}
		if d >= cutoff {
			month += v
		}
	}
	return today, month
}

// Projects returns a copy of the projects added from chat.
func (st *Store) Projects() map[string]Project {
	st.mu.Lock()
	defer st.mu.Unlock()
	return maps.Clone(st.s.Projects)
}

// AddProject stores a project, failing if the name is taken.
func (st *Store) AddProject(name string, p Project) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.s.Projects[name]; ok {
		return fmt.Errorf("a project named %q already exists", name)
	}
	st.s.Projects[name] = p
	return st.save()
}

// RemoveProject deletes a stored project, failing if there is none.
func (st *Store) RemoveProject(name string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.s.Projects[name]; !ok {
		return fmt.Errorf("no project named %q was added from chat", name)
	}
	delete(st.s.Projects, name)
	return st.save()
}

func (st *Store) save() error {
	data, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}
