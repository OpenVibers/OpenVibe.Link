"""The desk stream's privacy curtain (Agent Streams S1).

The curtain is a state, not a button: while it is up, no captured frame reaches the publisher and the desk reports
``{"up": true, "reason": …}`` to OpenVibe.Live, which pauses every viewer's consumer as well. A detector answers with a
reason when it sees something private, with ``None`` when it is clear, and may raise when it cannot tell; **a detector
that raises, or that is unsure, puts the curtain up and never down** (the design's fail-closed rule). The curtain comes
down only when every detector has been clear for two seconds, or when the owner lowers it.

Detectors:
  * ``password_field`` — the focused accessible element is a password field (AT-SPI on Linux, UI Automation on Windows,
    the AX API on macOS; the probe is injected so each platform, and the tests, supply their own);
  * ``secret_file`` — the focused window's title or class looks secret, or a secret-named file was just read;
  * ``env_print`` — an ``exec`` printed environment values or ran ``env``/``printenv``;
  * ``manual`` — the person pressed the hotkey or chose Curtain now;
  * ``site`` — a browser desk navigated outside its allow-list;
  * ``detector`` — a detector failed or could not decide.

The hotkey is Ctrl+Alt+P by default on X11 (XGrabKey through ctypes, no extra dependency). ``SIGUSR1`` toggles the
curtain too, which is what a service can be given without a display. Neither the reason nor a detector's error text is
allowed to carry anything a viewer sees: viewers only ever see "Private screen".
"""
from __future__ import annotations

import ctypes
import os
import re
import signal
import subprocess
import sys
import threading
import time
from urllib.parse import urlparse

CURTAIN_CLEAR_HOLD_S = 2.0
CURTAIN_POLL_S = 0.25
# How long a one-off signal (a secret file read, an environment print) keeps the curtain up once it is seen.
CURTAIN_EVENT_HOLD_S = 10.0

# A path or window title that looks secret. `.env`, `*credentials*`, a private key, PEMs and password managers.
SECRET_NAME = re.compile(
    r"(^|[/\\])(\.env(\.[A-Za-z0-9_-]+)?|[^/\\]*credentials[^/\\]*|id_rsa|id_dsa|id_ecdsa|id_ed25519|"
    r"[^/\\]*\.(pem|key|p12|pfx|kdbx|ovpn)|[^/\\]*secret[^/\\]*)$",
    re.IGNORECASE)
SECRET_WINDOW = re.compile(
    r"(keepass|1password|bitwarden|lastpass|dashlane|nordpass|password|credential|secret|\benv\b)", re.IGNORECASE)

REASONS = ("password_field", "secret_file", "env_print", "manual", "site", "detector")

# A line from `env`/`printenv`, or an exported assignment. Three or more in one output is an environment print.
ENV_LINE = re.compile(r"^[A-Z_][A-Z0-9_]{2,}=.*$")


def looks_secret_name(name):
    return bool(name) and bool(SECRET_NAME.search(name))


def looks_secret_window(title, klass=""):
    return bool(title and SECRET_WINDOW.search(title)) or bool(klass and SECRET_WINDOW.search(klass))


class Detector:
    """A curtain detector. poll() returns None when clear, a reason when it sees something private, and may raise."""
    reason = "detector"

    def poll(self):
        return None


class PasswordFieldDetector(Detector):
    reason = "password_field"

    def __init__(self, probe=None):
        self.probe = probe or _password_field_focused

    def poll(self):
        v = self.probe()
        if v is None:
            # Unsure is not clear.
            return self.reason
        return self.reason if v else None


class SecretFileDetector(Detector):
    reason = "secret_file"

    def __init__(self, window=None, clock=time.monotonic):
        self.window = window or _active_window
        self.clock = clock
        self.until = 0.0

    def saw_path(self, name):
        if looks_secret_name(name):
            self.until = max(self.until, self.clock() + CURTAIN_EVENT_HOLD_S)

    def poll(self):
        title, klass = self.window()
        if looks_secret_window(title, klass):
            return self.reason
        if self.clock() < self.until:
            return self.reason
        return None


