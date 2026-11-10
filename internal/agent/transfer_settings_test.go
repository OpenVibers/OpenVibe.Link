package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Clipboard sync and auto-accept are off on a machine that never turned them on, and a capability or first-run
// change never switches them on by accident.
func TestClipboardAndAutoAcceptStartOffAndPersist(t *testing.T) {
	dir := t.TempDir()
	if clipboard, autoAccept := loadPanelSettings(dir); clipboard || autoAccept {
		t.Fatalf("a fresh machine has clipboard=%v auto_accept=%v, want both off", clipboard, autoAccept)
	}
	if err := savePanelSettings(dir, true, true); err != nil {
		t.Fatal(err)
	}
	if clipboard, autoAccept := loadPanelSettings(dir); !clipboard || !autoAccept {
		t.Fatalf("the person's switches were not kept: clipboard=%v auto_accept=%v", clipboard, autoAccept)
	}
	// A capability or first-run save rewrites the same file and must keep the transfer switches.
	if err := savePanel(dir, map[string]bool{"terminal": true}, &panelFirstRun{Answered: true}); err != nil {
		t.Fatal(err)
	}
	if clipboard, autoAccept := loadPanelSettings(dir); !clipboard || !autoAccept {
		t.Fatalf("a capability save lost the transfer switches: clipboard=%v auto_accept=%v", clipboard, autoAccept)
	}
	info, err := os.Stat(filepath.Join(dir, panelFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("panel.json mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dir, panelFile))
	if err != nil {
		t.Fatal(err)
	}
	var saved panelSaved
	if err := json.Unmarshal(raw, &saved); err != nil || !saved.Clipboard || !saved.AutoAccept {
		t.Fatalf("saved switches: %+v (%v)", saved, err)
	}
}
