"""OpenVibe Node computer plugin: the machine's terminal, files, screen and input for its owner's agents (plan T14/T17).

Off unless its owner lists it in the Node's config. The owner sets the **control level** on the machine itself; a cloud
command can never raise it:

* ``observe`` (the default): capture the screen; list, stat and read files inside the roots.
* ``ask``: also run programs, write files inside the roots and use the mouse and keyboard, each only after the owner
  approves it on the machine
  (the tray, a notification's buttons, or ``openvibe-node approvals``, ``approve <id>``, ``deny <id>``); unanswered
  requests expire.
* ``trusted``: run programs, write files inside the roots and use the mouse and keyboard without asking.
* ``full``: as trusted, anywhere the account running the Node can reach.

The local kill switch (``openvibe-node stop``) and a remote e-stop drop the plugin to observe, kill what is running
and deny what waits; the core refuses acting commands while either is latched too. The plugin runs as the Node's own
account and never as root, except on a shared desk host for other people, where it runs as root and every desk sits in
its own sandbox.

Command kinds and their answers (the ack's ``data``):

* ``exec`` ``{"argv": [...], "cwd"?, "timeout_s"?, "stdin"?, "desk"?, "detach"?}`` → ``{exit_code, stdout, stderr,
  truncated, timed_out, duration_ms}``. A program and its arguments, never a shell line; output is cut at
  ``max_output_bytes``. ``desk`` (Node v0.26) runs it in that desk's session (its display, bus and temp folder), so a
  program with windows opens on the desk. ``detach`` (a whole desk only) starts a program that keeps running (an office
  suite, a terminal) and answers at once ``{started, pid, desk}``; it is ended when its desk stops (at most
  MAX_DESK_PROGRAMS per desk).
* ``fs`` ``{"op": "list"|"stat"|"read"|"write"|"mkdir", "path", …}``: ``read`` takes ``offset``/``length`` and answers
  ``content_b64``; ``write`` takes ``content_b64`` and ``mode`` (``create``, ``overwrite`` or ``append``).
* ``screen`` ``{"max_width"?}`` → ``{format, width, height, content_b64}`` (JPEG through Pillow when it is installed).
* ``screen_stream`` ``{"action": "start", "seconds"?, "fps"?}`` → ``{streaming, seconds, fps, tool}``, or ``{"action":
  "stop"}``: the screen shown live to the machine's owner. The plugin captures a JPEG frame ``fps`` times a second (at
  most 5) with the same tools as ``screen`` and hands each to the core as video (``stream: "screen"``), which publishes
  it to the owner's private stream; the core alone holds the WHIP URL and key. It runs at every level, like a
  screenshot, says so on the desktop when it starts (config ``notify``), and ends when its time is up (30 s to 30 min),
  on stop, and at once on the local kill switch or an e-stop. Config ``allow_screen_stream: false`` turns it off.
* ``input`` ``{"action", "x"?, "y"?, "width"?, "height"?, "dy"?, "text"?, "keys"?}`` → ``{done, action, x?, y?}``: move,
  click, double_click or right_click at a point, scroll by ``dy`` clicks, type ``text`` into whatever has focus, or press
  one key or chord (``keys``, for example ``ctrl+s``). ``x``/``y`` are in the space of a screen picture ``width`` ×
  ``height`` (the ``screen`` answer) and are scaled to the real screen. An acting command: refused at observe, approved
  one by one at ask. xdotool on X11, cliclick on macOS (no scroll there); none on Wayland yet. Config
  ``allow_input: false`` turns it off.
* ``browser`` ``{"action", "url"?, "selector"?, "text"?, "key"?, "max_width"?}`` (every connection it makes, a page's own
  scripts included, goes through the egress guard, ``egress.py``: never the machine's own network below level full): a
  Chromium the agent drives, in a profile
  of its own (``browser_profile``), over a pipe (no port is opened). ``open`` a ``http(s)`` URL, ``click`` a CSS
  ``selector`` or the button or link whose ``text`` it shows, ``fill`` a field (``selector`` and ``text``), ``press`` a
  ``key`` (Enter, Tab, Escape, arrows …), ``back`` → ``{url, title, text, truncated, links}``; ``read`` the page the same
  way; ``screenshot`` → ``{format, width, height, content_b64}``; ``close``. Reading, screenshots and closing run at every
  level; the rest are acting commands. Below level full, ``open`` refuses an address on the machine's own network and
  every download is denied; at full a desk's downloads folder is used only when it resolves inside the roots.
  Config ``allow_browser: false`` turns it off; ``browser_path``, ``browser_headless`` (default true).
* ``desk`` ``{"action": "start"|"stop"|"status"|"list", "desk"?, "width"?, "height"?}``: a private desk (``desk.py``): an Xvfb display with a
  window manager and a D-Bus session of its own, where the agent works without touching the owner's screen. ``screen``,
  ``screen_stream``, ``input`` and ``browser`` take ``"desk": "private"`` to act there (the browser shown on the desk, in a
  profile of its own, ``browser_profile`` + ``-desk``); ``main`` (the default) is the owner's screen. Several desks run at
  once (Node v0.19, Desks plan D1): ``"desk": "desk-<name>"`` names one (start it with ``desk`` and that name), each with
  its own display, bus, browser profile (``browser_profile`` + ``-<name>``), temp folder and downloads folder, so two
  agents working at once never see or drive each other's screen. ``max_desks`` (default: one per 2 GiB of memory, 1 to
  8) caps how many run at once; ``list`` reports the running ones. A desk no command used for ``desk_idle_s`` (15
  minutes by default) is stopped, so a desk an agent left behind (it crashed or restarted mid-task) ends by itself.
  ``"profile": "<name>"`` on start (Node v0.24, Desks plan D7 on one machine) keeps the desk's browser profile by
  name: the desk's browser starts from that profile (its cookies and site storage: sign in once, on any desk) and the
  desk saves it back when it stops (the last desk to stop wins), then removes its own copy. Caches are never kept, a
  profile larger than ``desk_profile_max_mb`` is not saved, and the kill switch saves nothing.
  ``"kind": "browser"`` on start (Node v0.23, Desks plan D2) makes a **browser desk**: a headless browser alone, with no
  display, about half the cost of a whole desk (``kind: desktop``, the default), on Linux or macOS. Its page is its
  screen: ``screen``, ``input`` (mouse, wheel, keys and text, sent to the page) and ``screen_stream`` (frames of the page)
  on it go through the browser's pipe, and ``browser`` drives that same page. A browser desk counts as half a desk
  against ``max_desks``. ``describe`` lists the kinds this machine runs (``desk.kinds``). Starting and stopping act
  (refused at observe, approved at ask), ``status`` runs at every level, and the kill switch stops the desk. Whole desks
  need Linux with Xvfb, browser desks a Chromium-family browser; config ``allow_desk: false`` turns both off.
* ``desk.stream`` ``{"action": "start"|"stop"|"status"|"curtain", "desk"?, "allow_sites"?, "up"?, "reason"?}``
  (Agent Streams S1, ``stream.py`` and ``curtain.py``): the agent's own stream on OpenVibe.Live. ``start`` answers
  ``{streaming, sid, capture, curtain, detectors}`` with the capture source the core's ffmpeg should use (x11grab,
  PipeWire, ddagrab, AVFoundation, or a browser desk's page through a FIFO); the core mints the stream and the ``wpt_``
  ticket on Live, so neither reaches this plugin. The privacy curtain's detectors (password field, secret-named
  window/file, an environment print, the hotkey, the browser's allow-list) run here and report ``curtain`` events;
  a detector that fails or is unsure puts the curtain up and never down. Config ``allow_desk_stream: false`` turns it
  off. The core publishes over WHIP and snapshots while nobody watches.
* **A desk host for other people** (``desk_isolation: "sandbox"``, Node v0.32, Desks plan D5): every desk is a browser
  desk whose browser runs under ``openvibe-node desk-sandbox`` (the jobs' sandbox: its own namespaces and cgroup as
  ``worker.run_as``, ``desk_memory_mb``, ``desk_cpu_millis``, ``desk_disk_mb`` and ``desk_pids`` each, only the public
  internet with ``desk_egress: public``), its profile and downloads inside the sandbox and gone with the desk. Nothing
  of the machine itself is offered: ``exec``, ``fs``, ``probe``, the main screen and browser, whole desks and kept
  profiles are refused, and so is ``screen_stream`` (the machine's one stream is its operator's; ``screen`` shows a desk). It runs on Linux with the Node as root (the one case the plugin runs as root) and the worker
  on, at level ``trusted`` (or ``observe``): nobody is at the machine to approve.
* ``probe`` ``{"target", "timeout_s"?}`` → for ``http(s)://…`` one GET, no redirect followed: ``{status, content_type,
  etag, last_modified, body, truncated, ms}``; for ``tcp://host:port``: ``{open, ms, error?}``. Read-only, so it runs at
  every level, and only towards the machine's own network: every address the name resolves to must be private,
  loopback or link-local, and the connection goes to the address that was checked. Config ``probe: false`` turns it off.
"""

import base64
import contextlib
import ctypes
import getpass
import hashlib
import json
import http.client
import ipaddress
import os
import re
import socket
import ssl
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

from openvibe_plugin import Deferred, Fault, Plugin

from .browser import KEY_CODES, Browser, BrowserError, find_binary
from .curtain import (Curtain, EnvPrintDetector, Hotkey, PasswordFieldDetector, SecretFileDetector, SiteDetector,
                      _active_window, install_sigusr1)
from .desk import MAX_DESKS_CAP, BrowserDesk, Desks, PrivateDesk, default_max_desks
from .egress import EgressProxy, not_public
from .stream import PageScreencast, capture_kinds, desktop_capture
from .files import Files, Handles

LEVELS = ("observe", "ask", "trusted", "full")
RANK = {name: i for i, name in enumerate(LEVELS)}
READ_OPS = frozenset({"list", "stat", "read"})
WRITE_OPS = frozenset({"write", "mkdir"})
MAX_ARGS = 64
MAX_ARG_LEN = 4096
MAX_STDIN = 64 * 1024
MAX_LIST = 1000
MAX_RUNNING = 2
MAX_WAITING = 8
# An approval from the desktop (the notification's button or the macOS dialog) is not taken while the agent drives the
# mouse or keyboard, nor this many seconds after: an approved click must not land on another request's Approve button.
INPUT_GUARD_S = 2.0
# A desktop surface that cannot show the whole request offers no Approve button on cut text (notify-send's body, a
# macOS dialog); the owner answers it in full with `openvibe-node approvals`.
NOTIFY_SUMMARY_MAX = 300
DIALOG_SUMMARY_MAX = 400
FULL_VIEW_HINT = "openvibe-node approvals"
# Control, ANSI and bidi characters are escaped before any surface shows them: a summary can be a terminal, a
# notification or a dialog, and none of them may interpret what the agent wrote as layout or markup.
BIDI_CHARS = frozenset("\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069")
# Linux: PR_SET_NO_NEW_PRIVS (children cannot gain privileges) and PR_SET_CHILD_SUBREAPER (orphaned descendants of an
# exec'd program come back to the plugin, so a stop can find and end them).
PR_SET_NO_NEW_PRIVS = 38
PR_SET_CHILD_SUBREAPER = 36
CAPTURE_JOIN_S = 5.0
CONTROL_FILES = ("config.json", "credential.json", "latch.json", "node.sock")
CONTROL_STATE_PARTS = ("plugins", "venv", "frames")   # the state directory's own folders
try:
    _PRCTL = ctypes.CDLL(None, use_errno=True).prctl if sys.platform.startswith("linux") else None
except (OSError, AttributeError):
    _PRCTL = None

# The core caps an ack's data at 512 KB of JSON; base64 grows by a third.
MAX_OUTPUT_CAP = 192 * 1024
MAX_READ_CAP = 256 * 1024
SCREEN_MAX_BYTES = 300 * 1024
# The live screen: frame width (Pillow resizes larger screens) and JPEG quality; the stream ends after this many
# capture failures in a row.
STREAM_WIDTH = 1600
STREAM_QUALITY = 70
STREAM_MAX_FAILURES = 5
INPUT_ACTIONS = frozenset({"move", "click", "double_click", "right_click", "scroll", "type", "key"})
BROWSER_READS = frozenset({"read", "screenshot", "close"})
BROWSER_ACTS = frozenset({"open", "click", "fill", "press", "back"})
MAX_URL = 2048
MAX_DESK_PROGRAMS = 16
PROFILE_NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,23}$")
SESSION_COOKIES = "ov-session-cookies.json"   # in a kept profile: the cookies that end with the browser
# What a kept profile leaves out: caches, crash reports, locks and the last session's tabs (a desk opens on a blank page).
PROFILE_SKIP = shutil.ignore_patterns("Cache", "Code Cache", "GPUCache", "GrShaderCache", "GraphiteDawnCache", "ShaderCache",
                                      "DawnCache", "DawnGraphiteCache", "DawnWebGPUCache", "CacheStorage", "ScriptCache",
                                      "component_crx_cache", "Crashpad", "BrowserMetrics*", "Safe Browsing",
                                      "optimization_guide_*", "Singleton*", "Sessions", "blob_storage", "*.tmp")
MAX_SELECTOR = 500
POINT_ACTIONS = frozenset({"move", "click", "double_click", "right_click"})
MAX_TYPE = 2000
KEYS_OK = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_+-")
MODIFIERS = {"ctrl": "ctrl", "control": "ctrl", "alt": "alt", "option": "alt", "shift": "shift", "cmd": "cmd",
             "command": "cmd", "super": "super", "meta": "super", "win": "super"}
# xdotool key names → cliclick's kp: names (macOS); a single character is typed with t:.
CLICLICK_KEYS = {"return": "return", "enter": "enter", "escape": "esc", "esc": "esc", "tab": "tab", "backspace": "delete",
                 "delete": "fwd-delete", "space": "space", "up": "arrow-up", "down": "arrow-down", "left": "arrow-left",
                 "right": "arrow-right", "home": "home", "end": "end", "page_up": "page-up", "prior": "page-up",
                 "page_down": "page-down", "next": "page-down"}
