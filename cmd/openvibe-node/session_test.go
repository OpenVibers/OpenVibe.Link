package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestUserSessionCommand(t *testing.T) {
	got := userSessionCommand(`C:\Program Files\OpenVibe\openvibe-node.exe`, `C:\Users\m\AppData\Local\OpenVibe Node`, "info")
	want := `"C:\Program Files\OpenVibe\openvibe-node.exe" run --home "C:\Users\m\AppData\Local\OpenVibe Node"`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := userSessionCommand(`C:\bin\node.exe`, `D:\ov`, "debug"); got != `C:\bin\node.exe run --home D:\ov --log-level debug` {
		t.Fatalf("plain paths and the log level: %s", got)
	}
}

func TestUserSessionRefusedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the refusal is for other systems")
	}
	var out, errb bytes.Buffer
	if code := run([]string{"install", "--user-session", "--home", t.TempDir()}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "for Windows") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

// Each Node directory has its own sign-in start (v0.37.2): a test node beside a working robot's Node no longer replaces
// its Run value, and the old single value is this Node's only when it starts this same home.
func TestUserSessionPerHome(t *testing.T) {
	cozmo, test := `C:\Users\m\AppData\Local\OpenVibe Node`, `D:\looi-test`
	if a, b := runValueFor(cozmo), runValueFor(test); a == b || !strings.HasPrefix(a, "OpenVibe Node (") {
		t.Fatalf("one value per home: %q %q", a, b)
	}
	legacy := userSessionCommand(`C:\bin\openvibe-node.exe`, cozmo, "info")
	if !legacyRunsHome(legacy, cozmo) {
		t.Fatal("the old value starts the Cozmo home")
	}
	if legacyRunsHome(legacy, test) {
		t.Fatal("the old value is another Node's: never this test home's")
	}
	if legacyRunsHome(userSessionCommand(`C:\bin\n.exe`, `D:\ov2`, "debug"), `D:\ov`) {
		t.Fatal(`D:\ov is not D:\ov2`)
	}
	if !legacyRunsHome(userSessionCommand(`C:\bin\n.exe`, `D:\ov`, "debug"), `D:\ov`) {
		t.Fatal("with a log level after it, the home still matches")
	}
}

func TestParseRegQuery(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Run\r\n    OpenVibe Node    REG_SZ    \"C:\\bin\\openvibe-node.exe\" run --home \"C:\\Users\\m\\OpenVibe Node\"\r\n\r\n"
	got, ok := parseRegQuery(out, "OpenVibe Node")
	if !ok || got != `"C:\bin\openvibe-node.exe" run --home "C:\Users\m\OpenVibe Node"` {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := parseRegQuery(out, "OpenVibe Node (D:\\x)"); ok {
		t.Fatal("another value is not found")
	}
}
