package access

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Task is the local projection of agent.task@1. Link keeps the original
// sender, person and thread visible to the receiving agent.
type Task struct {
	ID          string `json:"id"`
	To          string `json:"to"`
	From        string `json:"from"`
	OnBehalfOf  string `json:"on_behalf_of"`
	Thread      string `json:"thread"`
	FromMachine string `json:"from_machine,omitempty"`
	Message     struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"message"`
	CreatedAt string `json:"created_at,omitempty"`
}

type InboxEntry struct {
	Task       Task      `json:"task"`
	ReceivedAt time.Time `json:"received_at"`
	Status     string    `json:"status"`
	Reply      string    `json:"reply,omitempty"`
}

type Inbox struct {
	path    string
	now     func() time.Time
	mu      sync.Mutex
	entries []InboxEntry
}

func OpenInbox(path string) (*Inbox, error) {
	i := &Inbox{path: path, now: time.Now}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return i, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &i.entries) != nil {
		return nil, errors.New("agent inbox state is invalid")
	}
	i.prune()
	return i, nil
}

func (i *Inbox) prune() {
	now := i.now()
	out := i.entries[:0]
	for _, entry := range i.entries {
		if now.Sub(entry.ReceivedAt) < 24*time.Hour && now.Sub(entry.ReceivedAt) > -time.Minute {
			out = append(out, entry)
		}
	}
	i.entries = out
}

func (i *Inbox) save() error {
	raw, err := json.Marshal(i.entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(i.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(i.path), ".agent-inbox-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), i.path)
}

func (i *Inbox) Add(t Task) error {
	if t.ID == "" || t.To == "" || t.From == "" || t.OnBehalfOf == "" || t.Thread == "" || len(t.Message.Parts) == 0 || len(t.Message.Parts) > 20 {
		return errors.New("invalid agent task")
	}
	for _, p := range t.Message.Parts {
		if p.Type != "text" || len(p.Text) > 8000 {
			return errors.New("invalid agent task text")
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.prune()
	for _, entry := range i.entries {
		if entry.Task.ID == t.ID {
			return nil
		}
	}
	if len(i.entries) >= 100 {
		return errors.New("agent inbox is full")
	}
	i.entries = append(i.entries, InboxEntry{Task: t, ReceivedAt: i.now(), Status: "pending"})
	return i.save()
}

func (i *Inbox) List(agent string) []InboxEntry {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.prune()
	out := make([]InboxEntry, 0, len(i.entries))
	for _, entry := range i.entries {
		if agent == "" || entry.Task.To == agent {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ReceivedAt.Before(out[b].ReceivedAt) })
	return out
}

func (i *Inbox) Reply(id, text string) (InboxEntry, error) {
	if id == "" || len(text) > 8000 {
		return InboxEntry{}, errors.New("invalid agent reply")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.prune()
	for n := range i.entries {
		if i.entries[n].Task.ID != id {
			continue
		}
		if i.entries[n].Status != "pending" {
			return InboxEntry{}, errors.New("agent task already answered")
		}
		i.entries[n].Status = "completed"
		i.entries[n].Reply = text
		if err := i.save(); err != nil {
			return InboxEntry{}, err
		}
		return i.entries[n], nil
	}
	return InboxEntry{}, errors.New("agent task not in this inbox")
}

func NewTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "tsk_" + hex.EncodeToString(b)
}

func NewRelayID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "mrl_" + hex.EncodeToString(b)
}

func NewThreadID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "thr_" + hex.EncodeToString(b)
}