class EnvPrintDetector(Detector):
    reason = "env_print"

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.until = 0.0

    def saw_output(self, argv, output):
        """The exec wrapper calls this with what the command printed, before the output leaves the machine."""
        if not output:
            return
        cmd = os.path.basename(argv[0]) if argv else ""
        if cmd in ("env", "printenv") or any(a in ("-0", "--null") for a in argv):
            self.until = max(self.until, self.clock() + CURTAIN_EVENT_HOLD_S)
            return
        hits = 0
        for line in output.splitlines():
            if ENV_LINE.match(line):
                hits += 1
                if hits >= 3:
                    self.until = max(self.until, self.clock() + CURTAIN_EVENT_HOLD_S)
                    return

    def poll(self):
        return self.reason if self.clock() < self.until else None


class SiteDetector(Detector):
    reason = "site"

    def __init__(self, allow_sites=None, current_url=None):
        self.allow = [u for u in (allow_sites or []) if u]
        self.current_url = current_url
        self.last = None

    def allows(self, url):
        if not self.allow:
            return True
        host = urlparse(url).hostname or ""
        for entry in self.allow:
            want = urlparse(entry).hostname if "://" in entry else entry
            if not want:
                continue
            if host == want or host.endswith("." + want):
                return True
        return False

    def saw_url(self, url):
        self.last = url
        if url and not self.allows(url):
            return self.reason
        return None

    def poll(self):
        if self.current_url is not None:
            try:
                url = self.current_url()
            except Exception:
                return self.reason
        else:
            url = self.last
        return self.saw_url(url)


class ManualDetector(Detector):
    reason = "manual"

    def __init__(self):
        self.raised = False
        self.lock = threading.Lock()

    def raise_(self):
        with self.lock:
            self.raised = True

    def lower(self):
        with self.lock:
            self.raised = False

    def poll(self):
        with self.lock:
            return self.reason if self.raised else None


class Curtain:
    """The fail-closed curtain. evaluate() is pure enough to test with an injected clock and detectors."""

    def __init__(self, detectors=None, clock=time.monotonic, clear_hold=CURTAIN_CLEAR_HOLD_S, on_change=None):
        self.detectors = list(detectors or [])
        self.clock = clock
        self.clear_hold = clear_hold
        self.on_change = on_change
        self.lock = threading.RLock()
        self.up = False
        self.reason = None
        self.since = None
        self.clear_since = None
        self.error = None
        self._stop = threading.Event()
        self._thread = None
        self.manual = ManualDetector()
        self.detectors.append(self.manual)

    def add(self, detector):
        with self.lock:
            self.detectors.append(detector)
        return detector

    def evaluate(self):
        """One pass: any raised or unsure detector raises the curtain; all clear for two seconds lowers it."""
        with self.lock:
            reason = None
            for det in self.detectors:
                try:
                    got = det.poll()
                except Exception as e:  # a detector that fails is never clear
                    got = "detector"
                    self.error = "%s: %s" % (getattr(det, "reason", "detector"), e)
                if got:
                    reason = got if got in REASONS else "detector"
                    break
            now = self.clock()
            if reason:
                self.clear_since = None
                return self._set(True, reason, now)
            if not self.up:
                self.clear_since = None
                return False
            if self.clear_since is None:
                self.clear_since = now
            if now - self.clear_since >= self.clear_hold:
                return self._set(False, None, now)
            return True

    def _set(self, up, reason, now):
        if up == self.up:
            if up and reason and reason != self.reason:
                self.reason = reason
                if self.on_change:
                    try:
                        self.on_change(up, reason)
                    except Exception:
                        pass
            return self.up
        self.up = up
        self.reason = reason
        self.since = now if up else None
        self.clear_since = None
        if self.on_change:
            try:
                self.on_change(up, reason or "detector")
            except Exception:
                pass
        return self.up

    def raise_(self, reason="manual"):
        if reason == "manual":
            self.manual.raise_()
        with self.lock:
            self._set(True, reason if reason in REASONS else "manual", self.clock())

    def lower(self):
        """The owner's own button: lower now (the detectors may raise it again on the next pass)."""
        self.manual.lower()
        with self.lock:
            self.clear_since = self.clock()
            return self._set(False, None, self.clock())

    def snapshot(self):
        with self.lock:
            return {"up": self.up, "reason": self.reason if self.up else None,
                    "since": None if self.since is None else int(self.since)}

    def start(self):
        if self._thread is not None:
            return
        self._stop.clear()
        self._thread = threading.Thread(target=self._loop, name="computer-curtain", daemon=True)
        self._thread.start()

    def _loop(self):
        while not self._stop.is_set():
            try:
                self.evaluate()
            except Exception:
                self.raise_("detector")
            self._stop.wait(CURTAIN_POLL_S)

    def stop(self):
        self._stop.set()
        thread, self._thread = self._thread, None
        if thread is not None and thread is not threading.current_thread() and thread.is_alive():
            thread.join(1.0)


