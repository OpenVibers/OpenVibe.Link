//go:build !windows

package agent

import (
	"image"
	"image/color"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/video"
)

// startScreenNode runs the real computer plugin with a fake `grim` (a Wayland session) that copies a real JPEG to the
// file it is given, so the screen stream has frames without a display; video may use loopback.
func startScreenNode(t *testing.T, level string) *env {
	t.Helper()
	bin := t.TempDir()
	pic := filepath.Join(bin, "screen.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 160, 255})
		}
	}
	f, err := os.Create(pic)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// grim [-t jpeg -q N] <out>: the output is the last argument.
	script := "#!/bin/sh\nfor last; do :; done\ncp \"" + pic + "\" \"$last\"\n"
	if err := os.WriteFile(filepath.Join(bin, "grim"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e, _ := startComputerWith(t, level, func(pc *config.PluginConfig, o *Options) {
		pc.Env["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
		pc.Env["WAYLAND_DISPLAY"] = "wayland-test"
		pc.Config["notify"] = false
		o.VideoLoopback = true
	})
	return e
}

func screenWHIP(t *testing.T) (*video.WHIPReceiver, string) {
	t.Helper()
	w := &video.WHIPReceiver{PublishKey: "ork_screen_test_key"}
	srv := httptest.NewServer(w)
	t.Cleanup(func() { w.Close(); srv.Close() })
	return w, srv.URL + "/whip/ork_screen_test_key"
}

func TestScreenStreamPublishesTheScreenAndStops(t *testing.T) {
	e := startScreenNode(t, "observe")
	whip, url := screenWHIP(t)
	r := e.computerCmd("s1", protocol.KindScreenStream, map[string]any{"action": "start", "whip_url": url, "publish_key": "ork_screen_test_key", "seconds": 60, "fps": 5}, 20*time.Second)
	d := dataOf(t, r)
	if d["streaming"] != true || d["seconds"] != float64(60) || d["fps"] != float64(5) {
		t.Fatalf("%+v", d)
	}
	deadline := time.Now().Add(20 * time.Second)
	for whip.Packets.Load() < 10 {
		if time.Now().After(deadline) {
			t.Fatalf("no screen video: %+v", e.node.Status().Screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := e.node.Status().Screen; st.State != video.StateLive || st.Until == "" {
		t.Fatalf("status %+v", st)
	}
	if whip.BadAuth.Load() != 0 {
		t.Fatal("the publish key was not sent as the bearer")
	}
	stop := dataOf(t, e.computerCmd("s2", protocol.KindScreenStream, map[string]any{"action": "stop"}, 10*time.Second))
	if stop["stopped"] != true {
		t.Fatalf("%+v", stop)
	}
	deadline = time.Now().Add(5 * time.Second)
	for e.node.Status().Screen.State != video.StateOff || whip.Deletes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("still streaming after stop: %+v (deletes %d)", e.node.Status().Screen, whip.Deletes.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestScreenStreamEndsOnTheKillSwitchAndIsRefusedWhileLatched(t *testing.T) {
	e := startScreenNode(t, "trusted")
	whip, url := screenWHIP(t)
	start := map[string]any{"action": "start", "whip_url": url, "publish_key": "ork_screen_test_key", "fps": 5}
	dataOf(t, e.computerCmd("k1", protocol.KindScreenStream, start, 20*time.Second))
	deadline := time.Now().Add(20 * time.Second)
	for whip.Packets.Load() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("no screen video: %+v", e.node.Status().Screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := localctl.Call(e.paths.SocketPath(), localctl.Request{Cmd: localctl.CmdStop}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for e.node.Status().Screen.State != video.StateOff {
		if time.Now().After(deadline) {
			t.Fatalf("the kill switch did not end the stream: %+v", e.node.Status().Screen)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if c := nackCode(e.computerCmd("k2", protocol.KindScreenStream, start, 10*time.Second)); c != protocol.FaultLocalStop {
		t.Fatalf("start while latched: %q", c)
	}
}

func TestScreenStreamRefusesBadTargets(t *testing.T) {
	e := startScreenNode(t, "observe")
	for i, v := range []map[string]any{
		{"action": "start", "whip_url": "http://203.0.113.5/whip/k", "publish_key": "k"},
		{"action": "start", "whip_url": "https://user:pw@openre.stream/whip/k", "publish_key": "k"},
		{"action": "start", "whip_url": "https://openre.stream/whip/k"},
		{"action": "watch"},
	} {
		r := e.computerCmd("b"+string(rune('0'+i)), protocol.KindScreenStream, v, 10*time.Second)
		if c := nackCode(r); c != protocol.FaultBadValue {
			t.Fatalf("%v: %q", v, c)
		}
	}
	if st := e.node.Status().Screen.State; st != video.StateOff {
		t.Fatalf("%q", st)
	}
}

// The live screen through ov-desk (Link v0.75): on an X11 session with ov-desk the plugin answers native and the core
// runs `ov-desk stream` itself. Here ov-desk is a fake that records its arguments and plays H.264 from the PCM encoder.
func TestScreenStreamRunsOvDeskOnAnX11Session(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ov-desk captures X11 on Linux; the plugin offers it nowhere else")
	}
	bin := t.TempDir()
	enc, err := video.NewPCMEncoder(64, 48)
	if err != nil {
		t.Fatal(err)
	}
	var stream []byte
	f := video.NewFrame(64, 48)
	for i := 0; i < 30; i++ {
		video.DrawPattern(f, i)
		stream = append(stream, enc.Encode(f, true, nil)...)
	}
	h264 := filepath.Join(bin, "screen.h264")
	if err := os.WriteFile(h264, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(bin, "argv")
	envFile := filepath.Join(bin, "xauth")
	// An ov-desk older than v0.77 knows no `publish` (exit 2): the core falls back to `stream` and its own publisher.
	fake := "#!/bin/sh\n[ \"$1\" = publish ] && exit 2\necho \"$@\" > '" + argvFile + "'\necho \"$XAUTHORITY\" > '" + envFile + "'\n" +
		"n=0\nwhile [ $n -lt 300 ]; do cat '" + h264 + "'; sleep 0.1; n=$((n+1)); done\n"
	exe := filepath.Join(bin, "ov-desk")
	if err := os.WriteFile(exe, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	e, _ := startComputerWith(t, "observe", func(pc *config.PluginConfig, o *Options) {
		pc.Env["DISPLAY"] = ":99"
		pc.Env["XAUTHORITY"] = "/run/user/1000/xauth-test"
		pc.Env["OPENVIBE_DESK_EXE"] = exe
		delete(pc.Env, "WAYLAND_DISPLAY")
		pc.Config["notify"] = false
		o.VideoLoopback = true
	})
	whip, url := screenWHIP(t)
	d := dataOf(t, e.computerCmd("n1", protocol.KindScreenStream, map[string]any{"action": "start", "whip_url": url, "publish_key": "ork_screen_test_key", "seconds": 60, "fps": 2}, 20*time.Second))
	if d["streaming"] != true || d["native"] != true || d["fps"] != float64(ScreenNativeFPS) {
		t.Fatalf("not the native screen: %+v", d)
	}
	deadline := time.Now().Add(20 * time.Second)
	for whip.Packets.Load() < 10 {
		if time.Now().After(deadline) {
			t.Fatalf("no screen video from ov-desk: %+v", e.node.Status().Screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := os.ReadFile(argvFile)
	if want := "stream -display :99 -fps 15 -max-width 1600 -kbps 2500"; string(got) != want+"\n" {
		t.Fatalf("ov-desk ran with %q, want %q", got, want)
	}
	if x, _ := os.ReadFile(envFile); string(x) != "/run/user/1000/xauth-test\n" {
		t.Fatalf("XAUTHORITY %q", x)
	}
	if stop := dataOf(t, e.computerCmd("n2", protocol.KindScreenStream, map[string]any{"action": "stop"}, 10*time.Second)); stop["stopped"] != true {
		t.Fatalf("%+v", stop)
	}
}

func TestScreenNativeRunsOnlyOvDeskOnAnXDisplay(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ov-desk")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "sh")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		n  *screenNative
		ok bool
	}{
		{&screenNative{Exe: exe, Display: ":0"}, true},
		{&screenNative{Exe: exe, Display: "localhost:10.0", XAuthority: "/home/a/.Xauthority"}, true},
		{nil, false},
		{&screenNative{Exe: "ov-desk", Display: ":0"}, false},
		{&screenNative{Exe: other, Display: ":0"}, false},
		{&screenNative{Exe: filepath.Join(dir, "none", "ov-desk"), Display: ":0"}, false},
		{&screenNative{Exe: exe, Display: ":0; rm -rf /"}, false},
		{&screenNative{Exe: exe, Display: ":0", XAuthority: "relative"}, false},
	} {
		argv, _, err := c.n.command(15)
		if (err == nil) != c.ok {
			t.Fatalf("%+v: err %v", c.n, err)
		}
		if c.ok && (argv[0] != exe || argv[1] != "stream") {
			t.Fatalf("argv %q", argv)
		}
	}
}
