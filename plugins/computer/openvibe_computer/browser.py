"""The computer plugin's browser: one Chromium the agent drives, in a profile of its own (plan T14, the browser kind).

Chromium is started on the first command with ``--remote-debugging-pipe``: it reads Chrome DevTools Protocol commands
from file descriptor 3 and writes answers and events to 4, each message JSON ended by a NUL byte. No port is opened,
so nothing else on the machine can drive it. The profile is a folder of its own (``browser_profile``, by default
``~/.local/share/openvibe-link/browser``): the agent never sees the owner's own browser sessions unless the owner points
that setting at one.

    Browser(binary, profile, headless).open(url) / read() / click(selector|text) / fill(selector, text) / press(key)
        / back() / screenshot(max_width) / close()

On a browser desk (Desks plan D2: a browser alone, no display) the page is also the desk's screen: ``mouse``,
``wheel``, ``insert`` and ``keys`` are its mouse and keyboard, and ``frame`` writes one picture of it for the live view.

Every page answer is ``{url, title, text, links}``: the page's visible text (cut at ``MAX_TEXT``) and its first links.
"""

import base64
import json
import os
import re
import subprocess
import sys
import threading
import time

from openvibe_computer.fence import cap_text, fence as fence_text, parse_fenced
from openvibe_computer.origins import origin_of

MAX_TEXT = 20000
MAX_PAGE_BYTES = 48 * 1024
STARTUP_S = 90.0   # a first start in a cold sandbox (CI runners took over 45 s on 2026-11-02)
MAX_LINKS = 60
MAX_ELEMENTS = 120
MAX_APPEARED = 8


def cap_json_text(value, max_bytes):
    """Bound both source UTF-8 and the JSON bytes sent through the pipe (non-ASCII escapes can be 6x)."""
    text, cut = cap_text(value, max_bytes)
    if len(json.dumps(text).encode("utf-8")) <= max_bytes:
        return text, cut
    mark = "…[truncated]"
    lo, hi = 0, len(text)
    while lo < hi:
        mid = (lo + hi + 1) // 2
        if len(json.dumps(text[:mid] + mark).encode("utf-8")) <= max_bytes:
            lo = mid
        else:
            hi = mid - 1
    return text[:lo] + mark, True
MAX_FOLLOWED = 5
MAX_SESSION_COOKIES = 3000
EXACT_HTTPS_ORIGIN = re.compile(r"^https://[a-z0-9.-]+(:[0-9]{1,5})?$")
_blind_fill_lock = threading.Lock()
_blind_fill_ids = {}
# What Storage.setCookies takes back of what Storage.getCookies gives.
COOKIE_FIELDS = ("name", "value", "domain", "path", "secure", "httpOnly", "sameSite", "priority", "sourceScheme",
                 "sourcePort", "partitionKey")   # pages opened from pages (a sign-in popup, a new tab) followed at most this deep
KEY_CODES = {
    "Enter": (13, "\r"), "Tab": (9, "\t"), "Escape": (27, ""), "Backspace": (8, ""), "Delete": (46, ""),
    "ArrowUp": (38, ""), "ArrowDown": (40, ""), "ArrowLeft": (37, ""), "ArrowRight": (39, ""),
    "PageUp": (33, ""), "PageDown": (34, ""), "Home": (36, ""), "End": (35, ""), "Space": (32, " "),
}
# xdotool's key names (what `input` takes) → the browser's keys: (key, code, Windows key code, text).
PAGE_KEYS = {
    "return": ("Enter", "Enter", 13, "\r"), "enter": ("Enter", "Enter", 13, "\r"), "kp_enter": ("Enter", "NumpadEnter", 13, "\r"),
    "tab": ("Tab", "Tab", 9, ""), "escape": ("Escape", "Escape", 27, ""), "esc": ("Escape", "Escape", 27, ""),
    "backspace": ("Backspace", "Backspace", 8, ""), "delete": ("Delete", "Delete", 46, ""), "space": (" ", "Space", 32, " "),
    "up": ("ArrowUp", "ArrowUp", 38, ""), "down": ("ArrowDown", "ArrowDown", 40, ""), "left": ("ArrowLeft", "ArrowLeft", 37, ""),
    "right": ("ArrowRight", "ArrowRight", 39, ""), "home": ("Home", "Home", 36, ""), "end": ("End", "End", 35, ""),
    "page_up": ("PageUp", "PageUp", 33, ""), "prior": ("PageUp", "PageUp", 33, ""), "page_down": ("PageDown", "PageDown", 34, ""),
    "next": ("PageDown", "PageDown", 34, ""), "insert": ("Insert", "Insert", 45, ""),
}
# Modifiers: the bit CDP's `modifiers` takes, and the key event that holds it down.
PAGE_MODIFIERS = {
    "alt": (1, ("Alt", "AltLeft", 18)), "option": (1, ("Alt", "AltLeft", 18)),
    "ctrl": (2, ("Control", "ControlLeft", 17)), "control": (2, ("Control", "ControlLeft", 17)),
    "super": (4, ("Meta", "MetaLeft", 91)), "meta": (4, ("Meta", "MetaLeft", 91)), "cmd": (4, ("Meta", "MetaLeft", 91)),
    "command": (4, ("Meta", "MetaLeft", 91)), "win": (4, ("Meta", "MetaLeft", 91)),
    "shift": (8, ("Shift", "ShiftLeft", 16)),
}


def mac_edit_command(bits, name, platform=None):
    """The editing command a chord means in a text field on macOS, or None. Chrome on a Mac runs select all, copy,
    paste, cut, undo and redo from the system's key bindings, which synthetic key events never reach: the event has to
    name the command (CDP's `commands`, as Puppeteer does). An agent sends the chord it knows (ctrl+a on Linux and
    Windows, cmd+a on a Mac), so on a Mac both work: a browser desk behaves the same on every OS (2026-10-27, found by
    running the plugin's tests on macOS: ctrl+a then BackSpace deleted one letter)."""
    if (platform or sys.platform) != "darwin" or not bits & 6:   # ctrl or meta held
        return None
    k = name.lower()
    if k == "z":
        return "redo" if bits & 8 else "undo"
    return {"a": "selectAll", "c": "copy", "x": "cut", "v": "paste", "y": "redo"}.get(k)