# ---- platform probes -----------------------------------------------------------------------------------------------

def _active_window(env=None):
    """(title, WM_CLASS) of the focused X11 window, or ("", "") when there is none (no display, no xdotool)."""
    run_env = None
    if env is not None:
        run_env = dict(os.environ)
        run_env.update({k: v for k, v in env.items() if isinstance(v, str)})
    if not (run_env or os.environ).get("DISPLAY"):
        return "", ""
    try:
        win = subprocess.run(["xdotool", "getactivewindow"], capture_output=True, text=True, timeout=2, env=run_env)
    except (OSError, subprocess.SubprocessError):
        return "", ""
    wid = win.stdout.strip()
    if not wid:
        return "", ""
    try:
        title = subprocess.run(["xdotool", "getwindowname", wid], capture_output=True, text=True, timeout=2,
                               env=run_env).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        title = ""
    try:
        klass = subprocess.run(["xprop", "-id", wid, "WM_CLASS"], capture_output=True, text=True, timeout=2,
                               env=run_env).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        klass = ""
    return title, klass


def _password_field_focused():
    """True when the focused AT-SPI element is a password field, False when it is not, None when it cannot tell.

    On Windows and macOS the probe is the platform's accessibility API; this module's default is the Linux one. A
    missing accessibility stack is None (unsure), which raises the curtain: the stream is never shown blind.
    """
    if sys.platform == "win32":
        return _password_field_windows()
    if sys.platform == "darwin":
        return _password_field_macos()
    try:
        import pyatspi
    except Exception:
        return None
    try:
        desktop = pyatspi.Registry.getDesktop(0)
        focused = _focused_accessible(desktop, pyatspi, 0)
        if focused is None:
            return False
        return focused.getRole() == pyatspi.ROLE_PASSWORD_TEXT
    except Exception:
        return None


def _focused_accessible(node, pyatspi, depth):
    """The accessible with STATE_FOCUSED, walking at most a dozen levels (accessibility trees can be huge)."""
    if depth > 12:
        return None
    try:
        state = node.getState()
        if state is not None and state.contains(pyatspi.STATE_FOCUSED):
            return node
    except Exception:
        pass
    try:
        children = node
    except Exception:
        return None
    try:
        for child in children:
            got = _focused_accessible(child, pyatspi, depth + 1)
            if got is not None:
                return got
    except Exception:
        return None
    return None


def _password_field_windows():
    # UI Automation through PowerShell: the focused element's ControlType is Edit and IsPassword is true.
    try:
        script = ("Add-Type -AssemblyName UIAutomationClient;"
                  "$e=[System.Windows.Automation.AutomationElement]::FocusedElement;"
                  "if($e -and $e.Current.IsPassword){'yes'}else{'no'}")
        r = subprocess.run(["powershell", "-NoProfile", "-Command", script], capture_output=True, text=True, timeout=3)
    except (OSError, subprocess.SubprocessError):
        return None
    if "yes" in r.stdout:
        return True
    if "no" in r.stdout:
        return False
    return None


