"""A whole desktop lent to other people's agents (Node v0.65, 2026-11-14: the owner wants desktops, not only browsers,
shared with everyone). On a desk host for other people (desk_isolation sandbox) whose owner also lends desktops
(``desk_shared_kinds: ["browser", "desktop"]``), a desktop desk is an X display, a window manager, a browser and a
terminal, every one of them inside that desk's own ``openvibe-link desk-sandbox``: the jobs' sandbox, as worker.run_as,
its own namespaces and cgroup, only the public internet, only the system's own paths and the desk's /tmp. Nothing of the
desktop runs on the machine itself, and two desktops never share a display, a process, a file or a network.

The plugin cannot reach a display inside the sandbox (its socket lives in the sandbox's /tmp and network namespace), so
a small helper (HELPER below, run there by the system's own Python) starts the desktop and answers the plugin over the
sandbox's two pipe ends, fds 3 and 4, one JSON line each way:

    {"id": n, "op": "shot"}                    -> {"id": n, "ok": true, "png": "<base64>"}   the whole screen
    {"id": n, "op": "xdotool", "argv": [...]}  -> {"id": n, "ok": bool, "out": "...", "err": "..."}
    {"id": n, "op": "stop"}                    -> {"id": n, "ok": true}, and the desktop ends

The helper and what runs beside it belong to the person the desk is lent to: whatever it answers is theirs, so the
plugin trusts none of it beyond its size (MAX_REPLY), its time (each call's timeout) and its shape.
"""

import base64
import json
import os
import subprocess
import threading
import time

from openvibe_plugin import Fault

# The window managers a lent desktop may use, the lightest first (none needs a session bus), and its terminals.
SANDBOX_WINDOW_MANAGERS = ("openbox", "fluxbox", "xfwm4", "metacity")
SANDBOX_TERMINALS = ("xterm", "xfce4-terminal", "lxterminal")
SYSTEM_PYTHON = "/usr/bin/python3"
START_S = 60.0               # Xvfb, the window manager and the first frame in a cold sandbox
MAX_REPLY = 32 << 20         # a 4K screen's PNG is a few MB; base64 adds a third
SHOT_S = 20.0
INPUT_S = 35.0               # the helper gives xdotool 30 s