def page_key(name, shift=False):
    """(key, code, Windows key code, text) for one xdotool key name on a page, or None."""
    low = name.lower()
    if low in PAGE_KEYS:
        return PAGE_KEYS[low]
    if len(name) == 1 and name.isalpha():
        ch = name.upper() if shift else name.lower()
        return (ch, "Key" + name.upper(), ord(name.upper()), ch)
    if len(name) == 1 and name.isdigit():
        return (name, "Digit" + name, ord(name), name)
    if low.startswith("f") and low[1:].isdigit() and 1 <= int(low[1:]) <= 12:
        return ("F" + low[1:], "F" + low[1:], 111 + int(low[1:]), "")
    return None


CANDIDATES = ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser")
MAC_APPS = ("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
            "/Applications/Chromium.app/Contents/MacOS/Chromium",
            "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
            "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser")

# The page as text, its links, and its interactive elements as refs (Desks plan D3: act on refs from the page's own
# structure before ever reaching for a screenshot). An element keeps its ref (data-ov-ref) for as long as it stays on
# the page, so a ref from one read still names the same element after the page changed around it. Each element is one
# compact line, `e7 button "Sign in"`, which costs a model a fraction of a screenshot.
SUMMARY_JS = """(() => {
  const text = (document.body ? document.body.innerText : '').replace(/\\n{3,}/g, '\\n\\n');
  const links = [...document.querySelectorAll('a[href]')]
    .filter((a) => /^https?:/.test(a.href))
    .slice(0, %d)
    .map((a) => ({ text: (a.innerText || a.getAttribute('aria-label') || '').trim().slice(0, 100), href: a.href }));
  const SEL = 'a[href], button, input:not([type=hidden]), select, textarea, summary, [contenteditable=""], [contenteditable=true],'
    + ' [role=button], [role=link], [role=checkbox], [role=radio], [role=tab], [role=menuitem], [role=switch], [role=option], [role=combobox]';
  const shown = (el) => { const r = el.getClientRects(); if (!r.length) return false; const cs = getComputedStyle(el); return cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const roleOf = (el) => el.getAttribute('role') || ({ A: 'link', BUTTON: 'button', SELECT: 'select', TEXTAREA: 'textbox', SUMMARY: 'button' })[el.tagName]
    || (el.tagName === 'INPUT' ? ({ checkbox: 'checkbox', radio: 'radio', submit: 'button', button: 'button', reset: 'button', range: 'slider', file: 'file' })[el.type] || 'textbox' : 'textbox');
  const labelOf = (el) => (el.labels && el.labels.length ? el.labels[0].innerText : '');
  const valueName = (el) => (['checkbox', 'radio'].includes(el.type) ? '' : el.value);
  const nameOf = (el) => (el.getAttribute('aria-label') || labelOf(el) || el.innerText || valueName(el) || el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt') || el.getAttribute('name') || '').replace(/\\s+/g, ' ').trim().slice(0, 80);
  window.__ovRef = window.__ovRef || 0;
  const elements = [];
  for (const el of document.querySelectorAll(SEL)) {
    if (elements.length >= %d) break;
    if (!shown(el)) continue;
    if (!el.dataset.ovRef) el.dataset.ovRef = 'e' + (++window.__ovRef);
    const role = roleOf(el);
    let line = el.dataset.ovRef + ' ' + role + ' ' + JSON.stringify(nameOf(el));
    if (role === 'textbox' && el.tagName === 'INPUT' && el.type && el.type !== 'text') line += ' (' + el.type + ')';
    if (el.checked) line += ' checked';
    if (el.disabled) line += ' disabled';
    if (role === 'textbox' && el.value) line += ' value=' + JSON.stringify(String(el.value).slice(0, 40));
    elements.push(line);
  }
  return { url: location.href, title: document.title, text: text.slice(0, %d), truncated: text.length > %d, links, elements };
})()""" % (MAX_LINKS, MAX_ELEMENTS, MAX_TEXT, MAX_TEXT)

# What the page says before an action, to tell what the action changed (its receipt).
BEFORE_JS = """(() => ({ url: location.href, text: (document.body ? document.body.innerText : '').slice(0, 60000) }))()"""


def receipt(before, after):
    """What an action changed (Desks plan D3), from the page before and after it: whether it went to another address,
    the first lines that appeared (MAX_APPEARED), and how many lines went away. An agent reads at once whether its click
    did anything, without another read or a screenshot."""
    if not isinstance(before, dict):
        return None
    old = [l.strip() for l in str(before.get("text") or "").splitlines() if l.strip()]
    new = [l.strip() for l in str(after.get("text") or "").splitlines() if l.strip()]
    old_set, new_set = set(old), set(new)
    appeared = [l[:160] for l in new if l not in old_set][:MAX_APPEARED]
    gone = len([l for l in old_set if l not in new_set])
    return {"url_changed": before.get("url") != after.get("url"), "appeared": appeared, "gone": gone}

CLICK_JS = """((selector, text) => {
  let el = null;
  if (selector) el = document.querySelector(selector);
  else {
    const want = text.trim().toLowerCase();
    const items = [...document.querySelectorAll('a, button, [role=button], input[type=submit], input[type=button], summary, label')];
    el = items.find((e) => (e.innerText || e.value || e.getAttribute('aria-label') || '').trim().toLowerCase() === want)
      || items.find((e) => (e.innerText || e.value || e.getAttribute('aria-label') || '').trim().toLowerCase().includes(want));
  }
  if (!el) return false;
  el.scrollIntoView({ block: 'center' });
  el.click();
  return true;
})(%s, %s)"""