def _password_field_macos():
    # The AX API is only reachable from a signed/trusted helper; without it the answer is unsure.
    return None


# ---- the hotkey ---------------------------------------------------------------------------------------------------

class Hotkey:
    """A global curtain hotkey. X11: Ctrl+Alt+P through XGrabKey (ctypes, no dependency). Elsewhere: unavailable, and
    the owner uses Curtain now on Live or `openvibe-node stream` (SIGUSR1) instead."""

    def __init__(self, callback, combo="ctrl+alt+p"):
        self.callback = callback
        self.combo = combo
        self._stop = threading.Event()
        self._thread = None
        self.error = None

    def available(self):
        return sys.platform.startswith("linux") and bool(os.environ.get("DISPLAY"))

    def start(self):
        if not self.available() or self._thread is not None:
            return False
        self._thread = threading.Thread(target=self._run, name="computer-curtain-hotkey", daemon=True)
        self._thread.start()
        return True

    def stop(self):
        self._stop.set()
        thread, self._thread = self._thread, None
        if thread is not None and thread.is_alive():
            thread.join(1.0)

    def _run(self):
        try:
            x11 = ctypes.CDLL("libX11.so.6")
        except OSError as e:
            self.error = str(e)
            return
        # Declare the few calls used; keycodes and masks are constants in X11/X.h.
        x11.XOpenDisplay.restype = ctypes.c_void_p
        x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
        x11.XDefaultRootWindow.restype = ctypes.c_ulong
        x11.XDefaultRootWindow.argtypes = [ctypes.c_void_p]
        x11.XStringToKeysym.restype = ctypes.c_ulong
        x11.XStringToKeysym.argtypes = [ctypes.c_char_p]
        x11.XKeysymToKeycode.restype = ctypes.c_ubyte
        x11.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
        x11.XGrabKey.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_uint, ctypes.c_ulong, ctypes.c_int,
                                 ctypes.c_int, ctypes.c_int]
        x11.XPending.argtypes = [ctypes.c_void_p]
        x11.XNextEvent.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
        x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
        x11.XCloseDisplay.restype = ctypes.c_int
        dpy = x11.XOpenDisplay(None)
        if not dpy:
            self.error = "no X display"
            return
        try:
            key = self.combo.split("+")[-1]
            mods = 0
            parts = [p.strip().lower() for p in self.combo.split("+")[:-1]]
            if "ctrl" in parts or "control" in parts:
                mods |= 4      # ControlMask
            if "alt" in parts or "mod1" in parts:
                mods |= 8      # Mod1Mask
            if "shift" in parts:
                mods |= 1      # ShiftMask
            keysym = x11.XStringToKeysym(key.encode("ascii"))
            keycode = x11.XKeysymToKeycode(dpy, keysym)
            root = x11.XDefaultRootWindow(dpy)
            # Grab the four CapsLock/NumLock variants, else the hotkey stops working when either is on.
            for extra in (0, 2, 16, 18):  # LockMask, Mod2Mask
                x11.XGrabKey(dpy, keycode, mods | extra, root, 1, 1, 1)
            event = ctypes.create_string_buffer(192)   # XEvent is 24 longs; this is plenty for KeyPress.
            while not self._stop.is_set():
                while x11.XPending(dpy):
                    x11.XNextEvent(dpy, event)
                    try:
                        self.callback("manual")
                    except Exception:
                        pass
                self._stop.wait(0.05)
        finally:
            x11.XCloseDisplay(dpy)


def install_sigusr1(toggle):
    """A service's local curtain hotkey: SIGUSR1 toggles it (kill -USR1 <pid>). Returns the previous handler."""
    try:
        old = signal.getsignal(signal.SIGUSR1)

        def handler(signum, frame):
            try:
                toggle()
            except Exception:
                pass
        signal.signal(signal.SIGUSR1, handler)
        return old
    except (ValueError, AttributeError, OSError):
        return None
