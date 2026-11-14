package deskdisplay

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// App is one program a desktop opens after its display, as [name, argv] in the plugin's apps JSON.
type App struct {
	Name string
	Argv []string
}

// ParseApps reads the plugin's apps JSON: [["wm", ["/usr/bin/openbox"]], ["browser", [...]], ...].
func ParseApps(raw string) ([]App, error) {
	var list [][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("apps: %w", err)
	}
	out := make([]App, 0, len(list))
	for _, pair := range list {
		if len(pair) != 2 {
			return nil, errors.New("apps: each is [name, argv]")
		}
		var a App
		if err := json.Unmarshal(pair[0], &a.Name); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(pair[1], &a.Argv); err != nil || len(a.Argv) == 0 {
			return nil, errors.New("apps: argv is a list of strings")
		}
		out = append(out, a)
	}
	return out, nil
}

// Serve runs a desktop inside the desk's sandbox (Link v0.66; the Python helper in sandbox_desk.py is its fallback):
// it starts Xvfb, then each app with its output in HOME/.logs, then answers the plugin one JSON line per request on in
// and out (the sandbox's fds 3 and 4): shot (a JPEG made here), xdotool (done in-process, or by xdotool itself where
// it is not), logs, stop. It returns when stop is asked, the pipe closes or the display ends.
func Serve(width, height int, apps []App, in io.Reader, out io.Writer) error {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp/desk"
	}
	env := []string{"DISPLAY=:0", "HOME=" + home, "TMPDIR=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm-256color",
		"SHELL=" + shell(), "DBUS_SESSION_BUS_ADDRESS=disabled:", "XDG_RUNTIME_DIR=" + home + "/run", "NO_AT_BRIDGE=1"}
	_ = os.MkdirAll(home+"/run", 0o700)
	logs := filepath.Join(home, ".logs")
	_ = os.MkdirAll(logs, 0o700)
	// Xvfb is not root in here, so it will not make its socket's directory itself.
	_ = os.MkdirAll("/tmp/.X11-unix", 0o1777)
	_ = os.Chmod("/tmp/.X11-unix", 0o1777)

	var mu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		_, _ = out.Write(append(b, '\n'))
	}
	start := func(name string, argv []string) (*proc, error) {
		f, err := os.OpenFile(filepath.Join(logs, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		c := exec.Command(argv[0], argv[1:]...)
		c.Env, c.Dir, c.Stdout, c.Stderr = env, home, f, f
		if err := c.Start(); err != nil {
			f.Close()
			return nil, err
		}
		p := &proc{name: name, cmd: c, done: make(chan struct{})}
		go func() { _ = c.Wait(); f.Close(); close(p.done) }()
		return p, nil
	}
	var procs []*proc
	end := func() {
		for i := len(procs) - 1; i >= 0; i-- {
			_ = procs[i].cmd.Process.Signal(os.Interrupt)
		}
		time.Sleep(300 * time.Millisecond)
		for _, p := range procs {
			if p.alive() {
				_ = p.cmd.Process.Kill()
			}
		}
	}
	defer end()

	x, err := start("display", []string{"Xvfb", ":0", "-screen", "0", fmt.Sprintf("%dx%dx24", width, height), "-nolisten", "tcp", "-noreset"})
	if err != nil {
		write(map[string]any{"ready": false, "error": "the display did not start: " + err.Error()})
		return err
	}
	procs = append(procs, x)
	var d *Display
	for i := 0; i < 150; i++ {
		if _, err := os.Stat("/tmp/.X11-unix/X0"); err == nil {
			if d, err = Open(":0"); err == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if d == nil {
		write(map[string]any{"ready": false, "error": "the display did not start"})
		return errors.New("the display did not start")
	}
	defer d.Close()
	var started []string
	for _, a := range apps {
		c, err := start(a.Name, a.Argv)
		if err != nil {
			continue
		}
		procs = append(procs, c)
		started = append(started, a.Name)
		if a.Name == "wm" {
			time.Sleep(300 * time.Millisecond)
		}
	}
	write(map[string]any{"ready": true, "apps": started, "native": true})

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var req struct {
			ID       any      `json:"id"`
			Op       string   `json:"op"`
			Argv     []string `json:"argv"`
			MaxWidth int      `json:"max_width"`
			MaxBytes int      `json:"max_bytes"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		reply := map[string]any{"id": req.ID}
		switch req.Op {
		case "shot":
			pic, w, h, err := d.Shot(req.MaxWidth, req.MaxBytes)
			if err != nil {
				reply["ok"], reply["err"] = false, err.Error()
			} else {
				reply["ok"], reply["jpeg"], reply["width"], reply["height"] = true, base64.StdEncoding.EncodeToString(pic), w, h
			}
		case "xdotool":
			o, err := d.Xdotool(req.Argv)
			if errors.Is(err, ErrFallback) {
				o, err = forkXdotool(req.Argv, env)
			}
			reply["ok"], reply["out"] = err == nil, o
			if err != nil {
				reply["err"] = clipErr(err.Error() + " (" + trimmed(req.Argv) + ")")
			}
		case "logs":
			tails := map[string]string{}
			names, _ := filepath.Glob(filepath.Join(logs, "*.log"))
			sort.Strings(names)
			for _, n := range names[:min(8, len(names))] {
				tails[strings.TrimSuffix(filepath.Base(n), ".log")] = tail(n, 2048)
			}
			running := map[string]bool{}
			for _, p := range procs {
				running[p.name] = p.alive()
			}
			reply["ok"], reply["logs"], reply["running"] = true, tails, running
		case "stop":
			reply["ok"] = true
			write(reply)
			return nil
		default:
			reply["ok"], reply["err"] = false, "unknown op"
		}
		write(reply)
		if !x.alive() {
			write(map[string]any{"ended": true})
			return errors.New("the display ended")
		}
	}
	return sc.Err()
}

// proc is one program the desktop started; done closes when it ends.
type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// forkXdotool runs the real xdotool for what is not done in-process (a character with no key, an unknown verb).
func forkXdotool(argv []string, env []string) (string, error) {
	if len(argv) == 0 || argv[0] != "xdotool" {
		return "", errors.New("xdotool and its arguments")
	}
	path, err := exec.LookPath("xdotool")
	if err != nil {
		return "", errors.New("this desktop has no xdotool for that input")
	}
	c := exec.Command(path, argv[1:]...)
	c.Env = env
	b, err := c.Output()
	return string(b), err
}

func shell() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

func tail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(0, st.Size()-n)
	b := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(b, off)
	return string(b)
}

func clipErr(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
