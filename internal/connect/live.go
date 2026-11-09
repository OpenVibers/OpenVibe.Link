package connect

import (
	"fmt"
	"sort"
	"sync"
)

// Status is one saved share's live state, as `openvibe-link status` (and its JSON) lists it. It never contains the
// key: a share served by this computer is named by its shr_… id, and a key-served share by its port.
type Status struct {
	ID        string `json:"id,omitempty"`
	Port      int    `json:"port"`
	Host      string `json:"host,omitempty"`
	URL       string `json:"url,omitempty"`
	Connected bool   `json:"connected"`
	Requests  uint64 `json:"requests"`
	LastError string `json:"last_error,omitempty"`
}

// live is the state of the shares this process keeps up. The Node service runs every saved share in this process
// (RunAll), and its local status socket answers from here; a foreground `openvibe-link share` has its own process and
// is not listed by the service's status.
var live = struct {
	sync.Mutex
	shares map[string]*Status
}{shares: map[string]*Status{}}

// liveKey is the share's id, or its port for a share served with a connect key.
func liveKey(s Share) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("port:%d", s.Port)
}

// markLive records a share that is about to connect (it stays listed while it reconnects).
func markLive(s Share) {
	live.Lock()
	defer live.Unlock()
	st := live.shares[liveKey(s)]
	if st == nil {
		st = &Status{ID: s.ID, Port: s.Port}
		live.shares[liveKey(s)] = st
	}
	st.Port = s.Port
	st.ID = s.ID
}

// liveReady records the address the server gave and that the share is connected.
func liveReady(s Share, r Ready) {
	live.Lock()
	defer live.Unlock()
	st := live.shares[liveKey(s)]
	if st == nil {
		st = &Status{ID: s.ID, Port: s.Port}
		live.shares[liveKey(s)] = st
	}
	st.Host, st.URL, st.Connected, st.LastError = r.Host, r.URL, true, ""
}

// liveDown records that the share is not connected, and why when it was not the owner stopping it.
func liveDown(s Share, err error) {
	live.Lock()
	defer live.Unlock()
	st := live.shares[liveKey(s)]
	if st == nil {
		return
	}
	st.Connected = false
	if err != nil {
		st.LastError = errString(err)
	}
}

// liveRequest counts one request the share answered (or tried to).
func liveRequest(s Share) {
	live.Lock()
	defer live.Unlock()
	if st := live.shares[liveKey(s)]; st != nil {
		st.Requests++
	}
}

// forgetLive drops a share when its runner ends, so a stopped share is not listed as still being served.
func forgetLive(s Share) {
	live.Lock()
	defer live.Unlock()
	delete(live.shares, liveKey(s))
}

// LiveShares is a snapshot of the shares this process keeps up, by port then id.
func LiveShares() []Status {
	live.Lock()
	defer live.Unlock()
	out := make([]Status, 0, len(live.shares))
	for _, st := range live.shares {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].ID < out[j].ID
	})
	return out
}
