package connect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// `openvibe-node status` lists the shares the service keeps up: their port, the address Connect gave, whether the
// tunnel is up, how many requests the app answered, and why it last went down.
func TestLiveSharesForStatus(t *testing.T) {
	s := Share{ID: "shr_01K9ZZZZZZZZZZZZZZZZZZZZZZ", Port: 8790, Key: "ovc_hidden"}
	markLive(s)
	if got := LiveShares(); len(got) != 1 || got[0].Port != 8790 || got[0].Connected {
		t.Fatalf("marked: %+v", got)
	}
	liveReady(s, Ready{Host: "agents--alex.openvibe.website", URL: "https://agents--alex.openvibe.website"})
	liveRequest(s)
	liveRequest(s)
	got := LiveShares()
	if len(got) != 1 || !got[0].Connected || got[0].Host != "agents--alex.openvibe.website" ||
		got[0].URL != "https://agents--alex.openvibe.website" || got[0].Requests != 2 || got[0].LastError != "" {
		t.Fatalf("ready: %+v", got)
	}
	if got[0].ID != s.ID {
		t.Fatalf("the share id is listed: %+v", got)
	}
	liveDown(s, errors.New("dial: connection refused"))
	got = LiveShares()
	if got[0].Connected || got[0].LastError != "dial: connection refused" || got[0].Requests != 2 {
		t.Fatalf("down: %+v", got)
	}
	forgetLive(s)
	if got := LiveShares(); len(got) != 0 {
		t.Fatalf("forgotten: %+v", got)
	}
}

// A key-served share is listed by its port, and its key is never part of the status.
func TestLiveSharesHidesTheKey(t *testing.T) {
	s := Share{Key: "ovc_0123456789012345678901234567890123456789", Port: 9001}
	markLive(s)
	got := LiveShares()
	if len(got) != 1 || got[0].ID != "" || got[0].Port != 9001 {
		t.Fatalf("listed: %+v", got)
	}
	if got[0].URL != "" || got[0].Host != "" {
		t.Fatalf("nothing to show yet: %+v", got)
	}
	forgetLive(s)
}

// The service's shares are listed while it runs and stop when its context ends: `openvibe-node status` is the service
// process, and ending the Node ends every share it keeps up.
func TestRunAllListsAndStopsWithTheNode(t *testing.T) {
	f := newFakeServer(t, 0)
	dir := t.TempDir()
	if err := Add(dir, Share{Key: "ovc_" + strings.Repeat("L", 43), Port: 8790, Server: f.url()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunAll(ctx, dir, Options{})
		close(done)
	}()
	select {
	case <-f.conns:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("the share did not connect")
	}
	var got []Status
	for i := 0; i < 150; i++ {
		got = LiveShares()
		if len(got) == 1 && got[0].Connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 || !got[0].Connected || got[0].Port != 8790 {
		t.Fatalf("listed while running: %+v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunAll did not return when the Node stopped")
	}
	if got := LiveShares(); len(got) != 0 {
		t.Fatalf("not listed after the Node stops: %+v", got)
	}
}