FOCUS_JS = """((selector) => {
  const el = document.querySelector(selector);
  if (!el) return false;
  el.scrollIntoView({ block: 'center' });
  el.focus();
  if ('value' in el) { el.value = ''; el.dispatchEvent(new Event('input', { bubbles: true })); }
  else if (el.isContentEditable) el.textContent = '';
  return true;
})(%s)"""

# What a click would follow, read before it is clicked (the origins gate): the same match as CLICK_JS,
# then the nearest link's address, or null when the target is no link. Read-only: nothing is clicked.
TARGET_JS = """((selector, text) => {
  let el = null;
  if (selector) el = document.querySelector(selector);
  else {
    const want = text.trim().toLowerCase();
    const items = [...document.querySelectorAll('a, button, [role=button], input[type=submit], input[type=button], summary, label')];
    el = items.find((e) => (e.innerText || e.value || e.getAttribute('aria-label') || '').trim().toLowerCase() === want)
      || items.find((e) => (e.innerText || e.value || e.getAttribute('aria-label') || '').trim().toLowerCase().includes(want));
  }
  if (!el) return null;
  const link = el.closest ? el.closest('a[href]') : null;
  const href = (link && link.href) || null;
  return href && /^https?:/.test(href) ? href : null;
})(%s, %s)"""


class BrowserError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def find_binary(which, platform, configured=None):
    """The Chromium-family browser to drive: the configured path, else the first one on PATH (or a macOS app)."""
    if configured:
        return configured if os.path.isfile(configured) and os.access(configured, os.X_OK) else None
    for name in CANDIDATES:
        path = which(name)
        if path:
            return path
    if platform == "darwin":
        for path in MAC_APPS:
            if os.path.isfile(path):
                return path
    return None


