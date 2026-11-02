//go:build windows

package plugins

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A plugin's own children must not outlive it on Windows: TerminateProcess reaches the direct child only, so the
// core puts the plugin in a Job Object and kills the whole tree. The fake plugin (the test binary, mode "jobtree")
// starts a grandchild and then ignores stdin, so the core has to kill it after StopGrace instead of the plugin
// exiting by itself on EOF.
func TestPluginKillEndsTheWholeTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	fx := start(t, "jobtree", "OPENVIBE_FAKE_GRANDCHILD_PIDFILE="+pidFile)
	fx.waitState(StateReady)
	pid := waitGrandchildPID(t, pidFile)
	fx.stop()
	deadline := time.Now().Add(2 * time.Second)
	for !processGone(uint32(pid)) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d is still running 2 s after the plugin was killed", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitGrandchildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the plugin never wrote the grandchild's pid to %s", path)
	return 0
}

// processGone reports whether the process has exited (or its process object is gone). os.FindProcess always
// succeeds on Windows, so the test opens the pid: a terminated process whose handles are closed is no longer
// openable, and one that still exists is waited on without blocking.
func processGone(pid uint32) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, 0)
	return err == nil && event == windows.WAIT_OBJECT_0
}
