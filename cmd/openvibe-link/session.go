package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The user-session start (Windows): instead of the LocalSystem service, the Node starts when you sign in, in your
// own session, from your Run key (HKCU\Software\Microsoft\Windows\CurrentVersion\Run; no administrator needed). This
// is for drivers that need what only a signed-in session has, such as Bluetooth through the Windows runtime for a
// bridge robot (docs/hardware-looi.md). It runs in a console window that shows the log; closing the window stops the
// Node, and every actuator stops with it (the plugins' heartbeat and stdin rules). Nothing restarts it until the next
// sign-in.

const (
	runKey       = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "OpenVibe Link"
)

// runValueFor is the Run value of one Node directory. A machine may run more than one Node at sign-in (a working
// robot and a test node, 11-02), so each --home has its own value; before v0.37.2 every install wrote the one
// "OpenVibe Link" value, and installing a second home replaced the first's sign-in start.
func runValueFor(home string) string { return runValueName + " (" + home + ")" }

// legacyRunsHome says whether a Run value's command line (the old single "OpenVibe Link" value) starts this home:
// only then is it this Node's to move or remove. A value that starts another home is another Node's and stays.
func legacyRunsHome(data, home string) bool {
	if data == "" || home == "" {
		return false
	}
	return strings.Contains(data, " --home "+quoteArg(home)+" ") || strings.HasSuffix(data, " --home "+quoteArg(home))
}

var errNeedHome = errors.New(`--user-session needs --home DIR: the directory you paired with, for example --home "%LOCALAPPDATA%\OpenVibe Link"`)

// userSessionCommand is the command line the Run key starts: the executable, `run`, and the Node's own directory.
func userSessionCommand(exe, home, logLevel string) string {
	parts := []string{quoteArg(exe), "run", "--home", quoteArg(home)}
	if logLevel != "" && logLevel != "info" {
		parts = append(parts, "--log-level", logLevel)
	}
	return strings.Join(parts, " ")
}

// quoteArg quotes one Windows command-line argument (paths with spaces, such as "OpenVibe Link").
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func cmdInstallUserSession(g *globals, stdout io.Writer) error {
	if err := userSessionSupported(); err != nil {
		return err
	}
	if g.home == "" {
		return errNeedHome
	}
	home, err := filepath.Abs(g.home)
	if err != nil {
		return err
	}
	if err := g.paths.Ensure(); err != nil {
		return fmt.Errorf("cannot create %s: %w", home, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	line := userSessionCommand(exe, home, g.logLevel)
	if err := registerUserSession(runValueFor(home), line); err != nil {
		return err
	}
	// The old single value, if it started this same home, is this Node's: the new value replaces it.
	if data, ok := readUserSession(runValueName); ok && legacyRunsHome(data, home) {
		_ = unregisterUserSession(runValueName)
	}
	fmt.Fprintln(stdout, "The Node now starts when you sign in, in its own window:", line)
	if err := startUserSession(exe, home, g.logLevel); err != nil {
		fmt.Fprintln(stdout, "It did not start now (", err, "); sign out and in, or run the line above.")
	} else {
		fmt.Fprintln(stdout, "Started it now. Closing its window stops the Node and every actuator.")
	}
	fmt.Fprintf(stdout, "Remove the sign-in start with `openvibe-link uninstall --user-session --home %s`.\n", quoteArg(home))
	return nil
}

func cmdUninstallUserSession(g *globals, stdout io.Writer) error {
	if err := userSessionSupported(); err != nil {
		return err
	}
	if g.home == "" {
		return errNeedHome
	}
	home, err := filepath.Abs(g.home)
	if err != nil {
		return err
	}
	if err := unregisterUserSession(runValueFor(home)); err != nil {
		return err
	}
	// An install from before v0.37.2 wrote the single "OpenVibe Link" value; it is removed only if it starts this home.
	if data, ok := readUserSession(runValueName); ok && legacyRunsHome(data, home) {
		if err := unregisterUserSession(runValueName); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "The Node no longer starts at sign-in. Close its window to stop the running one; config and credential are kept.")
	return nil
}

// parseRegQuery finds a value's data in `reg query <key> /v <name>` output, whose line reads
// "    <name>    REG_SZ    <data>". ok is false when the value is not there.
func parseRegQuery(out, name string) (string, bool) {
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, name+" ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, name))
		for _, typ := range []string{"REG_SZ", "REG_EXPAND_SZ"} {
			if strings.HasPrefix(rest, typ) {
				return strings.TrimSpace(strings.TrimPrefix(rest, typ)), true
			}
		}
	}
	return "", false
}