class Browser:
    def __init__(self, binary, profile, headless=True, timeout_s=15.0, popen=subprocess.Popen, extra_args=(), env=None,
                 sandbox=None):
        self.binary = binary
        self.env = env   # the private desk's display and bus, or None for this process's own
        # On a desk host serving other people (desk_isolation sandbox): the `openvibe-link desk-sandbox … --` command the
        # browser runs under, its profile inside the sandbox (profile is then a path in there, never made here).
        self.sandbox = list(sandbox) if sandbox else None
        self.extra_args = list(extra_args)
        self.stderr_tail = []
        self.profile = profile
        self.headless = headless
        self.timeout_s = timeout_s
        self.popen = popen
        self.proc = None
        self.out = None
        self.inp = None
        self.next_id = 0
        self.waiting = {}          # message id -> [event, result]
        self.events = []           # (method, params) seen since the last mark
        self.mu = threading.Lock()
        self.session = None
        self.used_at = 0.0
        self.download_dir = None   # a desk's own downloads folder (set before the first action), else the browser's default
        self.allow_downloads = False   # below level full the browser is told to deny every download (F4)
        self._downloads_state = None   # what was last told to the browser: "deny", "allow" or None
        self.window = (1280, 900)  # the window's size
        self.page_size = None      # on a browser desk, the page's exact size (the desk's: its page is its screen)
        self.write_mu = threading.Lock()   # one message at a time on the pipe: actions and the live view share it
        self.start_mu = threading.Lock()
        self.target = None         # the page in front: the agent reads and acts on it, a browser desk shows it
        self.behind = []           # (target, session) of the pages it was opened from, newest last
        self.tab_events = []       # Target.targetCreated / targetDestroyed seen since the last _follow
        self.tab_change = None     # "opened" or "closed" since the last _mark: said in the action's receipt
        self.last_navigation = None   # the URL of the last main-frame navigation (a desk stream's allow-list reads it)
        self.follow_mu = threading.Lock()
        self.storage_origins = set()
        self.after_start = None    # called once the browser runs (a kept profile's session cookies go back in)
        self.allowed_origins = None  # fixed for this browser process; a new task gets a new proxy and process
        self.task_egress = None

    # ---- the process and the pipe ------------------------------------------------------------------------------------
    def alive(self):
        return self.proc is not None and self.proc.poll() is None

    def start(self):
        # One start at a time: an action and a browser desk's live view may both find the browser closed at once.
        with self.start_mu:
            if not self.alive():
                self._start()

    def _start(self):
        if not self.sandbox:
            os.makedirs(self.profile, mode=0o700, exist_ok=True)
        to_chrome_r, to_chrome_w = os.pipe()
        from_chrome_r, from_chrome_w = os.pipe()
        argv = [self.binary, "--remote-debugging-pipe", "--user-data-dir=%s" % self.profile, "--no-first-run",
                "--no-default-browser-check", "--disable-background-networking", "--disable-sync",
                "--disable-component-update", "--password-store=basic", "--window-size=%d,%d" % self.window, "about:blank"]
        if self.headless:
            argv.insert(1, "--headless=new")
        argv[1:1] = self.extra_args
        if self.sandbox:
            argv[:0] = self.sandbox   # fds 3 and 4 pass through the sandbox to the browser

        def fds():
            # In the child: Chromium reads commands on 3 and writes on 4. Both ends move above 10 first, so placing one
            # never closes the other; 3 and 4 are in pass_fds, so the close that follows this function keeps them.
            import fcntl
            r = fcntl.fcntl(to_chrome_r, fcntl.F_DUPFD, 10)
            w = fcntl.fcntl(from_chrome_w, fcntl.F_DUPFD, 10)
            os.dup2(r, 3)
            os.dup2(w, 4)

        self.proc = self.popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                               pass_fds=(3, 4), preexec_fn=fds, start_new_session=True, **({"env": self.env} if self.env else {}))
        threading.Thread(target=self._stderr, args=(self.proc.stderr,), name="computer-browser-err", daemon=True).start()
        os.close(to_chrome_r)
        os.close(from_chrome_w)
        self.out = os.fdopen(to_chrome_w, "wb", buffering=0)
        self.inp = os.fdopen(from_chrome_r, "rb", buffering=0)
        threading.Thread(target=self._reader, name="computer-browser", daemon=True).start()
        # A first start on a fresh profile can take a while on a slow machine: the first command gets a longer window.
        target = self.call("Target.createTarget", {"url": "about:blank"}, session=False, timeout_s=max(self.timeout_s, STARTUP_S))["targetId"]
        self.target, self.behind = target, []
        self._attach(target)
        # Pages this page opens (a sign-in popup, a link to a new tab) are followed: see _follow.
        self.call("Target.setDiscoverTargets", {"discover": True}, session=False)
        self._apply_download_policy()
        if self.after_start:
            hook, self.after_start = self.after_start, None
            hook()

    def _apply_download_policy(self):
        """Below level full nothing is downloaded (not into a desk folder, not the browser's own); at full a desk's own
        folder is allowed. The plugin decides before the browser starts."""
        if not self.allow_downloads:
            self.call("Browser.setDownloadBehavior", {"behavior": "deny"}, session=False)
            self._downloads_state = "deny"
        elif self.download_dir:
            self.call("Browser.setDownloadBehavior", {"behavior": "allow", "downloadPath": self.download_dir}, session=False)
            self._downloads_state = "allow"
        elif self._downloads_state == "deny":
            # Back to the browser's own download folder after a level was raised (full, on the owner's screen).
            self.call("Browser.setDownloadBehavior", {"behavior": "default"}, session=False)
            self._downloads_state = None

    def _attach(self, target):
        """Drive that page: a session of its own, its events on, and a browser desk's exact size."""
        self.session = self.call("Target.attachToTarget", {"targetId": target, "flatten": True}, session=False)["sessionId"]
        self.call("Page.enable")
        self.call("Runtime.enable")
        if self.page_size:
            self.call("Emulation.setDeviceMetricsOverride", {"width": self.page_size[0], "height": self.page_size[1],
                                                              "deviceScaleFactor": 1, "mobile": False})

    def _follow(self):
        """Keep the page in front in front: a page this page opened (a sign-in popup, a link that opens a new tab)
        becomes the page the agent reads and acts on, and a browser desk shows; when it closes (a popup closes itself
        after a sign-in), the page it came from is in front again. Only pages opened from the followed pages, at most
        MAX_FOLLOWED deep; anything else in the browser is left alone."""
        with self.follow_mu:
            with self.mu:
                events, self.tab_events = self.tab_events, []
            for method, params in events:
                info = params.get("targetInfo") or {}
                ours = [self.target] + [t for t, _ in self.behind]
                if method == "Target.targetCreated" and info.get("type") == "page" and info.get("openerId") in ours:
                    if len(self.behind) >= MAX_FOLLOWED:
                        continue
                    was = (self.target, self.session)
                    try:
                        self.target = info["targetId"]
                        self._attach(self.target)
                    except BrowserError:
                        self.target, self.session = was
                        continue
                    self.behind.append(was)
                    self.tab_change = "opened"
                    self._ready()
                elif method == "Target.targetDestroyed":
                    gone = params.get("targetId")
                    if gone == self.target and self.behind:
                        self.target, self.session = self.behind.pop()
                        self.tab_change = "closed"
                    else:
                        self.behind = [(t, sess) for t, sess in self.behind if t != gone]

    def _front_changed(self, session, timeout_s=1.0):
        """Whether the page in front changed from that session's (its page closed), waiting up to timeout_s for the
        browser to say so."""
        end = time.monotonic() + timeout_s
        while True:
            self._follow()
            if self.session != session:
                return True
            if time.monotonic() >= end:
                return False
            time.sleep(0.05)

    def _ready(self, timeout_s=10.0):
        """Wait (a little) for a page just followed to finish loading."""
        end = time.monotonic() + timeout_s
        while time.monotonic() < end:
            try:
                if self._eval("document.readyState") == "complete":
                    return
            except BrowserError:
                pass
            time.sleep(0.1)

    def _stderr(self, stream):
        """Keep the browser's last lines of stderr: why it closed, when it does."""
        for line in iter(stream.readline, b""):
            self.stderr_tail = (self.stderr_tail + [line.decode("utf-8", "replace").strip()])[-5:]

    def _reader(self):
        buf = b""
        while True:
            try:
                chunk = self.inp.read(65536)
            except (OSError, ValueError):
                chunk = b""
            if not chunk:
                break
            buf += chunk
            while b"\0" in buf:
                raw, buf = buf.split(b"\0", 1)
                try:
                    msg = json.loads(raw.decode("utf-8"))
                except ValueError:
                    continue
                if "id" in msg:
                    with self.mu:
                        slot = self.waiting.get(msg["id"])
                    if slot is not None:
                        slot[1] = msg
                        slot[0].set()
                elif "method" in msg:
                    with self.mu:
                        if msg["method"] == "Target.detachedFromTarget":
                            # Its page closed (a popup that closes itself): a call still waiting on it is never answered
                            # (Chrome on macOS stays silent), so it fails at once and _eval reads the page in front again.
                            gone = (msg.get("params") or {}).get("sessionId")
                            for waiting in self.waiting.values():
                                if gone and waiting[2] == gone and waiting[1] is None:
                                    waiting[1] = {"error": {"message": "Session closed: its page closed"}}
                                    waiting[0].set()
                        self.events.append(msg["method"])
                        if msg["method"] == "Page.frameNavigated":
                            frame = (msg.get("params") or {}).get("frame") or {}
                            if not frame.get("parentId") and frame.get("url"):
                                self.last_navigation = frame["url"]
                        if msg["method"] in ("Target.targetCreated", "Target.targetDestroyed"):
                            self.tab_events = (self.tab_events + [(msg["method"], msg.get("params") or {})])[-50:]
                        if len(self.events) > 500:
                            del self.events[:250]
        with self.mu:
            for slot in self.waiting.values():
                slot[0].set()

    def call(self, method, params=None, session=True, timeout_s=None):
        sid = self.session if session else None
        with self.mu:
            self.next_id += 1
            mid = self.next_id
            slot = [threading.Event(), None, sid]   # the page's session it went to: answered at once if that page closes
            self.waiting[mid] = slot
        msg = {"id": mid, "method": method, "params": params or {}}
        if sid:
            msg["sessionId"] = sid
        try:
            with self.write_mu:
                self.out.write(json.dumps(msg).encode("utf-8") + b"\0")
        except (OSError, ValueError, AttributeError):
            raise BrowserError("hardware", "the browser is not running")
        ok = slot[0].wait(timeout_s or self.timeout_s)
        with self.mu:
            self.waiting.pop(mid, None)
        if not ok:
            raise BrowserError("expired", "the browser did not answer %s in time" % method)
        if slot[1] is None:
            why = " | ".join(x for x in self.stderr_tail if x)[-400:]
            raise BrowserError("hardware", "the browser closed" + (": %s" % why if why else ""))
        if "error" in slot[1]:
            raise BrowserError("bad_value", "%s: %s" % (method, slot[1]["error"].get("message", "refused")))
        return slot[1].get("result", {})

    def _mark(self):
        with self.mu:
            self.events = []
        self.tab_change = None

    def _wait_for(self, methods, timeout_s):
        end = time.monotonic() + timeout_s
        while time.monotonic() < end:
            with self.mu:
                if any(m in methods for m in self.events):
                    return True
            time.sleep(0.05)
        return False

    def _eval(self, expression, gesture=False, context_id=None, sanitize_errors=False):
        # gesture: as a person's click (user activation), so a page may open its sign-in popup from the agent's click.
        params = {"expression": expression, "returnByValue": True, "awaitPromise": True}
        if gesture:
            params["userGesture"] = True
        if context_id is not None:
            params["contextId"] = context_id
        session = self.session
        try:
            r = self.call("Runtime.evaluate", params)
        except BrowserError as e:
            if sanitize_errors:
                raise BrowserError("bad_value", "the browser refused the page script")
            # The page in front just closed (a popup closes itself after a sign-in) before the browser said so: wait for
            # it to say so (a second at most), then once more on the page in front again.
            if "session" not in e.message.lower() or not self._front_changed(session):
                raise
            r = self.call("Runtime.evaluate", params)
        if r.get("exceptionDetails"):
            if sanitize_errors:
                raise BrowserError("bad_value", "the browser refused the page script")
            raise BrowserError("bad_value", "the page refused: %s" % r["exceptionDetails"].get("text", "an exception"))
        return r.get("result", {}).get("value")

    def _settle(self, navigated_timeout=2.0):
        """After an action: if it started a navigation, wait for the load; then a short pause for scripts."""
        if self._wait_for(("Page.frameStartedLoading", "Page.frameNavigated"), navigated_timeout):
            self._wait_for(("Page.loadEventFired",), self.timeout_s)
        time.sleep(0.3)

    # ---- the actions -----------------------------------------------------------------------------------------------
    def summary(self):
        self.used_at = time.monotonic()
        self._follow()
        page = self._eval(SUMMARY_JS)
        # The page is untrusted. Cap UTF-8 bytes after JS's character cap, then put the nonce markers outside
        # that cap. A multi-byte page can exceed MAX_TEXT bytes even when it has MAX_TEXT characters.
        if isinstance(page, dict) and isinstance(page.get("text"), str):
            capped, cut = cap_json_text(page["text"], MAX_TEXT)
            title, title_cut = cap_json_text(page.get("title", ""), 512)
            url, url_cut = cap_json_text(page.get("url", ""), 2048)
            budget = MAX_PAGE_BYTES - len(json.dumps(capped).encode("utf-8")) - len(json.dumps(title).encode("utf-8")) - len(json.dumps(url).encode("utf-8")) - 2048
            links, elements = [], []
            for link in page.get("links") or []:
                if not isinstance(link, dict):
                    cut = True
                    continue
                label, label_cut = cap_json_text(link.get("text", ""), 160)
                href, href_cut = cap_json_text(link.get("href", ""), 2048)
                size = len(json.dumps({"text": label, "href": href}).encode("utf-8"))
                if size > budget:
                    cut = True
                    break
                links.append({"text": label, "href": href})
                budget -= size
                cut = cut or label_cut or href_cut
            for element in page.get("elements") or []:
                line, line_cut = cap_json_text(element, 256)
                size = len(json.dumps(line).encode("utf-8")) + 8
                if size > budget:
                    cut = True
                    break
                elements.append(line)
                budget -= size
                cut = cut or line_cut
            parts = origin_of(page.get("url"))
            if parts:
                scheme, host, port = parts
                origin = "%s://%s:%d" % (scheme, "[%s]" % host if ":" in host else host, port)
            else:
                origin = "unknown"
            if not re.fullmatch(r"https?://[A-Za-z0-9.:[\]-]+", origin):
                origin = "unknown"
            fenced, nonce = fence_text(capped, origin=origin)
            page = dict(page, url=url, title=title, text=fenced, links=links, elements=elements,
                        fence=nonce, origin=origin, truncated=bool(page.get("truncated") or cut or title_cut or url_cut))
        return page

    def open(self, url):
        self.start()
        self._mark()
        r = self.call("Page.navigate", {"url": url})
        if r.get("errorText"):
            raise BrowserError("hardware", "could not open %s: %s" % (url, r["errorText"]))
        self._wait_for(("Page.loadEventFired",), self.timeout_s)
        time.sleep(0.3)
        return self.summary()

    def read(self):
        self._need()
        return self.summary()

    def active_origin(self):
        """Return the active target's top-level frame origin, never child-frame URLs."""
        self._need()
        self._follow()
        tree = self.call("Page.getFrameTree").get("frameTree", {}).get("frame", {})
        raw = tree.get("url", "")
        from urllib.parse import urlsplit
        u = urlsplit(raw)
        if u.scheme != "https" or not u.hostname:
            return {"origin": None}
        host = u.hostname.lower()
        if ":" in host:
            host = "[" + host + "]"
        try:
            port = u.port
        except ValueError:
            return {"origin": None}
        origin = "%s://%s%s" % (u.scheme, host, (":" + str(port)) if port and port != 443 else "")
        return {"origin": origin if EXACT_HTTPS_ORIGIN.fullmatch(origin) else None}

    def blind_fill(self, request_id, origin, username, password, code="", submit=False):
        """Fill visible fields in the top-level document and return only an outcome code."""
        self._need()
        import json
        if not isinstance(request_id, str) or not re.fullmatch(r"fr_[A-Za-z0-9_-]{16,64}", request_id):
            raise BrowserError("bad_value", "invalid fill request")
        if not isinstance(origin, str) or not EXACT_HTTPS_ORIGIN.fullmatch(origin):
            raise BrowserError("bad_value", "invalid fill origin")
        if not isinstance(username, str) or not username or len(username) > 4096 or not isinstance(password, str) or not password or len(password) > 4096:
            raise BrowserError("bad_value", "invalid fill grant")
        if not isinstance(code, str) or len(code) > 32 or not isinstance(submit, bool):
            raise BrowserError("bad_value", "invalid fill grant")
        now = time.monotonic()
        with _blind_fill_lock:
            for key, expiry in list(_blind_fill_ids.items()):
                if expiry <= now: del _blind_fill_ids[key]
            if request_id in _blind_fill_ids:
                return {"outcome": "failed", "reason": "reused"}
            _blind_fill_ids[request_id] = now + 120
        active = self.active_origin().get("origin")
        if active != origin:
            return {"outcome": "failed", "reason": "origin_changed"}
        script = """(() => {
          const origin = %s;
          if (location.origin !== origin) return {ok:false,reason:'origin_changed'};
          const visible = e => { const r=e.getBoundingClientRect(),s=getComputedStyle(e); return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.display!=='none'&&!e.disabled; };
          const inputs = [...document.querySelectorAll('input')].filter(visible);
          const password = inputs.find(e => e.type === 'password');
          const username = inputs.find(e => e !== password && (e.autocomplete === 'username' || ['email','text'].includes(e.type)) && !/search|hidden/i.test(e.name+' '+e.autocomplete));
          if (!username || !password || username.ownerDocument !== document || password.ownerDocument !== document) return {ok:false,reason:'no_fields'};
          const set = (e,v) => { const p=Object.getPrototypeOf(e),d=Object.getOwnPropertyDescriptor(p,'value'); if(d&&d.set)d.set.call(e,v); else e.value=v; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); };
          set(username,%s); set(password,%s);
          const code = %s;
          if(code) { const otp=inputs.find(e => e !== username && e !== password && (e.autocomplete === 'one-time-code' || /otp|one.?time|verification.?code/i.test(e.name+' '+e.id+' '+e.placeholder))); if(!otp) return {ok:false,reason:'no_fields'}; set(otp,code); }
          if(%s) { const form=password.form; if(!form) return {ok:false,reason:'refused'}; const button=[...form.querySelectorAll('button[type=submit],input[type=submit]')].find(visible); form.requestSubmit(button || undefined); }
          return {ok:true};
        })()""" % (json.dumps(origin), json.dumps(username), json.dumps(password), json.dumps(code), "true" if submit else "false")
        frame = self.call("Page.getFrameTree").get("frameTree", {}).get("frame", {})
        frame_id = frame.get("id")
        if not frame_id:
            return {"outcome": "failed", "reason": "origin_changed"}
        world = self.call("Page.createIsolatedWorld", {"frameId": frame_id, "grantUniveralAccess": False})
        context_id = world.get("executionContextId")
        if not context_id:
            return {"outcome": "failed", "reason": "refused"}
        try:
            result = self._eval(script, context_id=context_id, sanitize_errors=True)
        finally:
            # Python strings cannot be zeroed; release the grant and generated script references promptly.
            del script, username, password, code
        if not isinstance(result, dict) or result.get("ok") is not True:
            return {"outcome": "failed", "reason": result.get("reason", "refused") if isinstance(result, dict) else "refused"}
        return {"outcome": "ok"}

    def _before(self):
        try:
            return self._eval(BEFORE_JS)
        except BrowserError:
            return None

    def _with_receipt(self, before):
        after = self.summary()
        # Compare the page's body, not its new fence markers; otherwise every action appears to add two lines.
        plain = dict(after, text=parse_fenced(after.get("text")) or "")
        r = receipt(before, plain)
        if r is not None:
            if self.tab_change:
                r["page"] = self.tab_change   # a page it opened is now in front ("opened"), or that page closed ("closed")
            r["appeared"] = [fence_text(line, after.get("fence"), after.get("origin", "unknown"))[0] for line in r["appeared"]]
            after["receipt"] = r
        return after

    def click_href(self, selector=None, text=None):
        """The http(s) address a click would follow, or None (no match, a button, a script link). Read-only."""
        self._need()
        try:
            href = self._eval(TARGET_JS % (json.dumps(selector), json.dumps(text or "")))
        except BrowserError:
            return None
        return href if isinstance(href, str) and href else None

    def click(self, selector=None, text=None):
        self._need()
        before = self._before()
        self._mark()
        if not self._eval(CLICK_JS % (json.dumps(selector), json.dumps(text or "")), gesture=True):
            raise BrowserError("bad_value", "nothing on the page matches %s" % (selector or repr(text)))
        self._settle()
        return self._with_receipt(before)

    def fill(self, selector, text):
        self._need()
        if not self._eval(FOCUS_JS % json.dumps(selector)):
            raise BrowserError("bad_value", "nothing on the page matches %s" % selector)
        self.call("Input.insertText", {"text": text})
        return {"done": True, "selector": selector, "chars": len(text)}

    def press(self, key):
        self._need()
        code, text = KEY_CODES[key]
        before = self._before()
        self._mark()
        base = {"key": key, "code": key if key != "Space" else "Space", "windowsVirtualKeyCode": code, "nativeVirtualKeyCode": code}
        self.call("Input.dispatchKeyEvent", dict(base, type="keyDown", **({"text": text} if text else {})))
        self.call("Input.dispatchKeyEvent", dict(base, type="keyUp"))
        self._settle()
        return self._with_receipt(before)

    def back(self):
        self._need()
        before = self._before()
        self._mark()
        self._eval("history.back()")
        self._settle()
        return self._with_receipt(before)

    def screenshot(self, max_width=1280):
        self._need()
        self.used_at = time.monotonic()
        r = self.call("Page.captureScreenshot", {"format": "jpeg", "quality": 70})
        data = base64.b64decode(r["data"])
        metrics = self.call("Page.getLayoutMetrics")
        view = metrics.get("cssVisualViewport") or metrics.get("visualViewport") or {}
        width, height = int(view.get("clientWidth", 1280)), int(view.get("clientHeight", 900))
        if width > max_width:
            try:
                from PIL import Image
                import io
                img = Image.open(io.BytesIO(data))
                ratio = max_width / float(img.width)
                img = img.resize((max_width, max(1, int(img.height * ratio))))
                out = io.BytesIO()
                img.convert("RGB").save(out, "JPEG", quality=70)
                data, width, height = out.getvalue(), img.width, img.height
            except Exception:
                pass
        return {"format": "jpeg", "width": width, "height": height, "content_b64": base64.b64encode(data).decode("ascii")}

    def _need(self):
        if not self.alive() or not self.session:
            raise BrowserError("not_ready", "no page is open: open a URL first")
        self._follow()

    # ---- a browser desk: the page is the screen ---------------------------------------------------------------------
    def page(self):
        """Make sure the browser runs (a blank page until one is opened): a browser desk always has a screen."""
        self.start()
        self.used_at = time.monotonic()
        self._follow()

    def viewport(self):
        """The page's size in CSS pixels, which mouse events use."""
        metrics = self.call("Page.getLayoutMetrics")
        view = metrics.get("cssVisualViewport") or metrics.get("visualViewport") or {}
        return int(view.get("clientWidth") or self.window[0]), int(view.get("clientHeight") or self.window[1])

    def mouse(self, action, x, y):
        """move, click, double_click or right_click at (x, y) on the page, with what it changed (a receipt) for a click."""
        self.page()
        before = self._before() if action != "move" else None
        self._mark()
        self.call("Input.dispatchMouseEvent", {"type": "mouseMoved", "x": x, "y": y})
        if action == "move":
            return {"done": True}
        button = "right" if action == "right_click" else "left"
        for n in range(1, (2 if action == "double_click" else 1) + 1):
            self.call("Input.dispatchMouseEvent", {"type": "mousePressed", "x": x, "y": y, "button": button, "clickCount": n})
            self.call("Input.dispatchMouseEvent", {"type": "mouseReleased", "x": x, "y": y, "button": button, "clickCount": n})
        self._settle(1.0)
        return self._receipt_only(before)

    def wheel(self, x, y, dy):
        """Scroll at (x, y): dy clicks of the wheel (negative is up), about 100 pixels each."""
        self.page()
        self.call("Input.dispatchMouseEvent", {"type": "mouseWheel", "x": x, "y": y, "deltaX": 0, "deltaY": dy * 100})
        time.sleep(0.2)
        return {"done": True}

    def insert(self, text):
        """Type text where the page's focus is."""
        self.page()
        self.call("Input.insertText", {"text": text})
        return {"done": True}

    def keys(self, chord):
        """One key or chord in xdotool's names (Return, ctrl+a, shift+Tab, F5), pressed on the page."""
        parts = chord.split("+")
        mods = [PAGE_MODIFIERS.get(p.lower()) for p in parts[:-1]]
        if None in mods:
            raise BrowserError("bad_value", "unknown modifier in %s" % chord)
        bits = sum(m[0] for m in mods)
        key = page_key(parts[-1], shift=bool(bits & 8))
        if key is None:
            raise BrowserError("unsupported", "the key %s is not supported on a browser desk" % parts[-1])
        self.page()
        before = self._before()
        self._mark()
        held = 0
        for bit, (k, code, vk) in mods:
            held |= bit
            self.call("Input.dispatchKeyEvent", {"type": "rawKeyDown", "key": k, "code": code, "windowsVirtualKeyCode": vk, "modifiers": held})
        k, code, vk, text = key
        typed = text if text and not bits & 7 else ""   # with ctrl, alt or meta held a key types nothing
        down = {"type": "keyDown" if typed else "rawKeyDown", "key": k, "code": code, "windowsVirtualKeyCode": vk, "modifiers": bits}
        if typed:
            down["text"] = down["unmodifiedText"] = typed
        command = mac_edit_command(bits, parts[-1], getattr(self, "platform", None))
        if command:
            down["commands"] = [command]
        self.call("Input.dispatchKeyEvent", down)
        self.call("Input.dispatchKeyEvent", {"type": "keyUp", "key": k, "code": code, "windowsVirtualKeyCode": vk, "modifiers": bits})
        for bit, (mk, mcode, mvk) in reversed(mods):
            held &= ~bit
            self.call("Input.dispatchKeyEvent", {"type": "keyUp", "key": mk, "code": mcode, "windowsVirtualKeyCode": mvk, "modifiers": held})
        self._settle(1.0)
        return self._receipt_only(before)

    def _receipt_only(self, before):
        out = {"done": True}
        try:
            self._follow()
            after = self._eval(BEFORE_JS)
            r = receipt(before, after)
        except BrowserError:
            r = None
        if r is not None:
            if self.tab_change:
                r["page"] = self.tab_change
            parts = origin_of(after.get("url"))
            origin = "%s://%s:%d" % (parts[0], "[%s]" % parts[1] if ":" in parts[1] else parts[1], parts[2]) if parts else "unknown"
            if not re.fullmatch(r"https?://[A-Za-z0-9.:[\]-]+", origin):
                origin = "unknown"
            r["appeared"] = [fence_text(line, origin=origin)[0] for line in r["appeared"]]
            out["receipt"] = r
        return out

    def frame(self, path, max_width, quality):
        """One JPEG picture of the page at path (written whole, then moved into place), at most max_width wide."""
        self.page()
        width, height = self.viewport()
        params = {"format": "jpeg", "quality": quality}
        if width > max_width:
            params["clip"] = {"x": 0, "y": 0, "width": width, "height": height, "scale": max_width / float(width)}
        data = base64.b64decode(self.call("Page.captureScreenshot", params)["data"])
        with open(path + ".part", "wb") as f:
            f.write(data)
        os.replace(path + ".part", path)

    # ---- cookies a kept profile carries (Node v0.24.1) ------------------------------------------------------------
    def profile_export(self):
        """Private Run transport only; never return this to a model or write it to a log."""
        self._need()
        cookies = self.call("Storage.getCookies", {}, session=False).get("cookies") or []
        frame = self.call("Page.getFrameTree").get("frameTree", {})
        def visit(tree):
            origin = tree.get("frame", {}).get("securityOrigin", "")
            if origin.startswith(("https://", "http://")):
                self.storage_origins.add(origin)
            for child in tree.get("childFrames", []):
                visit(child)
        visit(frame)
        stores = []
        for origin in sorted(self.storage_origins):
            entries = self.call("DOMStorage.getDOMStorageItems", {"storageId": {"securityOrigin": origin, "isLocalStorage": True}}).get("entries", [])
            stores.append({"origin": origin, "entries": entries})
        out = {"cookies": [{k: c[k] for k in COOKIE_FIELDS if k in c} for c in cookies], "origins": stores}
        if len(json.dumps(out).encode()) > 47000:
            raise BrowserError("profile_too_large", "Saved sign-ins exceed this computer's limit")
        return out

    def profile_import(self, profile):
        self._need()
        self.restore_cookies(profile.get("cookies", []))
        for store in profile.get("origins", []):
            origin = store["origin"]
            if not origin.startswith(("https://", "http://")):
                raise BrowserError("bad_value", "Invalid saved site")
            self.storage_origins.add(origin)
            for key, value in store.get("entries", []):
                self.call("DOMStorage.setDOMStorageItem", {"storageId": {"securityOrigin": origin, "isLocalStorage": True}, "key": key, "value": value})
        return {"done": True}

    def profile_sign_out(self, site):
        from urllib.parse import urlparse
        self._need()
        cookies = self.call("Storage.getCookies", {}, session=False).get("cookies") or []

        def under(host):
            # The site and every subdomain of it: signing out of google.com also signs out of accounts.google.com.
            h = (host or "").lstrip(".").lower()
            return h == site or h.endswith("." + site)

        for cookie in cookies:
            if under(cookie.get("domain", "")):
                self.call("Network.deleteCookies", {k: cookie[k] for k in ("name", "domain", "path", "partitionKey") if k in cookie})
        for origin in list(self.storage_origins):
            if under(urlparse(origin).hostname):
                self.call("Storage.clearDataForOrigin", {"origin": origin, "storageTypes": "local_storage,indexeddb,cache_storage,service_workers"})
                self.storage_origins.discard(origin)
        return {"done": True}

    def session_cookies(self):
        """The cookies that end with the browser (no expiry): Chrome drops them when it quits, and a sign-in is often
        one. A kept profile keeps them alongside, for the next desk (restore_cookies)."""
        if not self.alive():
            return []
        got = self.call("Storage.getCookies", {}, session=False).get("cookies") or []
        return [{k: c[k] for k in COOKIE_FIELDS if k in c} for c in got if c.get("session")][:MAX_SESSION_COOKIES]

    def restore_cookies(self, cookies):
        """Put session cookies back (from session_cookies, on another run of the browser)."""
        ok = [c for c in cookies if isinstance(c, dict) and isinstance(c.get("name"), str) and isinstance(c.get("value"), str)
              and isinstance(c.get("domain"), str)][:MAX_SESSION_COOKIES]
        if ok:
            self.call("Storage.setCookies", {"cookies": [{k: c[k] for k in COOKIE_FIELDS if k in c} for c in ok]}, session=False)
        return len(ok)

    def close(self, graceful=False):
        """End the browser. graceful: ask it to quit first (a few seconds), so it writes its cookies and storage to the
        profile (a desk's profile is kept, Node v0.24.0); else, and when it does not quit, it is killed."""
        if graceful and self.alive():
            try:
                self.call("Browser.close", session=False, timeout_s=5.0)
            except BrowserError:
                pass
            try:
                self.proc.wait(timeout=5)
            except Exception:
                pass
        proc, self.proc, self.session = self.proc, None, None
        self.target, self.behind = None, []
        for f in (self.out, self.inp):
            try:
                if f:
                    f.close()
            except OSError:
                pass
        self.out = self.inp = None
        if proc is not None and self.sandbox and proc.poll() is None:
            # The sandbox ends its desk and removes its cgroup, network and root on SIGTERM; killed, it would leave them.
            try:
                proc.terminate()
                proc.wait(timeout=5)
            except Exception:
                pass
        if proc is not None and proc.poll() is None:
            try:
                if hasattr(os, "killpg"):
                    os.killpg(proc.pid, 9)
                else:
                    proc.kill()
            except (OSError, ProcessLookupError):
                pass
        if proc is not None:
            try:
                proc.wait(timeout=5)
            except Exception:
                pass
            if proc.stderr:
                try:
                    proc.stderr.close()
                except OSError:
                    pass
        if self.task_egress is not None:
            self.task_egress.stop()
            self.task_egress = None
        return {"done": True, "closed": proc is not None}