# A desk host serving other people (desk_isolation sandbox, Desks plan D5): each desk's browser runs under
# `openvibe-node desk-sandbox`, which replaces Chrome's own sandbox (it cannot nest inside ours) and has no /dev/shm;
# its profile and downloads live in the sandbox's /tmp, gone with the desk; its environment is only this.
SANDBOX_CHROME_ARGS = ("--no-sandbox", "--no-zygote", "--disable-dev-shm-usage", "--disable-gpu")
SANDBOX_PROFILE = "/tmp/desk/profile"
SANDBOX_DOWNLOADS = "/tmp/desk/downloads"
SANDBOX_ENV = ("HOME=/tmp/desk", "TMPDIR=/tmp/desk", "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "DBUS_SESSION_BUS_ADDRESS=disabled:")
# What a shared desk host's own commands may be: its browser desks and what acts on them, never the machine itself.
# No screen_stream: the live view goes to the machine owner's one stream, which is the host's operator's, not the
# person's whose desk it is (a desk is seen there through screen, a picture answered to the command).
SHARED_DESK_KINDS = frozenset({"screen", "input", "browser"})



def _parse_notify_send(out):
    """notify-send --wait prints the name of the button pressed; nothing when the notification just closed."""
    lines = (out or "").strip().splitlines()
    last = lines[-1].strip() if lines else ""
    return True if last == "approve" else False if last == "deny" else None


def _parse_dialog(out):
    """osascript's display dialog: "button returned:Approve, gave up:false"; giving up answers nothing."""
    out = out or ""
    if "gave up:true" in out:
        return None
    return True if "button returned:Approve" in out else False if "button returned:Deny" in out else None


def _plain(value):
    """A string safe to show on one line: control, ANSI (ESC is a control character) and bidi characters are escaped.
    Backslashes are kept, so escaping an already escaped value changes nothing that matters."""
    out = []
    for ch in str(value):
        code = ord(ch)
        if ch == "\n":
            out.append("\\n")
        elif ch == "\r":
            out.append("\\r")
        elif ch == "\t":
            out.append("\\t")
        elif code < 0x20 or code == 0x7f or 0x80 <= code <= 0x9f or ch in BIDI_CHARS or ch in "\u2028\u2029":
            out.append("\\x%02x" % code if code <= 0xff else "\\u%04x" % code)
        else:
            out.append(ch)
    return "".join(out)


def _markup(value):
    """_plain for notify-send's markup: its body may be either text or the Pango-like markup, so escape both."""
    return _plain(value).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def _digest(data):
    """A short content digest for an approval: enough to tell two requests apart, not a secret."""
    return hashlib.sha256(data).hexdigest()[:16]


def _argv_text(argv):
    """A full argv, one shell-quoted argument at a time, control characters escaped."""
    return " ".join(shlex.quote(_plain(a)) for a in argv)


def _account_name():
    try:
        return _plain(getpass.getuser())
    except Exception:  # noqa: BLE001 - no passwd entry, a container without one
        return str(os.geteuid()) if hasattr(os, "geteuid") else "this account"


def _no_new_privs_available():
    """Whether exec'd programs can be given PR_SET_NO_NEW_PRIVS here (it is a no-op requirement outside Linux)."""
    if not sys.platform.startswith("linux"):
        return True
    return _PRCTL is not None


def _set_no_new_privs():
    """In the child, before exec: no program it starts may gain a privilege over the plugin's account. Never returns
    when it cannot be set: an exec without it is refused by exiting before the program runs."""
    try:
        if _PRCTL is None or _PRCTL(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0:
            raise OSError(ctypes.get_errno() if _PRCTL is not None else 0, "no_new_privs unavailable")
    except BaseException:  # noqa: BLE001 - the child must not run the program
        os._exit(126)


def _become_subreaper():
    """Linux: orphaned descendants of exec'd programs are reparented to the plugin, so a stop can end them."""
    if not sys.platform.startswith("linux") or _PRCTL is None:
        return False
    try:
        return _PRCTL(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) == 0
    except Exception:  # noqa: BLE001
        return False


def _tree_size(path, ignore):
    """The bytes a copy of path with that ignore would hold."""
    total = 0
    for root, dirs, files in os.walk(path):
        skip = ignore(root, dirs + files)
        dirs[:] = [d for d in dirs if d not in skip]
        for f in files:
            if f not in skip:
                try:
                    total += os.lstat(os.path.join(root, f)).st_size
                except OSError:
                    pass
    return total


def _int(v, name, lo, hi, default):
    if v is None:
        return default
    if isinstance(v, bool) or not isinstance(v, (int, float)) or v != v:
        raise Fault("bad_value", "%s must be a number" % name)
    return int(max(lo, min(hi, v)))


def node_data_dir():
    """Where the plugin keeps what it keeps (the agent's browser profile, the desks' folders). ~/.local/share/openvibe-node
    on Linux and macOS; on Windows %LOCALAPPDATA%\\openvibe-node, where a Windows program keeps its data (it was the
    Linux path with mixed separators before 0.31, found by running the tests on Windows). A Windows machine that already
    has the old folder keeps using it, so a kept sign-in is not lost."""
    old = os.path.normpath(os.path.expanduser("~/.local/share/openvibe-node"))
    if os.name != "nt":
        return old
    new = os.path.join(os.environ.get("LOCALAPPDATA") or os.path.expanduser(os.path.join("~", "AppData", "Local")), "openvibe-node")
    return old if os.path.isdir(old) and not os.path.isdir(new) else new

class Computer(Plugin):
    driver = "computer"
    version = "0.15.0"
    motion_kinds = frozenset()
    telemetry_interval_s = 2.0

    def __init__(self, which=shutil.which, popen=subprocess.Popen, clock=time.monotonic, env=None, platform=None, screen_size=None,
                 browser_factory=None, resolve=None, desk=None, desk_factory=None):
        self.which = which
        self.popen = popen
        self.screen_size = screen_size or self._screen_size
        self.clock = clock
        self.env = os.environ if env is None else env
        self.platform = platform or sys.platform
        self.level = "observe"
        self.roots = []
        self.root_forms = {}      # every root, and the names it answers to (the configured one and its own)
        self._handles = Handles()   # the folders, opened once and kept open
        self.timeout_s = 30
        self.max_output = 64 * 1024
        self.max_read = 256 * 1024
        self.allow_exec = True
        self.allow_probe = True
        self.allow_input = True
        self.notify = True
        self.notify_actions = None  # whether notify-send shows buttons (libnotify 0.7.10+); found out on first use
        self.prompts = {}  # command id -> the notification or dialog process asking about it
        self.tray_until = 0.0  # the Node's tray shows the approvals until then (op "tray")
        self.input_busy = 0
        self.input_ended = None
        self.approval_timeout_s = 120
        self.pending = {}  # command id -> ((run, threaded), expires_at, summary)
        self.procs = set()          # programs still running (their parent is this process)
        self.groups = set()         # process groups/sessions of exec'd programs, kept after the parent exits
        self.inputs = set()         # input tool processes (xdotool/cliclick), killed by stop and counted by the guard
        self.captures = set()       # screen capture processes, killed by stop
        self.children = set()       # pids of programs this plugin started (never reaped from under Popen)
        self.generation = 0         # rises on every stop: work authorized before one never starts after it
        self._gate = threading.local()   # the generation of the closure this thread is running
        # Every exec'd program carries this in its environment, so a stop can find its descendants even after they
        # leave the process group (and never touches a process this plugin did not start).
        self.exec_env_name = "OPENVIBE_NODE_EXEC_TREE"
        self.exec_env_value = "%d-%s" % (os.getpid(), os.urandom(8).hex())
        self.running = 0
        self.mu = threading.Lock()
        self._euid = os.geteuid() if hasattr(os, "geteuid") else None
        self.subreaper = False
        self._control_dirs = self._node_control_dirs()
        self.control_files = self._node_control_files()
        self.allow_browser = True
        self.browser_path = None
        self.browser_profile = None
        self.browser_headless = True
        self.browser_idle_s = 600
        self.browser = None
        self.allow_screen_stream = True
        self.stream = None  # the live screen: {"sid", "until", "fps", "tool", "dir", "stop": Event}
        self.stream_mu = threading.Lock()
        # The agent's desk stream (Agent Streams S1): capture, the curtain's detectors and the hotkey.
        self.allow_desk_stream = True
        self.desk_stream = None
        self.desk_stream_mu = threading.Lock()
        self.desk_stream_allow_sites = []
        self.desk_stream_hotkey = "ctrl+alt+p"
        self.desk_stream_detectors = None
        self._sigusr1_old = None
        self.browser_mu = threading.Lock()
        self.browser_op_mu = threading.Lock()
        self.browser_factory = browser_factory or self._new_browser
        # Every agent browser's connections go through the egress guard (Node v0.25.0): a page's own scripts never reach
        # the machine's own network below level full (config browser_egress: "open" turns it off).
        self.browser_egress = "public"
        self.desk_isolation = "none"   # or "sandbox": a desk host for other people (SANDBOX_*)
        self.desk_limits = {"memory_mb": 1024, "cpu_millis": 1000, "disk_mb": 512, "pids": 512}
        self.desk_egress = "public"
        self.egress = EgressProxy(blocked=self._egress_blocked, log=lambda *a: self.log(*a))
        self.desk_factory = desk_factory or (lambda: PrivateDesk(which=which, popen=popen))
        self.desk = desk or self.desk_factory()   # the desk named private (the first one, as before v0.9.0)
        self.desks = Desks(self.desk_factory, private=self.desk, max_desks=default_max_desks(),
                           browser_factory=lambda: BrowserDesk(clock=time.time, base_env=self.env))
        self.allow_desk = True
        self.desk_browsers = {}    # desk name -> the agent's browser on that desk (its own profile, shown there)
        self.desk_used = {}        # desk name -> when a command last used it (clock), for desk_idle_s
        self.desk_profiles = {}    # desk name -> the kept profile it started with (Node v0.24.0)
        self.desk_restored = set() # desks whose browser started from their kept profile (so it is saved back on stop)
        self.profile_locks = {}    # profile name -> one copy in or out at a time
        self.desk_profile_max_mb = 256
        self.desk_programs = {}    # desk name -> programs started there with detach, ended with the desk
        self.desk_idle_s = 900
        self.browser_locks = {}    # "main" or a desk name -> one action at a time in that browser
        self.resolve = resolve or (lambda host: [ai[4][0] for ai in socket.getaddrinfo(host, None)])

    # ---- configuration -------------------------------------------------------------------------------------------
    def _configure(self, config):
        level = str(config.get("level") or "observe").strip().lower()
        if level not in RANK:
            raise Fault("bad_value", "level is one of %s" % ", ".join(LEVELS))
        roots = config.get("roots")
        if roots is None:
            roots = [os.path.expanduser("~")]
        if not isinstance(roots, list) or not all(isinstance(r, str) and r.strip() for r in roots):
            raise Fault("bad_value", "roots is a list of folders")
        self.level = level
        # A root is kept under the name it was given and under its own: a folder that is reached through a link
        # is the same folder, and the plugin must recognize both names for it.
        forms = {}
        self.roots = []
        for r in roots:
            given = os.path.normpath(os.path.expanduser(r.strip()))
            real = os.path.realpath(given)
            self.roots.append(real)
            names = forms.setdefault(real, [])
            for name in (given, real):
                if name not in names:
                    names.append(name)
        self.root_forms = forms
        self._control_dirs = self._node_control_dirs()
        self.control_files = self._node_control_files()
        self.timeout_s = _int(config.get("timeout_s"), "timeout_s", 1, 600, 30)
        self.max_output = _int(config.get("max_output_bytes"), "max_output_bytes", 1024, MAX_OUTPUT_CAP, 64 * 1024)
        self.max_read = _int(config.get("max_read_bytes"), "max_read_bytes", 1024, MAX_READ_CAP, 256 * 1024)
        self.approval_timeout_s = _int(config.get("approval_timeout_s"), "approval_timeout_s", 10, 600, 120)
        self.allow_exec = config.get("allow_exec", True) is not False
        self.allow_probe = config.get("probe", True) is not False
        self.allow_input = config.get("allow_input", True) is not False
        self.notify = config.get("notify", True) is not False
        self.allow_browser = config.get("allow_browser", True) is not False
        self.allow_screen_stream = config.get("allow_screen_stream", True) is not False
        self.allow_desk_stream = config.get("allow_desk_stream", True) is not False
        self.desk_stream_hotkey = str(config.get("desk_stream_hotkey") or "ctrl+alt+p")
        sites = config.get("desk_stream_allow_sites") or []
        if not isinstance(sites, list) or not all(isinstance(u, str) and u for u in sites) or len(sites) > 50:
            raise Fault("bad_value", "desk_stream_allow_sites is a list of up to 50 URLs")
        self.desk_stream_allow_sites = list(sites)
        dets = config.get("desk_stream_detectors")
        if dets is not None:
            known = ("password_field", "secret_file", "env_print", "manual", "site")
            if not isinstance(dets, list) or not all(d in known for d in dets):
                raise Fault("bad_value", "desk_stream_detectors is a list of %s" % ", ".join(known))
            self.desk_stream_detectors = list(dets)
        self.allow_desk = config.get("allow_desk", True) is not False
        self.desks.max_desks = _int(config.get("max_desks"), "max_desks", 1, MAX_DESKS_CAP, default_max_desks())
        self.desk_idle_s = _int(config.get("desk_idle_s"), "desk_idle_s", 60, 86400, 900)
        self.desk_profile_max_mb = _int(config.get("desk_profile_max_mb"), "desk_profile_max_mb", 16, 4096, 256)
        self.desks.base_dir = os.path.normpath(os.path.expanduser(config.get("desk_dir") or os.path.join(node_data_dir(), "desks")))
        self.browser_path = config.get("browser_path") or None
        self.browser_profile = os.path.normpath(os.path.expanduser(config.get("browser_profile") or os.path.join(node_data_dir(), "browser")))
        self.browser_headless = config.get("browser_headless", True) is not False
        self.browser_idle_s = _int(config.get("browser_idle_s"), "browser_idle_s", 60, 86400, 600)
        egress = str(config.get("browser_egress") or "public").strip().lower()
        if egress not in ("public", "open"):
            raise Fault("bad_value", "browser_egress is public (the default: never the machine's own network below level full) or open")
        self.browser_egress = egress
        isolation = str(config.get("desk_isolation") or "none").strip().lower()
        if isolation not in ("none", "sandbox"):
            raise Fault("bad_value", "desk_isolation is none (the default: desks for the machine's owner) or sandbox (a desk host for other people)")
        if isolation == "sandbox":
            if not self.platform.startswith("linux"):
                raise Fault("unsupported", "desk_isolation sandbox needs Linux (namespaces and cgroup v2)")
            if level not in ("observe", "trusted"):
                raise Fault("bad_value", "a desk host for other people runs at level trusted (or observe): nobody is at the machine to approve")
        self.desk_isolation = isolation
        self.desk_limits = {"memory_mb": _int(config.get("desk_memory_mb"), "desk_memory_mb", 256, 16384, 1024),
                            "cpu_millis": _int(config.get("desk_cpu_millis"), "desk_cpu_millis", 100, 16000, 1000),
                            "disk_mb": _int(config.get("desk_disk_mb"), "desk_disk_mb", 64, 16384, 512),
                            "pids": _int(config.get("desk_pids"), "desk_pids", 64, 8192, 512)}
        desk_egress = str(config.get("desk_egress") or "public").strip().lower()
        if desk_egress not in ("public", "none"):
            raise Fault("bad_value", "desk_egress is public (the default: the public internet only) or none")
        self.desk_egress = desk_egress

    def _shared(self):
        return self.desk_isolation == "sandbox"

    def describe(self, config):
        try:
            self._configure(config)
        except Fault:
            return {}
        if self._shared():
            # A desk host for other people offers its browser desks, sandboxed, and nothing of the machine itself.
            if "browser" not in self._desk_kinds():
                return {}
            return {"desk": {"level": self.level, "window_manager": None, "input": True, "screen": True, "named": True,
                             "max_desks": self.desks.max_desks, "kinds": ["browser"], "profiles": False,
                             "isolation": "sandbox", "limits": dict(self.desk_limits), "egress": self.desk_egress}}
        caps = {"files": {"level": self.level, "roots": self.roots, "max_read_bytes": self.max_read}}
        if self.allow_exec:
            caps["terminal"] = {"level": self.level, "timeout_s": self.timeout_s, "max_output_bytes": self.max_output,
                                "desks": bool(self._desk_kinds())}   # exec takes desk and detach (Node v0.26.0)
        tool = self._screen_tool()
        if tool:
            caps["screen"] = {"level": self.level, "tool": tool[0],
                              "stream": bool(self.allow_screen_stream and self._stream_capable(tool))}
        if self.allow_probe:
            caps["probe"] = {"lan_only": True, "schemes": ["http", "https", "tcp"]}
        itool = self._input_tool() if self.allow_input else None
        if itool:
            caps["input"] = {"level": self.level, "tool": itool, "scroll": itool == "xdotool"}
        binary = self._browser_binary()
        if binary:
            caps["browser"] = {"level": self.level, "browser": os.path.basename(binary), "headless": self.browser_headless,
                               "egress": self.browser_egress}
        kinds = self._desk_kinds()
        if self.allow_desk_stream and (kinds or tool):
            caps["desk.stream"] = {"capture": capture_kinds(self.platform), "browser": "browser" in kinds,
                                   "curtain": ["password_field", "secret_file", "env_print", "manual", "site", "detector"],
                                   "hotkey": self.desk_stream_hotkey, "allow_sites": True}
        if kinds:
            # A browser desk's screen and input are its page's own (the browser's pipe); a whole desk's need X tools.
            whole, page = "desktop" in kinds, "browser" in kinds
            caps["desk"] = {"level": self.level, "window_manager": self.desks.window_manager() if whole else None,
                            "input": page or (whole and bool(self.which("xdotool"))),
                            "screen": page or (whole and bool(self._x11_tool())),
                            "named": True, "max_desks": self.desks.max_desks, "kinds": kinds, "profiles": True}
        return caps

    def _desk_kinds(self):
        """The kinds of desk this machine runs: desktop (Linux with Xvfb) and browser (a Chromium-family browser)."""
        if not self.allow_desk:
            return []
        kinds = []
        if self.platform.startswith("linux") and self.desks.available():
            kinds.append("desktop")
        if self._browser_binary():
            kinds.append("browser")
        return kinds

    def setup(self, config):
        self._configure(config)
        self._euid = os.geteuid() if hasattr(os, "geteuid") else None
        self.subreaper = _become_subreaper()
        # As root only on a desk host for other people, where nothing it starts runs unconfined: every browser goes
        # through desk-sandbox (as worker.run_as) and no command reaches the machine itself (_shared_check).
        if hasattr(os, "geteuid") and os.geteuid() == 0 and not self._shared():
            raise Fault("not_allowed", "the computer plugin never runs as root: run the Node as your own account")
        if sys.platform.startswith("linux") and not _no_new_privs_available():
            self.log("no_new_privs is unavailable: running programs is refused")

    # ---- safety ----------------------------------------------------------------------------------------------------
    def effective_level(self):
        """The level now: observe while a local stop or an e-stop is latched."""
        if self.runtime is not None and self.runtime.estopped:
            return "observe"
        return self.level

    def stop(self):
        """halt, stop, the local kill switch and an e-stop: kill what runs, deny what waits."""
        with self.mu:
            # The generation rises before anything is killed: a closure authorized before this stop fails its check.
            self.generation += 1
            procs = list(self.procs)
            groups = list(self.groups)
            inputs = list(self.inputs)
            waiting = list(self.pending.keys())
            self.pending.clear()
        for p in inputs:
            self._kill(p)
        for p in procs:
            self._kill(p)
        for pid in groups:
            self._kill_group(pid)
        self._close_browser()
        self._end_stream("stopped on the machine")
        self._close_desk_browsers()
        self._end_desk_programs()
        self.desks.stop_all()
        self._drop_desk_profiles()
        self._kill_exec_descendants()
        self._reap_orphans()
        with self.mu:   # nothing this stop killed is tracked any more: a pid reused later is never killed by it
            self.procs.clear()
            self.groups.clear()
            self.inputs.clear()
            self.captures.clear()
        for cid in waiting:
            self._close_prompt(cid)
            self.emit_event("approval_done", id=cid, approved=False, reason="stopped")
            self.fail(cid, "local_stop", "stopped on the machine before it ran")

    def _kill(self, proc):
        """End a program and its process group/session. Falls back to the handle when the group is gone (Windows)."""
        try:
            if hasattr(os, "killpg") and getattr(proc, "pid", None):
                os.killpg(proc.pid, signal.SIGKILL)
            else:
                proc.kill()
        except (OSError, ProcessLookupError, AttributeError):
            try:
                proc.kill()
            except (OSError, AttributeError):
                pass

    @staticmethod
    def _kill_group(pid):
        """Unix: end a whole exec session by its group (Windows has no groups to kill; the Popen handle does it)."""
        if not hasattr(os, "killpg"):
            return
        try:
            os.killpg(pid, signal.SIGKILL)
        except (OSError, ProcessLookupError):
            pass

    @staticmethod
    def _group_alive(pid):
        if not hasattr(os, "killpg"):
            return False
        try:
            os.killpg(pid, 0)
            return True
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        except OSError:
            return False

    def _reap_orphans(self):
        """Linux: children of an exec'd program that outlived it were reparented here (subreaper); once killed, reap
        the zombies so the pid really goes, and never reap a pid Popen is still waiting for."""
        if not sys.platform.startswith("linux") or not os.path.isdir("/proc"):
            return
        with self.mu:
            known = set(self.children)
        me = os.getpid()
        try:
            names = os.listdir("/proc")
        except OSError:
            return
        for name in names:
            if not name.isdigit() or int(name) in known:
                continue
            try:
                with open("/proc/%s/stat" % name) as f:
                    data = f.read()
                fields = data[data.rfind(")") + 2:].split()
                if fields[0] != "Z" or int(fields[1]) != me:
                    continue
                os.waitpid(int(name), os.WNOHANG)
            except (OSError, ValueError, IndexError):
                continue

    def _kill_exec_descendants(self):
        """Linux: end every program an exec started that is still around (found by the environment marker every exec'd
        program and its descendants inherit), including one that left the process group or outlived its parent."""
        if not sys.platform.startswith("linux") or not os.path.isdir("/proc"):
            return
        marker = ("%s=%s" % (self.exec_env_name, self.exec_env_value)).encode("utf-8")
        with self.mu:
            known = set(self.children)
        try:
            names = os.listdir("/proc")
        except OSError:
            return
        killed = []
        for name in names:
            if not name.isdigit() or int(name) in known:
                continue
            try:
                with open("/proc/%s/environ" % name, "rb") as f:
                    environ = f.read()
                if marker in environ.split(b"\x00"):
                    os.kill(int(name), signal.SIGKILL)
                    killed.append(int(name))
            except (OSError, ValueError):
                continue
        # They may reparent here (subreaper) and turn into zombies: wait a moment so no pid is left behind.
        end = time.monotonic() + 2.0
        while killed and time.monotonic() < end:
            still = []
            for pid in killed:
                try:
                    if os.waitpid(pid, os.WNOHANG)[0]:
                        continue
                except ChildProcessError:
                    pass
                except OSError:
                    continue
                if os.path.exists("/proc/%d" % pid):
                    still.append(pid)
            killed = still
            if killed:
                time.sleep(0.02)

    def _child_env(self, env):
        """The environment for an exec'd program: its own (a desk's, or this process's) plus the tree marker."""
        child = dict(os.environ if env is None else env)
        child[self.exec_env_name] = self.exec_env_value
        return child

    def _check_identity(self):
        """The account is the one setup accepted, and not root (the shared desk host is the one documented exception):
        checked again at every execution gate, not only at setup."""
        if not hasattr(os, "geteuid"):
            return
        euid = os.geteuid()
        if euid == 0:
            if self._shared():
                return   # the one documented exception: a desk host for other people, root with every desk sandboxed
            raise Fault("not_allowed", "the computer plugin never runs as root: run the Node as your own account")
        if self._euid is not None and euid != self._euid:
            raise Fault("not_allowed", "the computer plugin's account changed: run the Node as your own account")

    def _gate_generation(self):
        return getattr(self._gate, "generation", None)

    def _generation_changed(self):
        gen = self._gate_generation()
        return gen is not None and gen != self.generation

    def _deadline_clock(self):
        """The clock a command's deadline is counted on: the runtime's (a deadline is an instant on it, taken from the
        moment its line arrived). The plugin's own clock may be another one (a test's, a host's), and a deadline read
        on the wrong clock means nothing: on a machine whose uptime is short (a fresh CI runner) it looks passed long
        before it was, and the work it belongs to never runs."""
        rt = getattr(self, "runtime", None)
        return getattr(rt, "clock", None) or self.clock

    def _authorized(self, run, expires=None, generation=None, clock=None):
        """Wrap work that was authorized now or by an approval: at the moment it starts it checks, under the lock that
        a stop takes to raise the generation, that no stop happened and its deadline has not passed. The deadline is
        read on the clock it was counted on (clock): a command's deadline on the runtime's, an approval's on the
        plugin's own."""
        gen = self.generation if generation is None else generation
        now = clock or self.clock

        def go():
            self._gate.generation = gen
            try:
                with self.mu:
                    if gen != self.generation:
                        raise Fault("local_stop", "stopped on the machine before it started")
                    if expires is not None and now() >= expires:
                        raise Fault("expired", "its deadline passed before it started")
                self._check_identity()
                return run()
            finally:
                try:
                    del self._gate.generation
                except AttributeError:
                    pass
        return go

    def _spawn(self, argv, kind=None, **kwargs):
        """Start a program, registered in the same critical section as the stop generation check: a stop either sees
        it (and kills it) or the spawn sees the newer generation (and does not start)."""
        with self.mu:
            if self._generation_changed():
                raise Fault("local_stop", "stopped on the machine before it started")
            proc = self.popen(argv, **kwargs)
            self.procs.add(proc)
            pid = getattr(proc, "pid", None)
            if pid is not None:
                self.groups.add(pid)
                self.children.add(pid)
            if kind == "input":
                self.inputs.add(proc)
            elif kind == "capture":
                self.captures.add(proc)
        return proc

    def _forget(self, proc, kind=None):
        with self.mu:
            self.procs.discard(proc)
            pid = getattr(proc, "pid", None)
            if pid is not None:
                self.children.discard(pid)
                if not self._group_alive(pid):
                    self.groups.discard(pid)
            if kind == "input":
                self.inputs.discard(proc)
            elif kind == "capture":
                self.captures.discard(proc)

    def _input_guard_locked(self):
        """Whether the agent may be driving the mouse or keyboard right now: while an input process runs (until it has
        ended, not until the core answered) and for INPUT_GUARD_S after. Call with self.mu held."""
        if self.input_busy > 0 or self.inputs:
            return True
        try:
            return self.input_ended is not None and self.clock() - self.input_ended < INPUT_GUARD_S
        except TypeError:
            return True

    def poll(self):
        now = self.clock()
        b = self.browser
        if b is not None and b.alive() and b.used_at and time.monotonic() - b.used_at > self.browser_idle_s:
            self._close_browser()
        # A desk no command used for desk_idle_s is stopped: an agent that crashed or restarted mid-task never ran its
        # own stop, and a desk left running would hold its share of the machine and its browser's sessions.
        for name in self.desks.running_names():
            if now - self.desk_used.get(name, now) > self.desk_idle_s:
                self._end_desk_browser(name)
                self._end_desk_programs(name)
                self.desks.stop(name)
                self.desk_used.pop(name, None)
                self.emit_event("desk_idle_stopped", desk=name, idle_s=self.desk_idle_s)
                self.log("desk %s stopped after %d s without a command", name, self.desk_idle_s)
            elif name not in self.desk_used:
                self.desk_used[name] = now
        expired = []
        with self.mu:
            for cid, (_, at, _) in list(self.pending.items()):
                if now >= at:
                    expired.append(cid)
                    del self.pending[cid]
            waiting, running = len(self.pending), self.running
            for pid in list(self.groups):
                if not self._group_alive(pid):
                    self.groups.discard(pid)
        self._reap_orphans()
        for cid in expired:
            self._close_prompt(cid)
            self.emit_event("approval_done", id=cid, approved=False, reason="expired")
            self.fail(cid, "not_allowed", "nobody answered on the machine in time")
        # Sensors are what the core forwards to Bot: the machines page shows the level and what waits on the machine.
        streaming = self.stream is not None
        return {"sensors": {"computer_level": self.effective_level(), "computer_waiting": waiting, "computer_running": running,
                            "computer_screen_stream": "live" if streaming else "off",
                            "computer_desks": len(self.desks.running_names())}}

    # ---- paths -----------------------------------------------------------------------------------------------------
    def _node_control_dirs(self):
        """The Node's own config and state directories, as it passes them to the plugin in its environment."""
        dirs = []
        for key in ("OPENVIBE_NODE_CONFIG", "OPENVIBE_NODE_STATE"):
            value = self.env.get(key)
            if isinstance(value, str) and value.strip():
                real = os.path.realpath(os.path.expanduser(value.strip()))
                if real not in dirs:
                    dirs.append(real)
        return dirs

    def _node_control_files(self):
        """The control files inside those directories, for the hard-link aliases a path check cannot see."""
        files = []
        for d in self._control_dirs:
            for name in CONTROL_FILES:
                files.append(os.path.join(d, name))
        return files

    def _root_inside(self, directory):
        return any(r == directory or r.startswith(directory.rstrip(os.sep) + os.sep) for r in self.roots)

    @staticmethod
    def _control_name(name):
        """A control file, or one of its own temporaries (`latch.json.tmp`)."""
        return any(name == f or name.startswith(f + ".") for f in CONTROL_FILES)

    def _is_control(self, real):
        """Whether a resolved path is one of the Node's control files or lives in its control directories. This holds
        at every level, full included. When a configured root is inside a control directory (the per-user --home
        layout, roots like <home>/work), the directory itself stays usable and only its control entries are off: the
        control files, their temporaries, and the state's plugins/venv/frames folders."""
        norm = os.path.normcase(real)
        for d in self._control_dirs:
            control = os.path.normcase(d)
            if norm == control:
                return True
            if not norm.startswith(control + os.sep):
                continue
            if not self._root_inside(d):
                return True
            parts = norm[len(control) + 1:].split(os.sep)
            if parts[0] in CONTROL_STATE_PARTS:
                return True
            if len(parts) == 1 and self._control_name(parts[0]):
                return True
        try:
            got = os.stat(real)
        except OSError:
            return False
        for path in self.control_files:
            try:
                control = os.stat(path)
            except OSError:
                continue
            if (got.st_dev, got.st_ino) == (control.st_dev, control.st_ino):
                return True
        return False

    def _inside(self, path):
        return any(path == r or path.startswith(r.rstrip(os.sep) + os.sep) for r in self.roots)

    def _files(self, level):
        """The folders this machine allows, and the handles they are reached through."""
        return Files(self.roots, self.root_forms, level, self._handles)

    def _control_guard(self, st, path):
        """The object the plugin is about to use is one of the Node's own control files or folders.

        The name may be a link or another name for the same bytes, so the object itself is compared, not the name.
        """
        for control in self.control_files:
            try:
                other = os.stat(control)
            except OSError:
                continue
            if (st.st_dev, st.st_ino) == (other.st_dev, other.st_ino):
                raise Fault("not_allowed", "%s is one of the Node's own control files" % path)
        for control in self._control_dirs:
            try:
                other = os.stat(control)
            except OSError:
                continue
            if (st.st_dev, st.st_ino) == (other.st_dev, other.st_ino):
                raise Fault("not_allowed", "%s is one of the Node's own control folders" % path)

    def _path(self, raw, level, create="no"):
        """The path a command names: inside the roots unless the level is full, never the Node's control files.

        On Linux the name is resolved through the folders' handles (openat2, or one component at a time where
        the kernel has no openat2), so what the plugin later uses is the object the handle points at and not
        the name that was checked. macOS and Windows keep the name checks they always had.
        """
        if not isinstance(raw, str) or not raw or len(raw) > MAX_ARG_LEN or "\x00" in raw:
            raise Fault("bad_value", "path is a non-empty string")
        path = os.path.expanduser(raw)
        if not os.path.isabs(path):
            raise Fault("bad_value", "path must be absolute")
        target = self._files(level).target(os.path.normpath(path), level, create=create, guard=self._control_guard)
        # The Node's own files are refused by the name the agent asked for and by the name it resolves to: on macOS
        # the folder the Node runs in is reached through a link, so only the resolved name says where it is.
        if self._is_control(target.requested) or self._is_control(target.path):
            raise Fault("not_allowed", "%s is one of the Node's own control files or folders" % target.path)
        target.check_now(create)
        return target

    # ---- commands --------------------------------------------------------------------------------------------------
    def handle(self, cmd):
        # Every command is an execution gate: the account must still be the one setup accepted.
        self._check_identity()
        v = cmd.value
        if not isinstance(v, dict):
            raise Fault("bad_value", "value must be an object")
        level = self.effective_level()
        if self._shared():
            self._shared_check(cmd.kind, v)
        if cmd.kind == "desk":
            return self._desk_op(cmd, v, level)
        desk_env = self._desk_env(v) if cmd.kind in ("screen", "screen_stream", "input", "browser") else None
        desk_name = v.get("desk") if desk_env else None
        if desk_name:
            self.desk_used[desk_name] = self.clock()
        page = bool(desk_name) and self.desks.kind(desk_name) == "browser"   # a browser desk: its page is its screen
        if cmd.kind == "screen" and page:
            width = _int(v.get("max_width"), "max_width", 320, 3840, 1280)
            return self._start(cmd, lambda: self._page_shot(desk_name, width))
        if cmd.kind == "screen":
            tool = self._x11_tool() if desk_env else self._screen_tool()
            if not tool:
                raise Fault("unsupported", "no screen capture tool on this machine")
            width = _int(v.get("max_width"), "max_width", 320, 3840, 1280)
            return self._start(cmd, lambda: self._screen(tool, width, env=desk_env))
        if cmd.kind == "screen_stream":
            return self._screen_stream(v, env=desk_env, desk=desk_name, page=page)
        if cmd.kind == "desk.stream":
            return self._desk_stream_op(cmd, v, level)
        if cmd.kind == "fs":
            op = v.get("op")
            if op in READ_OPS:
                return self._fs_read_op(op, v, level)
            if op not in WRITE_OPS:
                raise Fault("bad_value", "op is list, stat, read, write or mkdir")
            run, summary = self._fs_write_op(op, v, level)
            return self._act(cmd, level, run, summary, threaded=False)
        if cmd.kind == "probe":
            if not self.allow_probe:
                raise Fault("unsupported", "this machine does not allow probes")
            target, timeout = self._probe_target(v)
            return self._start(cmd, lambda: self._probe(target, timeout))
        if cmd.kind == "input":
            if not self.allow_input:
                raise Fault("unsupported", "this machine does not allow mouse and keyboard input")
            if page:
                tool = "page"
            else:
                tool = ("xdotool" if self.which("xdotool") else None) if desk_env else self._input_tool()
            if not tool:
                raise Fault("unsupported", "no input tool on this machine (xdotool on X11, cliclick on macOS)")
            run, summary = self._input_op(tool, v, env=desk_env, desk=desk_name)
            return self._act(cmd, level, self._driving(run), summary, threaded=True)
        if cmd.kind == "browser":
            run, summary, acting = self._browser_op(v, level, env=desk_env, desk=desk_name)
            if acting:
                return self._act(cmd, level, run, summary, threaded=True)
            return self._start(cmd, run)
        if cmd.kind == "exec":
            if not self.allow_exec:
                raise Fault("unsupported", "this machine does not allow running programs")
            run, summary = self._exec_op(cmd, v, level)
            return self._act(cmd, level, run, summary, threaded=True)
        raise Fault("unsupported", "kind %s not supported" % cmd.kind)

    def _shared_check(self, kind, v):
        """On a desk host for other people: browser desks (started without a kept profile) and the commands that act on
        one; never the machine's own screen, browser, files, programs or network."""
        if kind == "desk":
            if v.get("action") == "start" and v.get("kind", "desktop") != "browser":
                raise Fault("unsupported", "this desk host runs browser desks only (kind browser)")
            if v.get("action") == "start" and v.get("profile") is not None:
                raise Fault("unsupported", "this desk host keeps no browser profiles: a desk's sign-ins end with it")
            return
        if kind in SHARED_DESK_KINDS and v.get("desk", "main") != "main":
            return
        if kind in ("screen_stream", "desk.stream"):
            raise Fault("not_allowed", "this desk host shows a desk as pictures (screen), not live: its one stream is its operator's")
        raise Fault("not_allowed", "this machine is a desk host for other people: only its browser desks take commands")

    def _sandbox_argv(self, binary):
        """The `openvibe-node desk-sandbox … --` a desk's browser runs under on a desk host for other people, else None."""
        if not self._shared():
            return None
        node = self.env.get("OPENVIBE_NODE_EXE") or self.which("openvibe-node")
        if not node:
            raise Fault("unsupported", "openvibe-node was not found to sandbox the desk (the Node sets OPENVIBE_NODE_EXE)")
        real = os.path.realpath(binary)
        binds = []
        if binary.startswith("/snap/") or os.path.basename(real) == "snap":
            raise Fault("unsupported", "a sandboxed desk's browser cannot be a snap: install Google Chrome's .deb (or Chromium from a .deb)")
        if real.startswith(("/opt/", "/usr/local/")):
            binds.append(os.path.dirname(real))   # the install directory, read-only; the rest of /usr is the system's
        elif not real.startswith("/usr/"):
            raise Fault("unsupported", "a sandboxed desk's browser is installed under /usr, /opt or /usr/local (%s is not: "
                                       "a snap cannot run there)" % real)
        if os.path.isdir("/etc/fonts"):
            binds.append("/etc/fonts")
        lim = self.desk_limits
        argv = [node, "desk-sandbox", "--memory-mb", str(lim["memory_mb"]), "--cpu-millis", str(lim["cpu_millis"]),
                "--disk-mb", str(lim["disk_mb"]), "--pids", str(lim["pids"])]
        if self.desk_egress == "public":
            argv += ["--egress", "public"]
        for b in binds:
            argv += ["--bind", b]
        for kv in SANDBOX_ENV:
            argv += ["--setenv", kv]
        return argv + ["--"]

    def _new_browser(self, binary, profile, headless, env=None):
        sandbox = self._sandbox_argv(binary)
        if sandbox:
            binary = os.path.realpath(binary)   # the sandbox shows the install directory, not the links that lead to it
        return Browser(binary, profile, headless, env=env, extra_args=self._browser_args(), sandbox=sandbox)

    # ---- the private desk --------------------------------------------------------------------------------------------
    def _desk_env(self, v):
        """None for the owner's own screen (desk main, the default); a desk's environment for private or desk-<name>."""
        where = v.get("desk", "main")
        if where == "main":
            return None
        Desks.check(where)
        if not self.allow_desk:
            raise Fault("unsupported", "this machine does not allow a private desk (allow_desk is off)")
        return self.desks.env(where)

    def _desk_op(self, cmd, v, level):
        action = v.get("action")
        name = v.get("desk", "private")
        if action == "list":
            return self.desks.list()
        Desks.check(name)
        if action == "status":
            return self.desks.status(name)
        if action not in ("start", "stop"):
            raise Fault("bad_value", "action is start, stop, status or list")
        self.desk_used[name] = self.clock()
        if not self.allow_desk:
            raise Fault("unsupported", "this machine does not offer desks (allow_desk is off)")
        what = "a private desk" if name == "private" else "desk %s" % name
        if action == "start":
            kind = v.get("kind", "desktop")
            if kind not in ("desktop", "browser"):
                raise Fault("bad_value", "kind is desktop or browser")
            kinds = self._desk_kinds()
            if kind not in kinds:
                if kind == "browser":
                    raise Fault("unsupported", "a browser desk needs Chromium, Chrome, Edge or Brave on this machine")
                raise Fault("unsupported", "a whole desk needs Linux with Xvfb%s" % (": this machine runs browser desks (kind browser)" if kinds else ""))
            width = _int(v.get("width"), "width", 640, 3840, 1280)
            height = _int(v.get("height"), "height", 480, 2160, 800)
            profile = v.get("profile")
            if profile is not None and (not isinstance(profile, str) or not PROFILE_NAME.match(profile)):
                raise Fault("bad_value", "profile is a name: lower-case letters, digits and dashes, at most 24 characters")
            if profile is not None and name == "private":
                raise Fault("bad_value", "the private desk keeps its own profile: name a desk desk-<name> to start one from a kept profile")
            if kind == "browser":
                what = "a browser desk" if name == "private" else "browser desk %s" % name

            def start():
                self._check_identity()
                st = self.desks.start(name, width, height, kind)
                with self.mu:
                    if self._generation_changed():
                        self.desks.stop(name)
                        raise Fault("local_stop", "stopped on the machine before the desk started")
                if profile and self.desk_profiles.get(name) != profile:
                    self.desk_profiles[name] = profile
                    self.desk_restored.discard(name)
                return dict(st, profile=profile) if profile else st
            return self._act(cmd, level, start,
                             "start %s (%d×%d) where your agent works without touching your screen%s"
                             % (what, width, height, ", in your kept browser profile %s" % profile if profile else ""),
                             threaded=True)

        def stop():
            self._check_identity()
            saved = self._end_desk_browser(name)
            self._end_desk_programs(name)
            out = self.desks.stop(name)
            return dict(out, profile_saved=saved) if saved is not None else out
        return self._act(cmd, level, stop, "stop %s (and close what runs on it)" % what, threaded=False)

    def _desk_browser(self, desk, binary=None, env=None):
        """The agent's browser on a desk, started on first use: shown on a whole desk's display, headless on a browser
        desk (its window the desk's size: its page is the desk's screen). Its own profile, downloads and temp folder."""
        with self.browser_mu:
            b = self.desk_browsers.get(desk)
            if b is not None and b.alive():
                self._set_downloads(b, desk)
                return b
            binary = binary or self._browser_binary()
            if not binary:
                raise Fault("unsupported", "no browser on this machine (Chromium, Chrome, Edge or Brave), or this machine does not allow it")
            env = env or self.desks.env(desk)
            page = self.desks.kind(desk) == "browser"
            if self._shared():   # its profile and downloads inside its sandbox, gone with the desk
                b = self.browser_factory(binary, SANDBOX_PROFILE, page, env=env)
                b.download_dir = SANDBOX_DOWNLOADS
                b.allow_downloads = False   # a desk host runs trusted or observe, never full
            else:
                profile = self._desk_profile_dir(desk)
                self._restore_profile(desk)
                b = self.browser_factory(binary, profile, page, env=env)
                kept = self._take_session_cookies(desk)
                if kept:
                    b.after_start = lambda: self._put_cookies(desk, b, kept)
                b.download_dir = self._desk_downloads(desk)
                b.allow_downloads = self.effective_level() == "full" and b.download_dir is not None
            with self.mu:
                if self._generation_changed():
                    b.close()
                    raise Fault("local_stop", "stopped on the machine before it started")
                self.desk_browsers[desk] = b
            if page:
                st = self.desks.status(desk)
                b.window = b.page_size = (st.get("width") or 1280, st.get("height") or 800)
            return b

    def _set_downloads(self, browser, desk=None):
        """Keep a running browser's download policy with the level: deny as soon as it is not full, and deny a desk
        whose downloads folder did not resolve inside the roots."""
        want = self.effective_level() == "full" and (desk is None or getattr(browser, "download_dir", None) is not None)
        if getattr(browser, "allow_downloads", None) != want:
            browser.allow_downloads = want
            if browser.alive() and hasattr(browser, "_apply_download_policy"):
                browser._apply_download_policy()

    def _desk_downloads(self, desk):
        """A desk's downloads folder, resolved: only when it is inside the roots (a symlink out of them is not used).
        None: the browser keeps its downloads denied."""
        path = self.desks.dir(desk, "downloads")
        if not path:
            return None
        real = os.path.realpath(path)
        return real if self._inside(real) else None

    def _desk_lock(self, desk):
        with self.browser_mu:
            return self.browser_locks.setdefault(desk, threading.Lock())

    def _on_page(self, desk, act):
        """act(browser) on a browser desk's page, one action at a time on that desk; a browser error is a fault."""
        b = self._desk_browser(desk)
        try:
            with self._desk_lock(desk):
                b.page()
                return act(b)
        except BrowserError as e:
            raise Fault(e.code, e.message)

    def _page_shot(self, desk, width):
        """A picture of a browser desk's page (not under its action lock: a look never waits for a click)."""
        b = self._desk_browser(desk)
        try:
            b.page()
            return b.screenshot(width)
        except BrowserError as e:
            raise Fault(e.code, e.message)

    def _desk_profile_dir(self, desk):
        return self.browser_profile + ("-desk" if desk == "private" else "-" + desk)

    def _kept_profile_dir(self, profile):
        return self.browser_profile + "-kept-" + profile

    def _profile_lock(self, profile):
        with self.mu:
            return self.profile_locks.setdefault(profile, threading.Lock())

    def _restore_profile(self, desk):
        """Before a desk's browser first starts in this run of the desk: its profile is a fresh copy of the kept one."""
        profile = self.desk_profiles.get(desk)
        if not profile or desk in self.desk_restored:
            return
        self.desk_restored.add(desk)
        own, kept = self._desk_profile_dir(desk), self._kept_profile_dir(profile)
        shutil.rmtree(own, ignore_errors=True)
        with self._profile_lock(profile):
            if os.path.isdir(kept):
                shutil.copytree(kept, own, symlinks=True, ignore=PROFILE_SKIP)
                os.chmod(own, 0o700)

    def _take_session_cookies(self, desk):
        """The session cookies a desk's kept profile brought (read once, then the file goes: it is written again on save)."""
        if desk not in self.desk_profiles:
            return None
        path = os.path.join(self._desk_profile_dir(desk), SESSION_COOKIES)
        try:
            with open(path) as f:
                cookies = json.load(f)
        except (OSError, ValueError):
            return None
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass
        return cookies if isinstance(cookies, list) else None

    def _put_cookies(self, desk, b, cookies):
        try:
            b.restore_cookies(cookies)
        except BrowserError as e:
            self.log("desk %s: its kept session cookies did not go back in: %s", desk, e.message)

    def _end_desk_browser(self, desk):
        """Close a desk's browser so it writes everything down, save its profile back when it started from a kept one
        (the last desk to stop wins), and remove the desk's own copy. → True when saved, False when not (too large or
        a failed copy), None for a desk without a kept profile."""
        b = self.desk_browsers.pop(desk, None)
        profile = self.desk_profiles.get(desk)
        if b is not None and profile and b.alive():
            # Chrome drops cookies that end with the browser when it quits, and a sign-in is often one: keep them.
            try:
                cookies = b.session_cookies()
                path = os.path.join(self._desk_profile_dir(desk), SESSION_COOKIES)
                fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                with os.fdopen(fd, "w") as f:
                    json.dump(cookies, f)
            except (BrowserError, OSError) as e:
                self.log("desk %s: its session cookies were not kept: %s", desk, getattr(e, "message", e))
        if b is not None:
            b.close(graceful=True)
        profile = self.desk_profiles.pop(desk, None)
        used = desk in self.desk_restored
        self.desk_restored.discard(desk)
        if not profile:
            return None
        own, kept = self._desk_profile_dir(desk), self._kept_profile_dir(profile)
        try:
            if not used or not os.path.isdir(own):
                return False
            size = _tree_size(own, PROFILE_SKIP)
            if size > self.desk_profile_max_mb * 1024 * 1024:
                self.emit_event("desk_profile_too_big", desk=desk, profile=profile, mb=size // (1024 * 1024))
                return False
            with self._profile_lock(profile):
                new, old = kept + ".new", kept + ".old"
                shutil.rmtree(new, ignore_errors=True)
                shutil.rmtree(old, ignore_errors=True)
                shutil.copytree(own, new, symlinks=True, ignore=PROFILE_SKIP)
                os.chmod(new, 0o700)
                if os.path.isdir(kept):
                    os.rename(kept, old)
                os.rename(new, kept)
                shutil.rmtree(old, ignore_errors=True)
            return True
        except OSError as e:
            self.log("desk %s: its profile %s was not saved: %s", desk, profile, e)
            return False
        finally:
            shutil.rmtree(own, ignore_errors=True)   # a copy for one run of the desk
            self._drop_empty_desk_dir(desk)

    def _drop_empty_desk_dir(self, desk):
        """A desk for one task leaves no empty folders behind (its downloads folder, then its own); a download stays."""
        if not self.desks.base_dir:
            return
        base = os.path.join(self.desks.base_dir, desk)
        for path in (os.path.join(base, "downloads"), base):
            try:
                os.rmdir(path)   # only when empty
            except OSError:
                return

    def _drop_desk_profiles(self):
        """The kill switch: nothing is saved, and the desks' own copies go."""
        for desk in list(self.desk_profiles):
            self.desk_profiles.pop(desk, None)
            self.desk_restored.discard(desk)
            shutil.rmtree(self._desk_profile_dir(desk), ignore_errors=True)

    def _close_desk_browser(self, name):
        b = self.desk_browsers.pop(name, None)
        if b is not None:
            b.close()

    def _close_desk_browsers(self):
        for name in list(self.desk_browsers):
            self._close_desk_browser(name)

    def _x11_tool(self):
        """An X11 capture tool for the private desk (it is always X11, whatever the owner's session is)."""
        if self.which("import"):
            return ("import", lambda out: ["import", "-window", "root", out])
        if self.which("scrot"):
            return ("scrot", lambda out: ["scrot", "-o", out])
        return None

    def _act(self, cmd, level, run, summary, threaded):
        self._check_identity()
        # This is the authorization instant: a stop after it (before the worker runs) must fail the check below.
        generation = self.generation
        if RANK[level] < RANK["ask"]:
            stopped = self.runtime is not None and self.runtime.estopped
            raise Fault("local_stop" if stopped else "not_allowed",
                        "stopped on the machine: observe only" if stopped else
                        "this machine allows observing only (its owner sets the level on the machine)")
        if level == "ask":
            # An approval never outlives the command it answers: whichever of the approval timeout and the request's
            # own deadline comes first. The request's deadline is counted on the runtime's clock and the approval's
            # expiry on the plugin's, so the time left on that deadline is what caps the approval.
            left = cmd.deadline - self._deadline_clock()()
            expires = self.clock() + min(self.approval_timeout_s, left)
            with self.mu:
                if cmd.id in self.pending:
                    raise Fault("not_ready", "an approval with id %s already waits on the machine" % _plain(cmd.id))
                if len(self.pending) >= MAX_WAITING:
                    raise Fault("not_ready", "%d requests already wait for approval on the machine" % MAX_WAITING)
                self.pending[cmd.id] = ((run, threaded), expires, summary)
            self.emit_event("approval", id=cmd.id, kind=cmd.kind, summary=summary,
                            expires_s=max(1, min(self.approval_timeout_s, int(round(expires - self.clock())))))
            self._notify(cmd.id, summary)
            self.log("waiting for approval %s: %s", cmd.id, summary)
            raise Deferred()
        self.log("%s %s: %s", cmd.kind, cmd.id, summary)
        if threaded:
            return self._start(cmd, run, generation=generation)
        with self._slot():
            return self._authorized(run, cmd.deadline, generation, clock=self._deadline_clock())()

    def _notify(self, cid, summary):
        """Tell the desktop that a request waits (config `notify`, on by default) when the Node runs in a graphical
        session and the Node's tray does not show it already (`openvibe-node tray`). On Linux the notification has
        Approve and Deny buttons when notify-send can show them (libnotify 0.7.10+); on macOS a dialog asks, with Deny
        as its default button. Best effort: it never blocks or fails a command."""
        if not self.notify or self.clock() < self.tray_until:
            return
        title = "OpenVibe: your agent asks"
        summary = _plain(summary)
        hint = "openvibe-node approve %s  (or deny %s)" % (shlex.quote(_plain(cid)), shlex.quote(_plain(cid)))
        parse = None
        if self.platform.startswith("linux") and (self.env.get("DISPLAY") or self.env.get("WAYLAND_DISPLAY")) and self.which("notify-send"):
            # A notification cannot show an arbitrarily long request; when it is longer than this surface can show in
            # full, it offers no button at all and points at the full view instead of approving a cut summary.
            if self._notify_can_act() and len(summary) <= NOTIFY_SUMMARY_MAX:
                argv = ["notify-send", "--app-name=OpenVibe Node", "--urgency=critical", "--wait",
                        "--expire-time=%d" % (self.approval_timeout_s * 1000), "--action=approve=Approve",
                        "--action=deny=Deny", "--", title, _markup(summary)]
                parse = _parse_notify_send
            elif len(summary) > NOTIFY_SUMMARY_MAX:
                argv = ["notify-send", "--app-name=OpenVibe Node", "--urgency=critical",
                        "--expire-time=%d" % (self.approval_timeout_s * 1000), "--", title,
                        _markup("This request is too long to show whole here. Run `%s` to read it and answer it.\n%s"
                                % (FULL_VIEW_HINT, hint))]
            else:
                argv = ["notify-send", "--app-name=OpenVibe Node", title, _markup("%s\n%s" % (summary, hint))]
        elif self.platform == "darwin" and self.which("osascript"):
            quote = lambda s: '"%s"' % s.replace("\\", "\\\\").replace('"', '\\"')
            if len(summary) <= DIALOG_SUMMARY_MAX:
                argv = ["osascript", "-e", 'display dialog %s with title %s buttons {"Deny", "Approve"} default button "Deny" '
                        "giving up after %d with icon caution" % (quote(summary), quote(title), self.approval_timeout_s)]
                parse = _parse_dialog
            else:
                argv = ["osascript", "-e", "display notification %s with title %s" % (
                    quote("This request is too long to show whole here. Run `%s` to read it and answer it." % FULL_VIEW_HINT),
                    quote(title))]
        else:
            return
        try:
            if parse is None:
                self.popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
                return
            proc = self.popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                              start_new_session=True, text=True)
        except OSError:
            return
        with self.mu:
            old = self.prompts.pop(cid, None)
            self.prompts[cid] = proc
        if old is not None:
            self._end_prompt(old)
        threading.Thread(target=self._await_prompt, args=(cid, proc, parse), name="computer-ask-%s" % cid, daemon=True).start()

    def _notify_can_act(self):
        """Whether this notify-send has --action and --wait (libnotify 0.7.10+), asked once."""
        if self.notify_actions is None:
            try:
                p = self.popen(["notify-send", "--help"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True)
                out = p.communicate(timeout=3)[0] or ""
                self.notify_actions = "--action" in out and "--wait" in out
            except (OSError, subprocess.SubprocessError, AttributeError):
                self.notify_actions = False
        return self.notify_actions

    def _await_prompt(self, cid, proc, parse):
        try:
            out = proc.communicate()[0] or ""
        except (OSError, ValueError, AttributeError):
            out = ""
        with self.mu:
            # Only the prompt instance that is the one shown now may answer: a replaced notification's late answer is
            # not the owner's answer to anything.
            current = self.prompts.get(cid) is proc
            if current:
                del self.prompts[cid]
        if not current:
            return
        answer = parse(out)
        if answer is not None:
            self._desktop_answer(cid, answer)

    def _desktop_answer(self, cid, approved):
        """The owner pressed Approve or Deny on the desktop. An approval that arrives while the agent drives the mouse or
        keyboard (or just after) may be the agent's own click: it is not taken, and the request is shown again."""
        with self.mu:
            item = self.pending.get(cid)
            driving = self._input_guard_locked()
        if item is None:
            return
        if approved and driving:
            self.log("an approval of %s came from the desktop while the agent drove the mouse or keyboard: asking again", cid)
            self._notify(cid, item[2])
            return
        with (self.runtime.lock if self.runtime is not None else contextlib.nullcontext()):
            self.on_approval(cid, approved)

    def _close_prompt(self, cid):
        """Take down the notification or dialog of a request answered elsewhere, expired or stopped."""
        with self.mu:
            proc = self.prompts.pop(cid, None)
        if proc is not None:
            self._end_prompt(proc)

    def _end_prompt(self, proc):
        try:
            if proc.poll() is None:
                proc.send_signal(signal.SIGINT)  # notify-send --wait closes its notification on SIGINT
        except (OSError, AttributeError):
            pass

    def _driving(self, run):
        """An input command, counted while it runs so desktop approvals wait it out (INPUT_GUARD_S)."""
        def go():
            with self.mu:
                self.input_busy += 1
            try:
                return run()
            finally:
                with self.mu:
                    self.input_busy -= 1
                    self.input_ended = self.clock()
        return go

    def on_tray(self, attached, ttl_s):
        try:
            ttl = max(1.0, min(float(ttl_s or 15), 60.0))
        except (TypeError, ValueError):
            ttl = 15.0
        self.tray_until = self.clock() + ttl if attached else 0.0

    def on_approval(self, cid, approved):
        with self.mu:
            item = self.pending.pop(cid, None)
            generation = self.generation   # the approval is the authorization; a later stop invalidates it
        if item is None:
            return
        self._close_prompt(cid)
        (work, expires, summary) = item
        run, threaded = work
        self.emit_event("approval_done", id=cid, approved=bool(approved))
        if not approved:
            self.log("denied %s: %s", cid, summary)
            self.fail(cid, "not_allowed", "the owner denied it on the machine")
            return
        try:
            self._check_identity()
        except Fault as f:
            self.fail(cid, f.code, f.message)
            return
        if self.effective_level() == "observe":
            self.fail(cid, "local_stop", "stopped on the machine before it ran")
            return
        with self.mu:
            expired = self.clock() >= expires
            driving = self._input_guard_locked()
        if expired:
            self.fail(cid, "expired", "its deadline passed before it was approved")
            return
        if driving:
            # The answer may be the agent's own keystroke: an input process is still driving (or had just ended), so
            # no approved effect runs until the guard is clear.
            self.fail(cid, "not_ready", "the agent is driving the mouse or keyboard right now")
            return
        self.log("approved %s: %s", cid, summary)
        gated = self._authorized(run, expires, generation)
        if threaded:
            try:
                self._thread(cid, gated)
            except Fault as f:
                self.fail(cid, f.code, f.message)
            return
        try:
            with self._slot():
                self.complete(cid, gated())
        except Fault as f:
            self.fail(cid, f.code, f.message)
        except Exception as e:
            self.fail(cid, "hardware", str(e))

    def _reserve(self):
        with self.mu:
            if self.running >= MAX_RUNNING:
                raise Fault("not_ready", "%d commands are already running on this machine" % MAX_RUNNING)
            self.running += 1

    def _release(self):
        with self.mu:
            self.running = max(0, self.running - 1)

    @contextlib.contextmanager
    def _slot(self):
        """One of the MAX_RUNNING slots, taken atomically: _start and an approved request take the same one."""
        self._reserve()
        try:
            yield
        finally:
            self._release()

    def _start(self, cmd, run, generation=None):
        self._thread(cmd.id, self._authorized(run, cmd.deadline, generation, clock=self._deadline_clock()))
        raise Deferred()

    def _thread(self, cid, run):
        self._reserve()   # raises not_ready to the caller before anything is started

        def work():
            try:
                self.complete(cid, run())
            except Fault as f:
                self.fail(cid, f.code, f.message)
            except Exception as e:
                self.fail(cid, "hardware", str(e))
            finally:
                self._release()

        threading.Thread(target=work, name="computer-%s" % cid, daemon=True).start()

    # ---- exec ------------------------------------------------------------------------------------------------------
    def _exec_summary(self, argv, cwd, stdin, desk, detached=False):
        """What the owner is asked to run: the full argv, the program it resolves to, the cwd, stdin and the fact that
        it runs with everything the account can reach, not only the folders."""
        program = argv[0]
        if os.sep in program or (os.altsep and os.altsep in program):
            # A path with a separator is resolved by the child against its cwd, so show the file it would actually run.
            resolved = os.path.realpath(program if os.path.isabs(program) else os.path.join(cwd or os.getcwd(), program))
        else:
            found = self.which(program)
            resolved = os.path.realpath(found) if found else "%s (not found on PATH)" % _plain(program)
        account = "%s with everything that account can reach, not only the folders %s" % (
            _account_name(), ", ".join(_plain(r) for r in self.roots) or "allowed")
        if detached:
            where = "the private desk" if desk == "private" else "desk %s" % desk
            return ("start %s on %s (it keeps running until the desk stops); executable %s; cwd %s; stdin none; "
                    "runs as %s" % (_argv_text(argv), where, _plain(resolved), _plain(cwd or "none"), account))
        where = "" if desk == "main" else (" on the private desk" if desk == "private" else " on desk %s" % desk)
        if stdin is None:
            cinfo = "none"
        else:
            data = stdin.encode("utf-8")
            cinfo = "%d bytes, sha256 %s" % (len(data), _digest(data))
        return ("run %s; executable %s; cwd %s%s; stdin %s; runs as %s"
                % (_argv_text(argv), _plain(resolved), _plain(cwd or "none"), where, cinfo, account))

    @staticmethod
    def _child_preexec(cwd_fd=None):
        """In the child: its own process group/session, the folder it runs in, and no privilege gain on Linux."""
        def preexec():
            os.setsid()
            if sys.platform.startswith("linux"):
                _set_no_new_privs()
            if cwd_fd is not None:
                os.fchdir(cwd_fd)   # the folder the parent checked, not a name that may have changed
        return preexec

    @staticmethod
    def _session_options(cwd_fd=None):
        """A new session/group for an exec'd program (Windows: its own process group, no preexec_fn there)."""
        if os.name == "nt":
            return {"creationflags": getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)}
        return {"preexec_fn": Computer._child_preexec(cwd_fd)}

    def _cwd_options(self, cwd):
        """How a program is started in a folder: the handle of the folder, checked again at this moment.

        A folder that was swapped since it was checked is refused; the program never runs in a name.
        """
        if cwd is None:
            return {}
        if cwd.by_name:
            return {"cwd": cwd.path}
        fd = cwd.open_dir()
        return {"cwd": None, "pass_fds": (fd,), "_cwd_fd": fd}

    def _exec_op(self, cmd, v, level):
        argv = v.get("argv")
        if not isinstance(argv, list) or not argv or len(argv) > MAX_ARGS or \
                not all(isinstance(a, str) and len(a) <= MAX_ARG_LEN and "\x00" not in a for a in argv) or not argv[0]:
            raise Fault("bad_value", "argv is a list of 1 to %d strings: a program and its arguments" % MAX_ARGS)
        if not _no_new_privs_available():
            raise Fault("unsupported", "no_new_privs is unavailable on this machine: running a program could gain a privilege")
        cwd = self._path(v["cwd"], level) if v.get("cwd") is not None else self._default_cwd(level)
        if cwd is not None:
            cwd.remember()   # what it is now: a folder that is swapped later is refused
        stdin = v.get("stdin")
        if stdin is not None and (not isinstance(stdin, str) or len(stdin.encode("utf-8")) > MAX_STDIN):
            raise Fault("bad_value", "stdin is a string of at most 64 KB")
        timeout = _int(v.get("timeout_s"), "timeout_s", 1, self.timeout_s, self.timeout_s)
        deadline = cmd.deadline
        desk = v.get("desk", "main")
        detach = v.get("detach", False)
        if not isinstance(detach, bool):
            raise Fault("bad_value", "detach is true or false")
        env = None
        if desk != "main":
            env = self._desk_env(v)   # checks the name and that the desk runs
            self.desk_used[desk] = self.clock()
        if detach:
            if desk == "main":
                raise Fault("not_allowed", "detach is for a desk only: a program left running on your own session is never started")
            if self.desks.kind(desk) == "browser":
                raise Fault("unsupported", "a browser desk has no screen for a program's windows: start a whole desk (kind desktop)")
            if stdin is not None:
                raise Fault("bad_value", "a detached program takes no stdin")
            return (lambda: self._start_on_desk(argv, cwd, env, desk),
                    self._exec_summary(argv, cwd.path if cwd is not None else None, None, desk, detached=True))

        def run():
            # The time left on the command's deadline, counted on the clock that deadline is on.
            left = deadline - self._deadline_clock()() - 1.0
            if left < 1:
                raise Fault("expired", "no time was left to run it")
            out = self._exec(argv, cwd, stdin, min(timeout, left), env=env)
            # The desk stream's environment-print detector sees the output before it leaves the machine.
            self._desk_stream_saw_output(argv, out)
            return out

        return run, self._exec_summary(argv, cwd.path if cwd is not None else None, stdin, desk)

    def _default_cwd(self, level):
        """The folder a program runs in when the request names none: the first root, as a handle."""
        if not self.roots:
            return None
        return self._files(level).target(self.roots[0], level)

    def _start_on_desk(self, argv, cwd, env, desk):
        """A program that keeps running on a desk (its windows there), ended when the desk stops."""
        self._check_identity()
        running = [p for p in self.desk_programs.get(desk, []) if p.poll() is None]
        if len(running) >= MAX_DESK_PROGRAMS:
            raise Fault("not_ready", "%d programs already run on this desk: stop the desk, or close one there" % len(running))
        options = self._cwd_options(cwd)
        fd = options.pop("_cwd_fd", None)
        try:
            try:
                proc = self._spawn(argv, env=self._child_env(env), stdin=subprocess.DEVNULL,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                   **options, **self._session_options(fd))
            except FileNotFoundError:
                raise Fault("bad_value", "no program %s on this machine" % argv[0])
            except PermissionError:
                raise Fault("not_allowed", "%s may not be run by the Node's account" % argv[0])
        finally:
            if fd is not None:
                os.close(fd)
        self.desk_programs[desk] = running + [proc]
        return {"started": True, "pid": proc.pid, "desk": desk}

    def _end_desk_programs(self, desk=None):
        """End what was started on a desk with detach (every desk's, without a name), each by its own handle."""
        names = [desk] if desk else list(self.desk_programs)
        for name in names:
            for proc in self.desk_programs.pop(name, []):
                if proc.poll() is None:
                    self._kill(proc)

    def _exec(self, argv, cwd, stdin, timeout, env=None):
        self._check_identity()
        started = self.clock()
        options = self._cwd_options(cwd)
        fd = options.pop("_cwd_fd", None)
        try:
            try:
                proc = self._spawn(argv, stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self._child_env(env),
                                   **options, **self._session_options(fd))
            except FileNotFoundError:
                raise Fault("bad_value", "no program %s on this machine" % argv[0])
            except PermissionError:
                raise Fault("not_allowed", "%s may not be run by the Node's account" % argv[0])
        finally:
            if fd is not None:
                os.close(fd)
        cap = self.max_output
        got = {"out": [b"", 0], "err": [b"", 0]}

        def drain(stream, key):
            buf = got[key]
            while True:
                chunk = stream.read(65536)
                if not chunk:
                    return
                buf[1] += len(chunk)
                if len(buf[0]) < cap:
                    buf[0] += chunk[:cap - len(buf[0])]

        readers = [threading.Thread(target=drain, args=(proc.stdout, "out"), daemon=True),
                   threading.Thread(target=drain, args=(proc.stderr, "err"), daemon=True)]
        for t in readers:
            t.start()
        if stdin is not None:
            try:
                proc.stdin.write(stdin.encode("utf-8"))
                proc.stdin.close()
            except (BrokenPipeError, OSError):
                pass
        timed_out = False
        try:
            code = proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            self._kill(proc)
            code = proc.wait()
        finally:
            # The process group stays in self.groups when a descendant outlived the parent: stop kills it.
            self._forget(proc)
        for t in readers:
            t.join(2)
        if not any(t.is_alive() for t in readers):
            for stream in (proc.stdout, proc.stderr, proc.stdin):
                if stream is not None:
                    try:
                        stream.close()
                    except OSError:
                        pass
        else:
            # A descendant still holds the pipe open: closing the file object here would wait on the reader's lock
            # forever. The reader ends (and the object is collected) when the last writer closes it.
            self._reap_orphans()
        text = lambda b: b.decode("utf-8", "replace")
        return {"exit_code": code, "stdout": text(got["out"][0]), "stderr": text(got["err"][0]),
                "truncated": got["out"][1] > cap or got["err"][1] > cap, "timed_out": timed_out,
                "duration_ms": int((self.clock() - started) * 1000)}

    # ---- probe (the node carrier of OpenVibe.Watch) -----------------------------------------------------------------
    @staticmethod
    def _lan(addr):
        ip = ipaddress.ip_address(addr)
        if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped:
            ip = ip.ipv4_mapped
        return ip.is_private or ip.is_loopback or ip.is_link_local

    def _probe_target(self, v):
        raw = v.get("target")
        if not isinstance(raw, str) or len(raw) > 2048:
            raise Fault("bad_value", "target is http(s)://host/path or tcp://host:port")
        u = urllib.parse.urlsplit(raw)
        if u.scheme not in ("http", "https", "tcp") or not u.hostname or u.username or u.password:
            raise Fault("bad_value", "target is http(s)://host/path or tcp://host:port, with no credentials")
        try:
            port = u.port or {"http": 80, "https": 443}.get(u.scheme)
        except ValueError:
            raise Fault("bad_value", "the port is not a number")
        if not port:
            raise Fault("bad_value", "a tcp target names its port")
        timeout = _int(v.get("timeout_s"), "timeout_s", 1, 30, 10)
        return {"scheme": u.scheme, "host": u.hostname, "port": port, "path": (u.path or "/") + ("?" + u.query if u.query else ""), "raw": raw}, timeout

    def _resolve_lan(self, host, port):
        """The first address of host, when every address it resolves to is on this machine's own network."""
        try:
            infos = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
        except socket.gaierror as e:
            raise Fault("bad_value", "%s does not resolve: %s" % (host, e))
        addrs = [i[4][0] for i in infos]
        if not addrs or not all(self._lan(a) for a in addrs):
            raise Fault("not_allowed", "a probe reaches this machine's own network only (private, loopback or link-local addresses); %s is not" % host)
        return infos[0][0], addrs[0]

    def _probe(self, t, timeout):
        family, addr = self._resolve_lan(t["host"], t["port"])
        started = self.clock()
        ms = lambda: int((self.clock() - started) * 1000)
        if t["scheme"] == "tcp":
            try:
                with socket.socket(family, socket.SOCK_STREAM) as s:
                    s.settimeout(timeout)
                    s.connect((addr, t["port"]))
                return {"open": True, "ms": ms()}
            except OSError as e:
                return {"open": False, "ms": ms(), "error": str(e)[:200]}
        raw = socket.create_connection((addr, t["port"]), timeout=timeout)
        try:
            if t["scheme"] == "https":
                raw = ssl.create_default_context().wrap_socket(raw, server_hostname=t["host"])
            conn = http.client.HTTPConnection(t["host"], t["port"], timeout=timeout)
            conn.sock = raw
            conn.request("GET", t["path"], headers={"User-Agent": "OpenVibe-Node-probe/1", "Accept": "*/*", "Connection": "close"})
            res = conn.getresponse()
            cap = min(self.max_output, 64 * 1024)
            body = res.read(cap + 1)
            return {"status": res.status, "content_type": res.getheader("Content-Type"), "etag": res.getheader("ETag"),
                    "last_modified": res.getheader("Last-Modified"), "location": res.getheader("Location"),
                    "body": body[:cap].decode("utf-8", "replace"), "truncated": len(body) > cap, "ms": ms()}
        except ssl.SSLError as e:
            raise Fault("hardware", "TLS failed: %s" % str(e)[:200])
        except (OSError, http.client.HTTPException) as e:
            raise Fault("hardware", "the probe failed: %s" % str(e)[:200])
        finally:
            try:
                raw.close()
            except OSError:
                pass

    # ---- files -----------------------------------------------------------------------------------------------------
    @staticmethod
    def _kind(mode):
        if stat.S_ISDIR(mode):
            return "dir"
        if stat.S_ISREG(mode):
            return "file"
        if stat.S_ISLNK(mode):
            return "link"
        return "other"

    def _fs_read_op(self, op, v, level):
        target = self._path(v.get("path"), level)
        st = target.stat()
        if op == "stat":
            return {"path": target.path, "type": self._kind(st.st_mode), "size": st.st_size, "mtime": int(st.st_mtime),
                    "mode": oct(stat.S_IMODE(st.st_mode))}
        if op == "list":
            if not stat.S_ISDIR(st.st_mode):
                raise Fault("bad_value", "%s is not a folder" % target.path)
            entries, total = target.list(MAX_LIST)
            return {"path": target.path, "entries": entries, "total": total, "truncated": total > len(entries)}
        if not stat.S_ISREG(st.st_mode):
            raise Fault("bad_value", "%s is not a file" % target.path)
        offset = _int(v.get("offset"), "offset", 0, 2 ** 53, 0)
        length = _int(v.get("length"), "length", 0, self.max_read, self.max_read)
        data, size = target.read(offset, length)
        self._desk_stream_saw_path(target.path)
        return {"path": target.path, "size": size, "offset": offset, "length": len(data),
                "truncated": offset + len(data) < size, "content_b64": base64.b64encode(data).decode("ascii")}

    def _fs_write_op(self, op, v, level):
        target = self._path(v.get("path"), level, create="any" if op == "mkdir" else "parent")
        if op == "mkdir":
            def run():
                self._check_identity()
                created = target.mkdir()
                return {"path": target.path, "created": created}
            return run, "make the folder %s" % _plain(target.path)
        mode = v.get("mode") or "create"
        if mode not in ("create", "overwrite", "append"):
            raise Fault("bad_value", "mode is create, overwrite or append")
        raw = v.get("content_b64")
        if not isinstance(raw, str):
            raise Fault("bad_value", "content_b64 is base64 text")
        try:
            data = base64.b64decode(raw, validate=True)
        except (ValueError, TypeError):
            raise Fault("bad_value", "content_b64 is not base64")
        if len(data) > self.max_read:
            raise Fault("bad_value", "at most %d bytes per write" % self.max_read)

        def run():
            self._check_identity()
            target.write(data, mode)
            return {"path": target.path, "written": len(data), "mode": mode}

        verb = {"create": "create", "overwrite": "overwrite", "append": "append to"}[mode]
        return run, "%s %s (mode %s, %d bytes, sha256 %s)" % (
            verb, _plain(target.path), mode, len(data), _digest(data))

    # ---- input -----------------------------------------------------------------------------------------------------
    # ---- browser ---------------------------------------------------------------------------------------------------
    def _browser_binary(self):
        if not self.allow_browser or self.platform.startswith("win"):
            return None
        return find_binary(self.which, self.platform, self.browser_path)

    def _egress_blocked(self, ip, port):
        """Whether the egress guard refuses a connection: an address that is not on the public internet (the machine's
        own network, loopback, link-local, ...), below level full; nothing when browser_egress is open."""
        if self.browser_egress == "open" or self.effective_level() == "full":
            return False
        return not_public(ip)

    def _browser_args(self):
        if self._shared():   # the sandbox's own network rules, not a proxy on 127.0.0.1 (another namespace there)
            return list(SANDBOX_CHROME_ARGS)
        return [] if self.browser_egress == "open" else self.egress.args()

    def _close_browser(self):
        # Never waits for an action in flight: closing kills the process, and the action fails at once.
        b, self.browser = self.browser, None
        if b is not None:
            b.close()

    def _local_host(self, host):
        """Does `host` name (or resolve to) an address on the machine's own network?"""
        try:
            addrs = [host] if self._is_ip(host) else self.resolve(host)
        except (OSError, UnicodeError):
            raise Fault("bad_value", "%s does not resolve" % host)
        return any(self._lan(a) for a in addrs) or not addrs

    @staticmethod
    def _is_ip(host):
        try:
            ipaddress.ip_address(host)
            return True
        except ValueError:
            return False

    def _browser_url(self, raw, level):
        if not isinstance(raw, str) or not raw or len(raw) > MAX_URL or any(c in raw for c in "\x00\r\n"):
            raise Fault("bad_value", "url is an http or https address")
        u = urllib.parse.urlsplit(raw)
        if u.scheme not in ("http", "https") or not u.hostname:
            raise Fault("bad_value", "url is an http or https address")
        if level != "full" and self._local_host(u.hostname.strip("[]")):
            raise Fault("not_allowed", "%s is on this machine's own network: only at level full" % u.hostname)
        return raw

    def _browser_op(self, v, level, env=None, desk=None):
        """(run, summary, acting) for one browser action; nothing is opened or clicked before run()."""
        binary = self._browser_binary()
        if not binary:
            raise Fault("unsupported", "no browser on this machine (Chromium, Chrome, Edge or Brave), or this machine does not allow it")
        action = v.get("action")
        if action not in BROWSER_READS and action not in BROWSER_ACTS:
            raise Fault("bad_value", "action is one of %s" % ", ".join(sorted(BROWSER_READS | BROWSER_ACTS)))
        selector, text, key, url = v.get("selector"), v.get("text"), v.get("key"), None
        if action == "open":
            url = self._browser_url(v.get("url"), level)
        if action in ("click", "fill") and selector is not None and (not isinstance(selector, str) or not selector or len(selector) > MAX_SELECTOR):
            raise Fault("bad_value", "selector is a CSS selector of at most %d characters" % MAX_SELECTOR)
        if action == "click" and selector is None and (not isinstance(text, str) or not text.strip() or len(text) > 200):
            raise Fault("bad_value", "click takes a selector, or the text of a button or link (at most 200 characters)")
        if action == "fill":
            if selector is None:
                raise Fault("bad_value", "fill takes the selector of the field")
            if not isinstance(text, str) or len(text) > MAX_TYPE or "\x00" in text:
                raise Fault("bad_value", "text is at most %d characters" % MAX_TYPE)
        if action == "press" and key not in KEY_CODES:
            raise Fault("bad_value", "key is one of %s" % ", ".join(sorted(KEY_CODES)))
        width = _int(v.get("max_width"), "max_width", 320, 2560, 1280)

        lock_key = desk if env else "main"

        def browser():
            if env:   # on a desk: its own browser (shown on a whole desk, headless on a browser desk)
                return self._desk_browser(desk, binary, env)
            with self.browser_mu:
                if self.browser is None or not self.browser.alive():
                    b = self.browser_factory(binary, self.browser_profile, self.browser_headless)
                    b.allow_downloads = self.effective_level() == "full"
                    with self.mu:
                        if self._generation_changed():
                            b.close()
                            raise Fault("local_stop", "stopped on the machine before it started")
                        self.browser = b
                self._set_downloads(self.browser)
                return self.browser

        def lock():
            with self.browser_mu:
                return self.browser_locks.setdefault(lock_key, threading.Lock())

        def run():
            self._check_identity()
            if action == "close":
                self._close_desk_browser(desk) if env else self._close_browser()
                return {"done": True}
            b = browser()
            try:
                with lock():   # one action at a time in each browser; two desks' browsers act in parallel
                    if action == "open":
                        out = b.open(url)
                    elif action == "read":
                        out = b.read()
                    elif action == "screenshot":
                        out = b.screenshot(width)
                    elif action == "click":
                        out = b.click(selector=selector, text=None if selector else text)
                    elif action == "fill":
                        out = b.fill(selector, text)
                    elif action == "press":
                        out = b.press(key)
                    else:
                        out = b.back()
                    if isinstance(out, dict):
                        self._desk_stream_saw_url(out.get("url"))
                    return out
            except BrowserError as e:
                raise Fault(e.code, e.message)

        where = "" if not env else (" on the private desk" if desk == "private" else " on desk %s" % desk)
        if action == "open":
            summary = "open %s in the browser%s" % (_plain(url), where)
        elif action == "click":
            summary = "click %s in the browser%s" % (
                _plain(selector) if selector else "the %r button or link" % _plain(text)[:120], where)
        elif action == "fill":
            shown = _plain(text) if len(_plain(text)) <= 120 else None
            summary = ("type %d characters into %s in the browser%s: %r" % (len(text), _plain(selector)[:200], where, shown)
                       if shown is not None else
                       "type %d characters into %s in the browser%s, sha256 %s"
                       % (len(text), _plain(selector)[:200], where, _digest(text.encode("utf-8"))))
        elif action == "press":
            summary = "press %s in the browser%s" % (_plain(key), where)
        elif action == "back":
            summary = "go back in the browser%s" % where
        else:
            summary = "%s the browser%s" % (action, where)
        return run, summary, action in BROWSER_ACTS

    def _input_tool(self):
        """xdotool on an X11 session, cliclick on macOS, else None (Wayland has no general input tool yet)."""
        if self.platform == "darwin" and self.which("cliclick"):
            return "cliclick"
        if self.platform.startswith("linux") and self.env.get("DISPLAY") and not self.env.get("WAYLAND_DISPLAY") and self.which("xdotool"):
            return "xdotool"
        return None

    def _input_op(self, tool, v, env=None, desk=None):
        """(run, summary) for one input action; nothing touches the screen until run() (after approval at ask). With env,
        on a desk (its display; points scale to the desk's own size). tool "page": a browser desk's page, through the
        browser's pipe (points scale to the page's size)."""
        action = v.get("action")
        if action not in INPUT_ACTIONS:
            raise Fault("bad_value", "action is one of %s" % ", ".join(sorted(INPUT_ACTIONS)))
        point = None
        if action in POINT_ACTIONS or (action == "scroll" and v.get("x") is not None):
            x = _int(v.get("x"), "x", 0, 16384, None)
            y = _int(v.get("y"), "y", 0, 16384, None)
            if x is None or y is None:
                raise Fault("bad_value", "%s needs x and y" % action)
            w = _int(v.get("width"), "width", 1, 16384, None)
            h = _int(v.get("height"), "height", 1, 16384, None)
            if (w is None) != (h is None):
                raise Fault("bad_value", "width and height come together")
            point = (x, y, w, h)
        if action == "scroll":
            dy = _int(v.get("dy"), "dy", -20, 20, None)
            if not dy:
                raise Fault("bad_value", "scroll needs dy (clicks, -20 to 20, not 0)")
            if tool not in ("xdotool", "page"):
                raise Fault("unsupported", "scrolling needs xdotool (Linux X11)")
        if action == "type":
            text = v.get("text")
            if not isinstance(text, str) or not text or len(text) > MAX_TYPE or "\x00" in text:
                raise Fault("bad_value", "text is 1 to %d characters" % MAX_TYPE)
        if action == "key":
            keys = v.get("keys")
            if not isinstance(keys, str) or not keys or len(keys) > 64 or not set(keys) <= KEYS_OK or keys.startswith("-"):
                raise Fault("bad_value", "keys is one key or chord, for example Return or ctrl+s")

        def run():
            self._check_identity()
            if tool == "page":
                return self._on_page(desk, lambda b: self._page_input(b, action, point, v))
            at = (self._desk_scale(point, desk) if env else self._scale(tool, point)) if point else None
            argv = self._input_argv(tool, action, at, v)
            proc = self._spawn(argv, kind="input", stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                               stderr=subprocess.PIPE, start_new_session=True,
                               **({"env": env} if env else {}))
            try:
                _, err = proc.communicate(timeout=30)
            except subprocess.TimeoutExpired:
                self._kill(proc)
                raise Fault("hardware", "%s did not finish in 30 s" % tool)
            finally:
                try:
                    alive = proc.poll() is None   # the driver must not outlive the command it was approved for
                except AttributeError:
                    alive = False
                if alive:
                    self._kill(proc)
                self._forget(proc, "input")
                with self.mu:
                    self.input_ended = self.clock()
            if proc.returncode != 0:
                raise Fault("hardware", "%s failed: %s" % (tool, (err or b"").decode("utf-8", "replace")[:200]))
            out = {"done": True, "action": action}
            if at:
                out["x"], out["y"] = at
            return out

        where = (" on the private desk" if desk == "private" else " on desk %s" % desk) if env else ""
        if action == "type":
            text = v["text"]
            shown = _plain(text) if len(_plain(text)) <= 120 else None
            summary = ("type %d characters%s: %r" % (len(text), where, shown) if shown is not None else
                       "type %d characters%s, sha256 %s" % (len(text), where, _digest(text.encode("utf-8"))))
        elif action == "key":
            summary = "press %s%s" % (_plain(v["keys"]), where)
        elif action == "scroll":
            summary = "scroll %s %d%s" % ("up" if v["dy"] < 0 else "down", abs(v["dy"]), where)
        else:
            x, y, w, h = point
            summary = "%s at %d, %d%s%s" % (action.replace("_", " "), x, y, " of a %d×%d picture of the screen" % (w, h) if w else "", where)
        return run, summary

    def _desk_scale(self, point, desk="private"):
        """A point in a picture of the desk, on the desk's own display (the desk the command names, at its size)."""
        x, y, w, h = point
        st = self.desks.status(desk or "private")
        if not w or not st.get("running") or not st.get("width"):
            return x, y
        return min(st["width"] - 1, round(x * st["width"] / w)), min(st["height"] - 1, round(y * st["height"] / h))

    @staticmethod
    def _page_input(b, action, point, v):
        """One input action on a browser desk's page. A point in a picture of the page scales to the page's own size;
        a scroll without a point is at the page's middle."""
        vw, vh = b.viewport()
        if point:
            x, y, w, h = point
            if w:
                x, y = round(x * vw / w), round(y * vh / h)
            x, y = min(max(0, x), vw - 1), min(max(0, y), vh - 1)
        else:
            x, y = vw // 2, vh // 2
        if action in POINT_ACTIONS:
            out = b.mouse(action, x, y)
        elif action == "scroll":
            out = b.wheel(x, y, v["dy"])
        elif action == "type":
            out = b.insert(v["text"])
        else:
            out = b.keys(v["keys"])
        out = dict(out, action=action)
        if point:
            out["x"], out["y"] = x, y
        return out

    def _screen_size(self, tool):
        """The screen's size in the units the input tool uses: pixels for xdotool, points for cliclick (a Retina
        screen's picture is twice as many pixels)."""
        if tool == "xdotool":
            out = subprocess.run(["xdotool", "getdisplaygeometry"], capture_output=True, text=True, timeout=10, env=self._tool_env())
            parts = out.stdout.split()
        else:
            out = subprocess.run(["osascript", "-e", "tell application \"Finder\" to get bounds of window of desktop"],
                                 capture_output=True, text=True, timeout=10)
            parts = [p.strip() for p in out.stdout.split(",")][2:4]
        if out.returncode != 0 or len(parts) != 2 or not all(p.isdigit() for p in parts):
            raise Fault("hardware", "could not read the screen size")
        return int(parts[0]), int(parts[1])

    def _scale(self, tool, point):
        x, y, w, h = point
        if not w:
            return x, y
        sw, sh = self.screen_size(tool)
        return min(sw - 1, round(x * sw / w)), min(sh - 1, round(y * sh / h))

    def _tool_env(self):
        env = dict(os.environ)
        env.update({k: v for k, v in self.env.items() if k in ("DISPLAY", "XAUTHORITY")})
        return env

    @staticmethod
    def _input_argv(tool, action, at, v):
        if tool == "xdotool":
            move = ["mousemove", "--sync", str(at[0]), str(at[1])] if at else []
            if action == "move":
                return ["xdotool"] + move
            if action in ("click", "double_click", "right_click"):
                button = "3" if action == "right_click" else "1"
                repeat = ["--repeat", "2"] if action == "double_click" else []
                return ["xdotool"] + move + ["click"] + repeat + [button]
            if action == "scroll":
                return ["xdotool"] + move + ["click", "--repeat", str(abs(v["dy"])), "4" if v["dy"] < 0 else "5"]
            if action == "type":
                return ["xdotool", "type", "--delay", "12", "--", v["text"]]
            return ["xdotool", "key", "--", v["keys"]]
        # cliclick (macOS): c: click, dc: double, rc: right, m: move, t: type, kd/ku: modifier down/up, kp: a named key.
        if action in POINT_ACTIONS:
            verb = {"move": "m", "click": "c", "double_click": "dc", "right_click": "rc"}[action]
            return ["cliclick", "%s:%d,%d" % (verb, at[0], at[1])]
        if action == "type":
            return ["cliclick", "t:" + v["text"]]
        parts = v["keys"].split("+")
        mods = [MODIFIERS.get(p.lower()) for p in parts[:-1]]
        if None in mods:
            raise Fault("bad_value", "unknown modifier in %s" % v["keys"])
        key = parts[-1]
        if len(key) == 1:
            press = ["t:" + key]
        elif key.lower() in CLICLICK_KEYS:
            press = ["kp:" + CLICLICK_KEYS[key.lower()]]
        elif key.lower().startswith("f") and key[1:].isdigit() and 1 <= int(key[1:]) <= 16:
            press = ["kp:" + key.lower()]
        else:
            raise Fault("unsupported", "the key %s is not supported on macOS" % key)
        mods = ["cmd" if m == "super" else m for m in mods]
        return ["cliclick"] + ["kd:" + m for m in mods] + press + ["ku:" + m for m in reversed(mods)]

    # ---- screen ----------------------------------------------------------------------------------------------------
    def _screen_tool(self):
        """(name, argv builder) for this machine's screen capture, or None."""
        if self.platform == "darwin" and self.which("screencapture"):
            return ("screencapture", lambda out: ["screencapture", "-x", "-t", "png", out])
        if self.platform.startswith("linux"):
            if self.env.get("WAYLAND_DISPLAY") and self.which("grim"):
                return ("grim", lambda out: ["grim", out])
            if self.env.get("DISPLAY"):
                if self.which("import"):
                    return ("import", lambda out: ["import", "-window", "root", out])
                if self.which("scrot"):
                    return ("scrot", lambda out: ["scrot", "-o", out])
                if self.which("gnome-screenshot"):
                    return ("gnome-screenshot", lambda out: ["gnome-screenshot", "-f", out])
        return None

    def _screen(self, tool, max_width, env=None):
        self._check_identity()
        fd, out = tempfile.mkstemp(prefix="openvibe-screen-", suffix=".png")
        os.close(fd)
        proc = None
        try:
            proc = self._spawn(tool[1](out), kind="capture", stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                               stderr=subprocess.PIPE, start_new_session=True, **({"env": env} if env else {}))
            try:
                _, err = proc.communicate(timeout=15)
            except subprocess.TimeoutExpired:
                self._kill(proc)
                raise Fault("hardware", "the screen capture did not finish in 15 s")
            if proc.returncode != 0 or not os.path.getsize(out):
                raise Fault("hardware", "screen capture failed: %s" % (err or b"").decode("utf-8", "replace")[:200])
            return self._encode(out, max_width)
        finally:
            if proc is not None:
                self._forget(proc, "capture")
            if os.path.exists(out):
                os.unlink(out)

    # ---- the live screen --------------------------------------------------------------------------------------------
    @staticmethod
    def _have_pillow():
        try:
            import PIL.Image  # noqa: F401
        except ImportError:
            return False
        return True

    @staticmethod
    def _native_jpeg(tool, path):
        """The capture tool writing JPEG itself (for machines without Pillow), or None when it only writes PNG."""
        name = tool[0]
        if name == "screencapture":
            return ["screencapture", "-x", "-t", "jpg", path]
        if name == "grim":
            return ["grim", "-t", "jpeg", "-q", str(STREAM_QUALITY), path]
        if name == "import":
            return ["import", "-silent", "-window", "root", "-quality", str(STREAM_QUALITY), path]
        if name == "scrot":
            return ["scrot", "-o", "-q", str(STREAM_QUALITY), path]
        return None

    def _stream_capable(self, tool):
        return self._have_pillow() or self._native_jpeg(tool, "x.jpg") is not None

    # ---- the agent's desk stream (Agent Streams S1) ---------------------------------------------------------------
    def _desk_stream_op(self, cmd, v, level):
        """desk.stream: start/stop/status/curtain one agent stream. The core mints the wpt_ ticket and publishes; the
        plugin says how to capture and runs the curtain's detectors (fail closed)."""
        action = v.get("action")
        if action == "status":
            return self._desk_stream_status()
        if action == "stop":
            return {"streaming": False, "stopped": self._stop_desk_stream("stopped by its owner")}
        if action == "curtain":
            with self.desk_stream_mu:
                st = self.desk_stream
            if st is None:
                raise Fault("not_ready", "no desk stream is running")
            up = v.get("up") is True
            reason = v.get("reason") if v.get("reason") in ("manual", "site", "detector") else "manual"
            if up:
                st["curtain"].raise_(reason)
            else:
                st["curtain"].lower()
            return self._desk_stream_status()
        if action != "start":
            raise Fault("bad_value", "action is start, stop, status or curtain")
        if not self.allow_desk_stream:
            raise Fault("unsupported", "this machine does not stream its desks (allow_desk_stream is off)")
        if self._shared():
            raise Fault("not_allowed", "a desk host for other people shows a desk as pictures, never live")
        desk = v.get("desk") or "main"
        if desk != "main":
            self.desks.check(desk)
            if not self.desks.status(desk).get("running"):
                raise Fault("not_ready", "start the desk first (desk start, desk %s)" % desk)
        allow_sites = v.get("allow_sites")
        if not isinstance(allow_sites, list):
            allow_sites = self.desk_stream_allow_sites
        allow_sites = [u for u in allow_sites if isinstance(u, str) and u][:50]
        self._stop_desk_stream("replaced")
        browser_desk = desk != "main" and self.desks.kind(desk) == "browser"
        screencast = None
        if browser_desk:
            status = self.desks.status(desk)
            width, height = status.get("width") or 1280, status.get("height") or 800
            browser = self._desk_browser(desk)
            screencast = PageScreencast(browser, width=width, height=height).start()
            capture = {"kind": "browser", "pipe": screencast.fifo, "width": width, "height": height}
        else:
            if desk == "main":
                if not self.allow_screen_stream:
                    raise Fault("unsupported", "this machine does not show its screen (allow_screen_stream is off)")
                if not self._screen_tool():
                    raise Fault("unsupported", "no screen capture tool on this machine")
                env = self.env
                width, height = self._display_size(env)
            else:
                status = self.desks.status(desk)
                env = self.desks.env(desk)
                width, height = status.get("width") or 1280, status.get("height") or 800
            try:
                capture = desktop_capture(self.platform, env, width, height,
                                          ffmpeg=self.which("ffmpeg") or "ffmpeg")
            except ValueError as e:
                raise Fault("unsupported", str(e))
        detectors = self._desk_stream_detectors(allow_sites, env if not browser_desk else None)
        curtain = Curtain(detectors=list(detectors.values()), on_change=self._desk_stream_curtain_changed)
        # Fail closed: the picture starts blank and only clears after every detector has been clear for two seconds.
        curtain.raise_("detector")
        curtain.start()
        hotkey = Hotkey(self._desk_stream_toggle, self.desk_stream_hotkey)
        hotkey.start()
        self._sigusr1_old = install_sigusr1(self._desk_stream_toggle)
        st = {"sid": os.urandom(8).hex(), "desk": desk, "capture": capture, "screencast": screencast,
              "curtain": curtain, "hotkey": hotkey, "detectors": detectors, "allow_sites": allow_sites,
              "since": self.clock()}
        with self.desk_stream_mu:
            self.desk_stream = st
        return {"streaming": True, "sid": st["sid"], "capture": capture, "curtain": curtain.snapshot(),
                "detectors": sorted(detectors), "hotkey": self.desk_stream_hotkey}

    def _desk_stream_status(self):
        with self.desk_stream_mu:
            st = self.desk_stream
        if st is None:
            return {"streaming": False}
        out = {"streaming": True, "sid": st["sid"], "desk": st["desk"], "capture": st["capture"],
               "curtain": st["curtain"].snapshot(), "detectors": sorted(st["detectors"]), "allow_sites": st["allow_sites"]}
        cast = st.get("screencast")
        if cast is not None:
            out["page"] = cast.status()
        return out

    def _stop_desk_stream(self, reason):
        with self.desk_stream_mu:
            st, self.desk_stream = self.desk_stream, None
        if st is None:
            return False
        cast = st.get("screencast")
        if cast is not None:
            cast.stop()
        st["hotkey"].stop()
        st["curtain"].stop()
        if self._sigusr1_old is not None:
            try:
                signal.signal(signal.SIGUSR1, self._sigusr1_old)
            except (ValueError, OSError):
                pass
            self._sigusr1_old = None
        self.log("desk stream ended (%s)", reason)
        return True

    def _desk_stream_detectors(self, allow_sites, env=None):
        wanted = self.desk_stream_detectors or ("password_field", "secret_file", "env_print", "site")
        out = {}
        if "password_field" in wanted:
            out["password_field"] = PasswordFieldDetector()
        if "secret_file" in wanted:
            out["secret_file"] = SecretFileDetector(window=(lambda: _active_window(env)) if env else None)
        if "env_print" in wanted:
            out["env_print"] = EnvPrintDetector()
        if "site" in wanted and allow_sites:
            out["site"] = SiteDetector(allow_sites)
        return out

    def _desk_stream_curtain_changed(self, up, reason):
        self.emit_event("curtain", up=bool(up), reason=reason)
        self.log("curtain %s (%s)", "up" if up else "down", reason)

    def _desk_stream_toggle(self, _reason="manual"):
        with self.desk_stream_mu:
            st = self.desk_stream
        if st is None:
            return
        if st["curtain"].snapshot()["up"]:
            st["curtain"].lower()
        else:
            st["curtain"].raise_("manual")

    def _desk_stream_saw_path(self, path):
        with self.desk_stream_mu:
            st = self.desk_stream
        if st is not None and "secret_file" in st["detectors"]:
            st["detectors"]["secret_file"].saw_path(path)

    def _desk_stream_saw_output(self, argv, out):
        with self.desk_stream_mu:
            st = self.desk_stream
        if st is None or "env_print" not in st["detectors"]:
            return
        text = ""
        if isinstance(out, dict):
            text = (out.get("stdout") or "") + "\n" + (out.get("stderr") or "")
        st["detectors"]["env_print"].saw_output(argv, text)
        if "secret_file" in st["detectors"]:
            for a in argv:
                st["detectors"]["secret_file"].saw_path(a)

    def _desk_stream_saw_url(self, url):
        with self.desk_stream_mu:
            st = self.desk_stream
        if st is not None and "site" in st["detectors"]:
            st["detectors"]["site"].saw_url(url)

    def _display_size(self, env):
        """The display's pixel size for x11grab, or 1920×1080 where there is no xdotool."""
        if not (env or {}).get("DISPLAY"):
            return 1920, 1080
        try:
            r = subprocess.run(["xdotool", "getdisplaygeometry"], capture_output=True, text=True, timeout=3,
                               env=self._child_env(env) if env is not None else None)
            w, h = [int(x) for x in r.stdout.split()]
            if w >= 320 and h >= 180:
                return w, h
        except (OSError, ValueError, subprocess.SubprocessError):
            pass
        return 1920, 1080

    def _screen_stream(self, v, env=None, desk=None, page=False):
        action = v.get("action")
        if action == "stop":
            return {"streaming": False, "stopped": self._end_stream("stopped by its owner")}
        if action != "start":
            raise Fault("bad_value", "action is start or stop")
        if not self.allow_screen_stream:
            raise Fault("unsupported", "this machine does not show its screen live (allow_screen_stream is off)")
        if self.runtime is not None and self.runtime.estopped:
            raise Fault("local_stop", "stopped on the machine")
        tool = ("page", None) if page else (self._x11_tool() if env else self._screen_tool())
        if not tool:
            raise Fault("unsupported", "no screen capture tool on this machine")
        if not page and not self._stream_capable(tool):
            raise Fault("unsupported", "%s only writes PNG: install Pillow (pip install pillow) to show the screen live" % tool[0])
        seconds = _int(v.get("seconds"), "seconds", 30, 1800, 600)
        fps = _int(v.get("fps"), "fps", 1, 5, 2)
        self._end_stream("replaced")
        frames = tempfile.mkdtemp(prefix="openvibe-screen-stream-")  # 0700, this account only
        stream = {"sid": os.urandom(8).hex(), "until": self.clock() + seconds, "fps": fps, "tool": tool, "dir": frames,
                  "stop": threading.Event(), "env": env, "desk": desk, "capture": None}
        stream["thread"] = threading.Thread(target=self._stream_loop, args=(stream,), name="computer-screen-stream",
                                            daemon=True)
        with self.stream_mu:
            self.stream = stream
        stream["thread"].start()
        if not env:   # the private desk is not the owner's screen: nothing of theirs goes live
            self._notify_stream(seconds)
        return {"streaming": True, "sid": stream["sid"], "seconds": seconds, "fps": fps, "tool": tool[0],
                "desk": (desk or "private") if env else "main"}

    def _end_stream(self, reason):
        """End the live screen, if one runs → whether one did. A capture in flight is killed and its thread joined, so
        no frame can be emitted after this returns."""
        with self.stream_mu:
            stream, self.stream = self.stream, None
        if stream is None:
            return False
        stream["stop"].set()
        stream["reason"] = reason
        capture = stream.get("capture")
        if capture is not None:
            self._kill(capture)
        thread = stream.get("thread")
        if thread is not None and thread is not threading.current_thread() and thread.is_alive():
            thread.join(CAPTURE_JOIN_S)
        return True

    def _stream_loop(self, stream):
        period = 1.0 / stream["fps"]
        n, failures, reason = 0, 0, "time is up"
        try:
            while not stream["stop"].is_set():
                started = self.clock()
                if started >= stream["until"]:
                    break
                path = os.path.join(stream["dir"], "frame-%d.jpg" % (n % 3))  # the core reads each one at once
                try:
                    self._stream_frame(stream["tool"], path, env=stream.get("env"), desk=stream.get("desk"), stream=stream)
                    n += 1
                    failures = 0
                    self.emit_video(path, stream="screen")
                except Fault as f:
                    failures += 1
                    if failures >= STREAM_MAX_FAILURES:
                        reason = "the screen capture failed: %s" % f.message
                        break
                stream["stop"].wait(max(0.0, period - (self.clock() - started)))
        finally:
            shutil.rmtree(stream["dir"], ignore_errors=True)
            with self.stream_mu:
                if self.stream is stream:
                    self.stream = None
            # The core ends the stream with the same id (a newer one that replaced it carries its own).
            self.emit_event("screen_stream_ended", sid=stream["sid"], reason=stream.get("reason") or reason, frames=n)

    def _stream_frame(self, tool, path, env=None, desk=None, stream=None):
        """One JPEG frame of the screen at path, at most STREAM_WIDTH wide when Pillow can resize it. On a browser desk,
        a picture of its page from the browser itself (JPEG and scaled there: no capture tool, no Pillow)."""
        if tool[0] == "page":
            # Not under the desk's action lock: the live view keeps moving while an action waits for a page to load.
            try:
                self._desk_browser(desk).frame(path, STREAM_WIDTH, STREAM_QUALITY)
            except BrowserError as e:
                raise Fault(e.code, e.message)
            return
        if self._have_pillow():
            fd, png = tempfile.mkstemp(prefix="openvibe-screen-", suffix=".png")
            os.close(fd)
            try:
                self._capture(tool[1](png), env=env, owner=stream)
                from PIL import Image
                with Image.open(png) as im:
                    im = im.convert("RGB")
                    if im.width > STREAM_WIDTH:
                        im = im.resize((STREAM_WIDTH, max(1, round(im.height * STREAM_WIDTH / im.width))))
                    im.save(path + ".part", "JPEG", quality=STREAM_QUALITY)
                os.replace(path + ".part", path)
            finally:
                if os.path.exists(png):
                    os.unlink(png)
            return
        self._capture(self._native_jpeg(tool, path + ".part.jpg"), env=env, owner=stream)
        os.replace(path + ".part.jpg", path)

    def _capture(self, argv, env=None, owner=None):
        """One screen capture process, tracked so a stream stop can kill it and join its frame loop."""
        proc = self._spawn(argv, kind="capture", stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                           stderr=subprocess.PIPE, start_new_session=True, **({"env": env} if env else {}))
        if owner is not None:
            with self.stream_mu:
                owner["capture"] = proc
        try:
            try:
                _, err = proc.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                self._kill(proc)
                raise Fault("hardware", "the screen capture did not finish in 10 s")
            if proc.returncode != 0:
                raise Fault("hardware", (err or b"").decode("utf-8", "replace")[:200] or "the capture tool failed")
        finally:
            self._forget(proc, "capture")
            if owner is not None:
                with self.stream_mu:
                    if owner.get("capture") is proc:
                        owner["capture"] = None

    def _notify_stream(self, seconds):
        """Say on the desktop that the screen is live, and how to end it (config `notify`). Best effort."""
        if not self.notify:
            return
        title = "Your screen is live on OpenVibe"
        body = "Shown to your own account for %d min. Run `openvibe-node stop` to end it now." % max(1, seconds // 60)
        argv = None
        if self.platform.startswith("linux") and (self.env.get("DISPLAY") or self.env.get("WAYLAND_DISPLAY")) and self.which("notify-send"):
            argv = ["notify-send", "--app-name=OpenVibe Node", "--", title, body]
        elif self.platform == "darwin" and self.which("osascript"):
            quote = lambda s: '"%s"' % s.replace("\\", "\\\\").replace('"', '\\"')
            argv = ["osascript", "-e", "display notification %s with title %s" % (quote(body), quote(title))]
        if argv is None:
            return
        try:
            self.popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       start_new_session=True)
        except OSError:
            pass

    @staticmethod
    def _encode(path, max_width):
        try:
            from PIL import Image
        except ImportError:
            with open(path, "rb") as f:
                png = f.read()
            if len(png) > SCREEN_MAX_BYTES:
                raise Fault("unsupported", "install Pillow (pip install pillow) to send screens larger than 300 KB")
            return {"format": "png", "content_b64": base64.b64encode(png).decode("ascii")}
        import io
        with Image.open(path) as im:
            im = im.convert("RGB")
            width = min(max_width, im.width)
            quality = 75
            while True:
                height = max(1, round(im.height * width / im.width))
                frame = im.resize((width, height)) if width != im.width else im
                buf = io.BytesIO()
                frame.save(buf, "JPEG", quality=quality, optimize=True)
                if buf.tell() <= SCREEN_MAX_BYTES or width <= 320:
                    break
                if quality > 50:
                    quality -= 15
                else:
                    width = max(320, int(width * 0.75))
        return {"format": "jpeg", "width": width, "height": height,
                "content_b64": base64.b64encode(buf.getvalue()).decode("ascii")}
