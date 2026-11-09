package egress

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Reserve before forwarding bytes. A crash can overcount, never reopen spent bandwidth.
// One Budget belongs to the running Link, shared by all its sessions and both directions.
type Budget struct {
	mu    sync.Mutex
	path  string
	Month string           `json:"month"`
	Used  int64            `json:"used"`
	Now   func() time.Time `json:"-"`
}

func NewBudget(path string) (*Budget, error) {
	b := &Budget{path: path, Now: time.Now}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, b); err != nil || b.Used < 0 {
			return nil, errors.New("browsing usage could not be read")
		}
	}
	return b, nil
}
func (b *Budget) Reserve(n, cap int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	month := b.Now().UTC().Format("2006-01")
	if month > b.Month {
		b.Month, b.Used = month, 0
	} // a clock moving back cannot reset the cap
	if n < 0 || cap < 0 || n > cap-b.Used || (n == 0 && b.Used >= cap) {
		return errors.New("monthly browsing limit reached")
	}
	b.Used += n
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(b.path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(b.path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(b.path+".tmp", b.path)
}
