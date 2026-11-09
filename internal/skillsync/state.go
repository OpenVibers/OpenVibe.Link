package skillsync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// State is what the person has turned on, kept on this machine only. It holds
// paths and choices, never a skill's text.
type State struct {
	// Version is 1.
	Version int `json:"version"`
	// Tools is the per-tool opt-in and what was written for it.
	Tools map[string]*ToolState `json:"tools,omitempty"`
}

// ToolState is one tool's opt-in state.
type ToolState struct {
	// Enabled is the person's choice for this tool. Off unless they said so.
	Enabled bool `json:"enabled,omitempty"`
	// Files are the files the tool's blocks were written into.
	Files map[string]*FileState `json:"files,omitempty"`
}

// FileState is what the Link needs to undo one file.
type FileState struct {
	// Confirmed is true once the person has seen the diff and said yes.
	Confirmed bool `json:"confirmed,omitempty"`
	// Created is true when the Link created the file.
	Created bool `json:"created,omitempty"`
	// Sep is the separator the Link added before a block in a file that
	// already had text (or "" when it added none). Undo removes exactly it.
	Sep string `json:"sep,omitempty"`
	// Kind is "skill" or "rules".
	Kind string `json:"kind,omitempty"`
	// Slug is the skill's folder name for a skill file.
	Slug string `json:"slug,omitempty"`
	// Frontmatter is the header the Link wrote when it created a skill file;
	// undo uses it to tell a file that held only our content from one that did
	// not.
	Frontmatter string `json:"frontmatter,omitempty"`
}

// NewState is an empty state.
func NewState() *State { return &State{Version: 1, Tools: map[string]*ToolState{}} }

// LoadState reads the state file. A missing file is an empty state, never an
// error: the first run has nothing turned on.
func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return nil, err
	}
	st := NewState()
	if err := json.Unmarshal(raw, st); err != nil {
		return nil, err
	}
	if st.Tools == nil {
		st.Tools = map[string]*ToolState{}
	}
	if st.Version == 0 {
		st.Version = 1
	}
	return st, nil
}

// Save writes the state with owner-only permissions, through a temporary file
// in the same directory so a crash never leaves half a state.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skills-sync-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Rename(name, path)
}

// Tool gives one tool's state, creating it when it is new.
func (s *State) Tool(id string) *ToolState {
	if s.Tools == nil {
		s.Tools = map[string]*ToolState{}
	}
	t := s.Tools[id]
	if t == nil {
		t = &ToolState{Files: map[string]*FileState{}}
		s.Tools[id] = t
	}
	if t.Files == nil {
		t.Files = map[string]*FileState{}
	}
	return t
}

// File gives one file's state, creating it when it is new.
func (s *State) File(tool, path string) *FileState {
	t := s.Tool(tool)
	f := t.Files[path]
	if f == nil {
		f = &FileState{}
		t.Files[path] = f
	}
	return f
}

// FileStateOf gives one file's state without creating it.
func (s *State) FileStateOf(tool, path string) *FileState {
	t := s.Tools[tool]
	if t == nil {
		return nil
	}
	return t.Files[path]
}

// EnabledTools lists the tools the person turned on, in the fixed tool order.
func (s *State) EnabledTools() []string {
	out := []string{}
	for _, id := range ToolIDs() {
		if t := s.Tools[id]; t != nil && t.Enabled {
			out = append(out, id)
		}
	}
	return out
}
