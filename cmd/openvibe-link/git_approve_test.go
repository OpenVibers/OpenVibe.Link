package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/gitrelay"
)

// A clone the page addressed to this computer waits for the same y/N as a link: yes runs it, anything else refuses,
// and an input that ends without an answer refuses too.
func TestCloneApproverReadsY(t *testing.T) {
	root := t.TempDir()
	req := gitrelay.CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo",
		ExpiresAt: time.Now().Add(90 * time.Second), RequestedBy: "sam"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "n\n": false, "\n": false, "": false} {
		var out bytes.Buffer
		approve, _ := cloneApprover(root, false, strings.NewReader(in), &out)
		got, err := approve(context.Background(), req)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got.Approve != want {
			t.Fatalf("%q: approve=%v want %v", in, got.Approve, want)
		}
		if !strings.Contains(out.String(), "openvibers/demo") || !strings.Contains(out.String(), "asked by sam") {
			t.Fatalf("%q: the prompt does not say what it would do: %q", in, out.String())
		}
	}
}

func TestCloneApproverYesAnswersWithoutAsking(t *testing.T) {
	var out bytes.Buffer
	approve, overwrite := cloneApprover(t.TempDir(), true, nil, &out)
	got, err := approve(context.Background(), gitrelay.CloneRequest{Repo: "openvibers/demo"})
	if err != nil || !got.Approve {
		t.Fatalf("approve=%v err=%v", got.Approve, err)
	}
	if filepath.Base(got.Folder) != "demo" {
		t.Fatalf("folder=%q", got.Folder)
	}
	ok, err := overwrite(context.Background(), gitrelay.CloneRequest{Repo: "openvibers/demo"}, "/tmp/demo")
	if err != nil || !ok {
		t.Fatalf("overwrite=%v err=%v", ok, err)
	}
}

func TestCloneApproverWithoutTerminalRefuses(t *testing.T) {
	approve, _ := cloneApprover(t.TempDir(), false, nil, io.Discard)
	got, err := approve(context.Background(), gitrelay.CloneRequest{Repo: "openvibers/demo"})
	if err == nil || got.Approve {
		t.Fatalf("with no terminal: approve=%v err=%v (it must refuse)", got.Approve, err)
	}
	if !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("err=%v", err)
	}
}

func TestCloneApproverRefusesBadRepository(t *testing.T) {
	approve, _ := cloneApprover(t.TempDir(), true, nil, io.Discard)
	if _, err := approve(context.Background(), gitrelay.CloneRequest{Repo: "../etc"}); err == nil {
		t.Fatal("a repository that is not owner/name was accepted")
	}
	if _, err := approve(context.Background(), gitrelay.CloneRequest{Repo: "openvibers/demo"}); err != nil {
		t.Fatalf("valid repository: %v", err)
	}
}