# Runs inside the sandbox as `python3 -I -S -c HELPER <width> <height> <apps JSON>`. Its environment is only what the
# sandbox was given (SANDBOX_ENV); the desktop's own is built here. Standard library only, Python 3.6 and later.
HELPER = r'''
import base64, json, os, shutil, signal, subprocess, sys, time
W, H, APPS = int(sys.argv[1]), int(sys.argv[2]), json.loads(sys.argv[3])
HOME = os.environ.get("HOME") or "/tmp/desk"
ENV = {"DISPLAY": ":0", "HOME": HOME, "TMPDIR": HOME, "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "xterm-256color",
       "SHELL": "/bin/bash" if os.path.exists("/bin/bash") else "/bin/sh",
       "DBUS_SESSION_BUS_ADDRESS": "disabled:", "XDG_RUNTIME_DIR": HOME + "/run", "NO_AT_BRIDGE": "1"}
os.makedirs(ENV["XDG_RUNTIME_DIR"], mode=0o700, exist_ok=True)
rd = os.fdopen(3, "rb", buffering=0)
wr = os.fdopen(4, "wb", buffering=0)
procs = []
def say(o):
    wr.write((json.dumps(o, separators=(",", ":")) + "\n").encode())
def end(code):
    for p in reversed(procs):
        try:
            p.terminate()
        except Exception:
            pass
    t = time.time() + 3
    for p in procs:
        try:
            p.wait(max(0.1, t - time.time()))
        except Exception:
            try:
                p.kill()
            except Exception:
                pass
    os._exit(code)
signal.signal(signal.SIGTERM, lambda *a: end(0))
LOGS = HOME + "/.logs"
os.makedirs(LOGS, mode=0o700, exist_ok=True)
def logged(name):
    f = open("%s/%s.log" % (LOGS, name), "ab")
    return dict(stdin=subprocess.DEVNULL, stdout=f, stderr=f, cwd=HOME)
# Xvfb is not root in here, so it will not make the socket's directory itself.
os.makedirs("/tmp/.X11-unix", exist_ok=True)
os.chmod("/tmp/.X11-unix", 0o1777)
x = subprocess.Popen(["Xvfb", ":0", "-screen", "0", "%dx%dx24" % (W, H), "-nolisten", "tcp", "-noreset"], env=ENV, **logged("display"))
procs.append(x)
for _ in range(300):
    if os.path.exists("/tmp/.X11-unix/X0") or x.poll() is not None:
        break
    time.sleep(0.1)
time.sleep(0.2)
if x.poll() is not None or not os.path.exists("/tmp/.X11-unix/X0"):
    say({"ready": False, "error": "the display did not start"})
    end(1)
started = []
for name, argv in APPS:
    try:
        procs.append(subprocess.Popen(argv, env=ENV, start_new_session=True, **logged(name)))
        started.append(name)
        if name == "wm":
            time.sleep(0.5)
    except OSError:
        pass
say({"ready": True, "apps": started})
def run(argv, timeout):
    return subprocess.run(argv, env=ENV, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=HOME, timeout=timeout)
for line in rd:
    try:
        req = json.loads(line.decode("utf-8", "replace"))
        rid, op = req.get("id"), req.get("op")
    except Exception:
        continue
    try:
        if op == "shot":
            if shutil.which("import", path=ENV["PATH"]):
                p = run(["import", "-silent", "-window", "root", "png:-"], 15)
                data = p.stdout
            else:
                path = HOME + "/.shot.png"
                p = run(["scrot", "-o", path], 15)
                with open(path, "rb") as f:
                    data = f.read()
                os.unlink(path)
            if p.returncode != 0 or not data:
                raise RuntimeError(p.stderr.decode("utf-8", "replace")[:200] or "no picture")
            say({"id": rid, "ok": True, "png": base64.b64encode(data).decode("ascii")})
        elif op == "xdotool":
            argv = req.get("argv")
            if not (isinstance(argv, list) and argv and argv[0] == "xdotool" and all(isinstance(a, str) for a in argv)):
                raise ValueError("xdotool and its arguments")
            p = run(argv, 30)
            say({"id": rid, "ok": p.returncode == 0, "out": p.stdout.decode("utf-8", "replace")[:4000],
                 "err": p.stderr.decode("utf-8", "replace")[:400]})
        elif op == "logs":
            # What each program said (its last 2 KB) and whether it still runs: why a desktop shows nothing.
            tails = {}
            for f in sorted(os.listdir(LOGS))[:8]:
                with open("%s/%s" % (LOGS, f), "rb") as fh:
                    fh.seek(0, 2)
                    fh.seek(max(0, fh.tell() - 2048))
                    tails[f[:-4]] = fh.read().decode("utf-8", "replace")
            alive = {n: p.poll() is None for n, p in zip(["display"] + started, procs)}
            say({"id": rid, "ok": True, "logs": tails, "running": alive})
        elif op == "stop":
            say({"id": rid, "ok": True})
            end(0)
        else:
            raise ValueError("unknown op")
    except Exception as e:
        say({"id": rid, "ok": False, "err": str(e)[:300]})
    if x.poll() is not None:
        say({"ended": True})
        end(1)
end(0)
'''


def _under_usr(path):
    """A program the sandbox can run: only /usr (and /bin, /sbin, /lib…, which the sandbox binds too) is there."""
    if not path:
        return None
    real = os.path.realpath(path)
    return real if real.startswith(("/usr/", "/bin/", "/sbin/")) else None


