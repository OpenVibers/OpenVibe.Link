//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// v0.78: a camera command's H.264 is published by ov-desk itself. The core finds ov-desk as the computer plugin does
// (OPENVIBE_DESK_EXE, PATH, beside the Link) and runs `ov-desk publish -whip <url> -- <the command>`; the key goes in the
// child's environment (NativePublisher), never here. OPENVIBE_CAMERA_PUBLISH=0, no ov-desk or no WHIP URL keep the Go
// publisher.
func TestCameraCommandIsPublishedByOvDesk(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ov-desk")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENVIBE_DESK_EXE", exe)
	t.Setenv("PATH", t.TempDir())
	cmd := []string{"rpicam-vid", "-t", "0", "--codec", "h264", "-o", "-"}
	got := cameraPublishArgv(cmd, "https://openre.stream/whip/cam")
	want := []string{exe, "publish", "-whip", "https://openre.stream/whip/cam", "--", "rpicam-vid", "-t", "0", "--codec", "h264", "-o", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv %q, want %q", got, want)
	}
	if cameraPublishArgv(cmd, "") != nil {
		t.Fatal("no WHIP URL, yet ov-desk was asked to publish")
	}
	t.Setenv("OPENVIBE_CAMERA_PUBLISH", "0")
	if cameraPublishArgv(cmd, "https://openre.stream/whip/cam") != nil {
		t.Fatal("OPENVIBE_CAMERA_PUBLISH=0 did not keep the Go publisher")
	}
	t.Setenv("OPENVIBE_CAMERA_PUBLISH", "")
	// Only a file named ov-desk, executable, at an absolute path.
	other := filepath.Join(t.TempDir(), "not-ov-desk")
	_ = os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("OPENVIBE_DESK_EXE", other)
	if got := cameraPublishArgv(cmd, "https://openre.stream/whip/cam"); got != nil && got[0] == other {
		t.Fatalf("a helper not named ov-desk was run: %q", got)
	}
}
