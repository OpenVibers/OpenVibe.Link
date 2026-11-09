package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The shares the Node keeps up live in <config>/connect-shares.json, readable only by the Node's own account (0600):
// a connect key lets a computer serve that share, so it is kept like a credential and never logged or printed whole.

// File is the saved shares' file name inside the config directory.
const File = "connect-shares.json"

type saved struct {
	Shares []Share `json:"shares"`
}

// Load reads the saved shares; a missing file is no shares.
func Load(dir string) ([]Share, error) {
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	return s.Shares, nil
}

func save(dir string, shares []Share) error {
	sort.Slice(shares, func(i, j int) bool { return shares[i].Port < shares[j].Port })
	b, err := json.MarshalIndent(saved{Shares: shares}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, File+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, File))
}

// Add saves a share (the same key, or the same share id, again replaces its port and name).
func Add(dir string, s Share) error {
	shares, err := Load(dir)
	if err != nil {
		return err
	}
	out := shares[:0]
	for _, x := range shares {
		if (s.Key == "" || x.Key != s.Key) && (s.ID == "" || x.ID != s.ID) {
			out = append(out, x)
		}
	}
	return save(dir, append(out, s))
}

// Remove forgets the share with this key or id, or every share on this port; it reports how many it removed.
func Remove(dir string, keyOrPort string) (int, error) {
	shares, err := Load(dir)
	if err != nil {
		return 0, err
	}
	out := shares[:0]
	n := 0
	for _, x := range shares {
		if (x.Key != "" && x.Key == keyOrPort) || (x.ID != "" && x.ID == keyOrPort) || fmt.Sprint(x.Port) == keyOrPort || (x.Name != "" && x.Name == keyOrPort) {
			n++
			continue
		}
		out = append(out, x)
	}
	if n == 0 {
		return 0, nil
	}
	return n, save(dir, out)
}

// Redact shows a key's first characters only.
func Redact(key string) string {
	if len(key) <= 10 {
		return "ovc_…"
	}
	return key[:10] + "…"
}

// RunAll keeps every saved share up until ctx ends; a share the server stops is left stopped (its key is wrong, it
// was closed, or another computer took it over). It remembers each share's address as the server names it.
func RunAll(ctx context.Context, dir string, o Options) {
	shares, err := Load(dir)
	if err != nil {
		if o.Log != nil {
			o.Log.Warn("connect: saved shares unreadable", "err", err)
		}
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, s := range shares {
		s := s
		so := o
		so.OnReady = func(r Ready) {
			if s.Name == r.Host {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			s.Name = r.Host
			_ = Add(dir, s)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Run(ctx, s, so)
			if errors.Is(err, ErrStopped) && o.Log != nil {
				o.Log.Warn("connect: share stopped; remove it with `openvibe-link share remove`", "port", s.Port, "share", s.Label())
			}
		}()
	}
	if len(shares) > 0 && o.Log != nil {
		o.Log.Info("connect: keeping shares up", "count", len(shares))
	}
	wg.Wait()
}