class SandboxDesktop:
    """One lent desktop: kind desktop, its display and apps inside its own sandbox, driven through HELPER.

    ``sandbox_argv()`` gives the ``openvibe-link desk-sandbox … --`` prefix (the plugin's, with the desktop's limits),
    ``apps(width, height)`` the programs it starts after its display, as [(name, argv)], the window manager first.
    """
    kind = "desktop"
    sandboxed = True

    def __init__(self, sandbox_argv, apps, which, popen=subprocess.Popen, clock=time.time, python=SYSTEM_PYTHON,
                 exists=os.path.exists, native=None):
        self.sandbox_argv = sandbox_argv
        self.apps = apps
        self.which = which
        self.popen = popen
        self.clock = clock
        self.python = python
        self.exists = exists
        # The Go helper (`openvibe-link desk-display`, Link v0.67): the Link's own binary where the sandbox can see it
        # (under /usr or /opt). It captures and drives the display in-process; HELPER is the fallback.
        self.native = native
        self.paths = {}  # how the Go helper makes pictures: {"capture": "shm"|"socket", "encoder": "libjpeg-turbo"|"go"}
        self.mu = threading.Lock()      # one start or stop at a time
        self.call_mu = threading.Lock()  # one request on the pipe at a time
        self.proc = self.out = self.inp = None
        self.width = self.height = self.since = None
        self.n = 0
        self.buf = bytearray()
        self.stderr_tail = []

    # ---- what the machine has --------------------------------------------------------------------------------------
    def _tool(self, name):
        return _under_usr(self.which(name))

    def missing(self):
        """What this machine lacks to lend a desktop (empty: it can)."""
        out = []
        if not self.native and not self.exists(self.python):
            out.append("python3")
        for name in ("Xvfb", "xdotool"):
            if not self._tool(name):
                out.append(name)
        if not (self._tool("import") or self._tool("scrot")):
            out.append("imagemagick")
        if not self.window_manager():
            out.append("openbox")
        return out

    def available(self):
        return not self.missing()

    def window_manager(self):
        for name in SANDBOX_WINDOW_MANAGERS:
            if self._tool(name):
                return name
        return None

    def terminal(self):
        for name in SANDBOX_TERMINALS:
            path = self._tool(name)
            if path:
                return path
        return None

    # ---- its life ----------------------------------------------------------------------------------------------------
    def running(self):
        return self.proc is not None and self.proc.poll() is None

    def status(self):
        if not self.running():
            return {"running": False, "available": self.available(), "sandboxed": True}
        return {"running": True, "width": self.width, "height": self.height, "since": int(self.since), "sandboxed": True,
                "window_manager": self.window_manager()}

    def start(self, width=1280, height=800):
        with self.mu:
            if self.running():
                return self.status()
            missing = self.missing()
            if missing:
                raise Fault("unsupported", "a lent desktop needs %s on this machine (Debian/Ubuntu: apt install xvfb "
                                           "xdotool imagemagick openbox xterm)" % ", ".join(missing))
            apps = json.dumps(self.apps(width, height))
            head = [self.native, "desk-display"] if self.native else [self.python, "-I", "-S", "-c", HELPER]
            argv = list(self.sandbox_argv()) + head + [str(width), str(height), apps]
            self.buf = bytearray()
            to_r, to_w = os.pipe()
            from_r, from_w = os.pipe()

            def fds():
                # In the child: the helper reads on 3 and writes on 4 (as the browser's pipe; see Browser._start).
                import fcntl
                r = fcntl.fcntl(to_r, fcntl.F_DUPFD, 10)
                w = fcntl.fcntl(from_w, fcntl.F_DUPFD, 10)
                os.dup2(r, 3)
                os.dup2(w, 4)
            try:
                self.proc = self.popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                                       pass_fds=(3, 4), preexec_fn=fds, start_new_session=True)
            finally:
                os.close(to_r)
                os.close(from_w)
            self.out = os.fdopen(to_w, "wb", buffering=0)
            self.inp = os.fdopen(from_r, "rb", buffering=0)
            if getattr(self.proc, "stderr", None) is not None:
                threading.Thread(target=self._stderr, args=(self.proc.stderr,), name="computer-desktop-err", daemon=True).start()
            hello = self._read(START_S)
            if not hello or hello.get("ready") is not True:
                why = (hello or {}).get("error") or "it did not answer in %d s" % START_S
                self._end()
                raise Fault("hardware", "the lent desktop did not start: %s%s" % (why, self._tail()))
            self.width, self.height, self.since = width, height, self.clock()
            self.paths = {k: hello[k] for k in ("capture", "encoder") if isinstance(hello.get(k), str)}
            return self.status()

    def stop(self):
        with self.mu:
            was = self.running()
            if was:
                try:
                    self.call("stop", timeout_s=5)
                except Fault:
                    pass
            self._end()
            return {"running": False, "stopped": was}

    def env(self):
        # Nothing of a lent desktop runs on the machine, so there is no environment here to run a tool in.
        raise Fault("unsupported", "a lent desktop is used through its screen, mouse and keyboard only")

    # ---- the helper's answers ----------------------------------------------------------------------------------------
    def call(self, op, timeout_s, **args):
        """One request to the helper and its answer; a Fault when it is gone, slow, too large or not an answer."""
        with self.call_mu:
            if not self.running() or self.out is None:
                raise Fault("not_ready", "the desktop is not running: start it again (desk start)")
            self.n += 1
            rid = self.n
            try:
                self.out.write((json.dumps(dict(args, id=rid, op=op)) + "\n").encode("utf-8"))
            except OSError:
                raise Fault("hardware", "the desktop stopped answering%s" % self._tail())
            deadline = self.clock() + timeout_s
            while True:
                left = deadline - self.clock()
                msg = self._read(max(0.1, left)) if left > 0 else None
                if msg is None:
                    raise Fault("hardware", "the desktop did not answer in %d s" % timeout_s)
                if msg.get("ended"):
                    raise Fault("hardware", "the desktop's display ended")
                if msg.get("id") == rid:
                    return msg

    def shot(self, max_width=0, max_bytes=0):
        """The whole screen: ("png", bytes) from the Python helper, or ("jpeg", bytes, width, height) made in the Go
        helper at max_width and within max_bytes (nothing to re-encode)."""
        msg = self.call("shot", SHOT_S, max_width=int(max_width or 0), max_bytes=int(max_bytes or 0))
        if not msg.get("ok") or not isinstance(msg.get("png") or msg.get("jpeg"), str):
            raise Fault("hardware", "the desktop's screen could not be captured: %s" % str(msg.get("err") or "no picture")[:200])
        kind = "jpeg" if isinstance(msg.get("jpeg"), str) else "png"
        try:
            data = base64.b64decode(msg[kind], validate=True)
        except (ValueError, TypeError):
            raise Fault("hardware", "the desktop sent a picture that is not one")
        if kind == "png" and not data.startswith(b"\x89PNG\r\n\x1a\n"):
            raise Fault("hardware", "the desktop sent a picture that is not a PNG")
        if kind == "jpeg":
            if not data.startswith(b"\xff\xd8\xff"):
                raise Fault("hardware", "the desktop sent a picture that is not a JPEG")
            w, h = msg.get("width"), msg.get("height")
            if not (isinstance(w, int) and isinstance(h, int) and 0 < w <= 7680 and 0 < h <= 4320):
                raise Fault("hardware", "the desktop sent a picture without its size")
            return ("jpeg", data, w, h)
        return ("png", data)

    def logs(self):
        """What each program on the desktop said lately, and which still run: {logs: {name: text}, running: {name: bool}}."""
        msg = self.call("logs", SHOT_S)
        logs = msg.get("logs") if isinstance(msg.get("logs"), dict) else {}
        running = msg.get("running") if isinstance(msg.get("running"), dict) else {}
        return {"logs": {str(k)[:40]: str(v)[-2048:] for k, v in list(logs.items())[:8]},
                "running": {str(k)[:40]: v is True for k, v in list(running.items())[:8]}}

    def xdotool(self, argv):
        """Run one xdotool command on the desktop's display: {ok, out, err}."""
        if not argv or argv[0] != "xdotool" or not all(isinstance(a, str) for a in argv):
            raise Fault("bad_value", "an xdotool command")
        msg = self.call("xdotool", INPUT_S, argv=list(argv))
        return {"ok": msg.get("ok") is True, "out": str(msg.get("out") or "")[:4000], "err": str(msg.get("err") or "")[:400]}

    def _read(self, timeout_s):
        """The helper's next line as an object, None at its end or after timeout_s; a reply over MAX_REPLY ends it."""
        if self.inp is None:
            return None
        import select
        deadline = self.clock() + timeout_s
        fd = self.inp.fileno()
        while True:
            nl = self.buf.find(b"\n")
            if nl >= 0:
                line, self.buf = bytes(self.buf[:nl]), self.buf[nl + 1:]
                try:
                    msg = json.loads(line.decode("utf-8"))
                except ValueError:
                    continue
                if isinstance(msg, dict):
                    return msg
                continue
            if len(self.buf) > MAX_REPLY:
                self._end()
                raise Fault("hardware", "the desktop sent an answer larger than %d MB" % (MAX_REPLY >> 20))
            left = deadline - self.clock()
            if left <= 0:
                return None
            ready, _, _ = select.select([fd], [], [], min(left, 1.0))
            if not ready:
                if not self.running():
                    return None
                continue
            chunk = os.read(fd, 1 << 16)
            if not chunk:
                return None
            self.buf += chunk

    def _stderr(self, stream):
        try:
            for line in stream:
                self.stderr_tail = (self.stderr_tail + [line.decode("utf-8", "replace").rstrip()[:300]])[-8:]
        except (OSError, ValueError):
            pass

    def _tail(self):
        return (": " + " | ".join(self.stderr_tail[-3:])) if self.stderr_tail else ""

    def _end(self):
        proc, self.proc = self.proc, None
        for f in (self.out, self.inp):
            try:
                if f:
                    f.close()
            except OSError:
                pass
        self.out = self.inp = None
        self.buf = bytearray()
        self.width = self.height = self.since = None
        if proc is None:
            return
        if proc.poll() is None:
            # SIGTERM first: the sandbox then ends the desk and removes its cgroup, network and root.
            try:
                proc.terminate()
                proc.wait(timeout=8)
            except Exception:
                pass
        if proc.poll() is None:
            try:
                if hasattr(os, "killpg"):
                    os.killpg(proc.pid, 9)
                else:
                    proc.kill()
            except (OSError, ProcessLookupError):
                pass
        try:
            proc.wait(timeout=5)
        except Exception:
            pass
        try:
            if getattr(proc, "stderr", None) is not None:
                proc.stderr.close()
        except (OSError, ValueError):
            pass
