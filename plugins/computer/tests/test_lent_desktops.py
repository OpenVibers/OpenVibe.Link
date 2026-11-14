"""Desktops lent to other people's agents (Node v0.65, 2026-11-14): on a desk host for other people whose owner lends
desktops (desk_shared_kinds), a desktop desk is a display, a window manager, a browser and a terminal inside the desk's
own sandbox, driven through its helper over the sandbox's pipe. The plugin's side runs against a fake desktop; the pipe
against a stand-in helper; the last test runs a real desktop in the sandbox and needs what CI's root step gives it
(OPENVIBE_DESK_SANDBOX_TESTS, OPENVIBE_LINK_EXE, Xvfb, xdotool, ImageMagick, openbox and xterm)."""
import base64
import io
import json
import os
import shutil
import struct
import sys
import tempfile
import textwrap
import unittest
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))

from openvibe_computer import Computer  # noqa: E402
from openvibe_computer.sandbox_desk import HELPER, SandboxDesktop  # noqa: E402
from openvibe_plugin import Fault, Runtime  # noqa: E402
from test_computer import Out, reply  # noqa: E402
from test_desk import FakePageBrowser  # noqa: E402

NODE = "/usr/local/bin/openvibe-link"


def tiny_png(w=4, h=3):
    raw = b"".join(b"\x00" + b"\x20\x40\x60" * w for _ in range(h))

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


class FakeLent:
    """A lent desktop, recorded: what started it, the xdotool commands it ran, its pictures."""
    kind, sandboxed = "desktop", True

    def __init__(self, missing=()):
        self._missing, self.on, self.calls, self.size = list(missing), False, [], None

    def missing(self):
        return list(self._missing)

    def available(self):
        return not self._missing

    def window_manager(self):
        return "openbox"

    def terminal(self):
        return "/usr/bin/xterm"

    def running(self):
        return self.on

    def status(self):
        if not self.on:
            return {"running": False, "available": True, "sandboxed": True}
        return {"running": True, "width": self.size[0], "height": self.size[1], "since": 1, "sandboxed": True}

    def start(self, width=1280, height=800):
        self.on, self.size = True, (width, height)
        return self.status()

    def stop(self):
        was, self.on = self.on, False
        return {"running": False, "stopped": was}

    def env(self):
        raise Fault("unsupported", "a lent desktop is used through its screen, mouse and keyboard only")

    def shot(self):
        return tiny_png()

    def xdotool(self, argv):
        self.calls.append(list(argv))
        return {"ok": True, "out": "", "err": ""}


class LentDesktopTest(unittest.TestCase):
    def boot(self, missing=(), **config):
        self.out = Out()
        self.desk_dir = tempfile.mkdtemp(prefix="ov-desks-")
        self.addCleanup(lambda: shutil.rmtree(self.desk_dir, ignore_errors=True))
        self.lent = []

        def lent_factory():
            d = FakeLent(missing)
            self.lent.append(d)
            return d
        self.plugin = Computer(env={"OPENVIBE_LINK_EXE": NODE}, platform="linux", which=lambda n: "/usr/bin/" + n,
                               browser_factory=lambda b, p, h, env=None: FakePageBrowser(b, p, h, env),
                               sandbox_desktop_factory=lent_factory)
        self.rt = Runtime(self.plugin, infile=io.StringIO(""), outfile=self.out)
        self.rt.dispatch({"op": "hello", "config": {"level": "trusted", "notify": False, "desk_dir": self.desk_dir,
                                                    "desk_isolation": "sandbox", **config}})
        self.n = 0
        return self.out.msgs()

    def cmd(self, kind, value):
        self.n += 1
        cid = "l%d" % self.n
        self.rt.dispatch({"op": "command", "id": cid, "kind": kind, "value": value, "deadline_ms": 30000})
        return reply(self.out, cid)

    def started(self):
        return [d for d in self.lent if d.on]

    def test_it_lends_desktops_when_its_owner_says_so(self):
        caps = self.boot(desk_shared_kinds=["browser", "desktop"], desk_desktop_memory_mb=3072)[0]["capabilities"]
        self.assertEqual(list(caps), ["desk"], "still nothing of the machine itself")
        desk = caps["desk"]
        self.assertEqual((desk["kinds"], desk["isolation"], desk["window_manager"]), (["browser", "desktop"], "sandbox", "openbox"))
        self.assertEqual(desk["desktop_limits"], {"memory_mb": 3072, "cpu_millis": 2000, "disk_mb": 1024, "pids": 1024})
        r = self.cmd("desk", {"action": "start", "desk": "desk-a", "kind": "desktop", "width": 1000, "height": 600})
        self.assertEqual(r["op"], "ack", r)
        [d] = self.started()
        self.assertEqual(d.size, (1000, 600))
        shot = self.cmd("screen", {"desk": "desk-a", "max_width": 400})
        self.assertEqual(shot["op"], "ack", shot)
        self.assertIn(shot["data"]["format"], ("jpeg", "png"))
        r = self.cmd("input", {"action": "click", "x": 50, "y": 30, "width": 500, "height": 300, "desk": "desk-a"})
        self.assertEqual(r["op"], "ack", r)
        self.assertEqual((r["data"]["x"], r["data"]["y"]), (100, 60), "a point in a picture lands on the desktop's own pixels")
        self.assertEqual(d.calls[-1][:2], ["xdotool", "mousemove"])
        self.assertEqual(self.cmd("input", {"action": "type", "text": "echo hi", "desk": "desk-a"})["op"], "ack")
        self.assertEqual(d.calls[-1][:2], ["xdotool", "type"])
        r = self.cmd("browser", {"action": "open", "url": "https://example.org/", "desk": "desk-a"})
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "unsupported"), "its browser is a window on the desktop")
        for kind, value in (("exec", {"argv": ["id"], "desk": "desk-a"}), ("screen_stream", {"action": "start", "desk": "desk-a"}),
                            ("screen", {}), ("fs", {"op": "list", "path": "/"})):
            self.assertEqual(self.cmd(kind, value)["op"], "nack", kind)
        self.assertEqual(self.cmd("desk", {"action": "stop", "desk": "desk-a"})["op"], "ack")
        self.assertEqual(self.started(), [], "stopping the desk ends its sandbox")

    def test_a_browser_desk_beside_it_is_unchanged(self):
        self.boot(desk_shared_kinds=["browser", "desktop"])
        self.assertEqual(self.cmd("desk", {"action": "start", "desk": "desk-b", "kind": "browser"})["op"], "ack")
        self.assertEqual(self.cmd("screen", {"desk": "desk-b"})["op"], "ack")
        self.assertEqual(self.started(), [], "no desktop for a browser desk")

    def test_what_a_lent_desktop_must_be(self):
        self.boot(desk_shared_kinds=["browser", "desktop"])
        r = self.cmd("desk", {"action": "start", "kind": "desktop"})
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "bad_value"), "never the machine's own private desk")
        self.assertEqual(self.started(), [])
        # Without what a desktop needs, the host lends browsers only and says what is missing.
        caps = self.boot(missing=("Xvfb", "openbox"), desk_shared_kinds=["browser", "desktop"])[0]["capabilities"]
        self.assertEqual(caps["desk"]["kinds"], ["browser"])
        r = self.cmd("desk", {"action": "start", "desk": "desk-a", "kind": "desktop"})
        self.assertEqual(r["fault_code"], "unsupported")
        self.assertIn("Xvfb, openbox", r["message"])
        # Without the owner's yes, browsers only, as before.
        caps = self.boot()[0]["capabilities"]
        self.assertEqual(caps["desk"]["kinds"], ["browser"])
        self.assertNotIn("desktop_limits", caps["desk"])
        self.assertIn("browser desks only", self.cmd("desk", {"action": "start", "desk": "desk-a", "kind": "desktop"})["message"])
        # Desktops alone.
        caps = self.boot(desk_shared_kinds=["desktop"])[0]["capabilities"]
        self.assertEqual(caps["desk"]["kinds"], ["desktop"])
        self.assertIn("desktop desks only", self.cmd("desk", {"action": "start", "desk": "desk-a", "kind": "browser"})["message"])
        for bad in ([], ["vm"], "desktop"):
            out = Out()
            Runtime(Computer(env={}, platform="linux"), infile=io.StringIO(""), outfile=out).dispatch(
                {"op": "hello", "config": {"level": "trusted", "desk_isolation": "sandbox", "desk_shared_kinds": bad}})
            self.assertEqual(out.msgs()[-1]["op"], "fault", bad)

    @unittest.skipIf(os.name == "nt", "paths of a Linux desk host")
    def test_the_desktop_sandbox_command(self):
        self.boot(desk_shared_kinds=["browser", "desktop"], desk_desktop_memory_mb=4096, desk_desktop_pids=2000)
        argv = self.plugin._desktop_sandbox_argv()
        self.assertEqual(argv[:10], [NODE, "desk-sandbox", "--memory-mb", "4096", "--cpu-millis", "2000", "--disk-mb", "1024", "--pids", "2000"])
        self.assertEqual(argv[10], "--ptys", "its terminal needs pseudo-terminals of its own")
        pairs = list(zip(argv[11:-1:2], argv[12:-1:2]))
        self.assertIn(("--egress", "public"), pairs)
        self.assertIn(("--setenv", "HOME=/tmp/desk"), pairs)
        self.assertEqual(argv[-1], "--")
        apps = self.plugin._desktop_apps(1280, 800)
        self.assertEqual([a[0] for a in apps], ["wm", "browser", "terminal"])
        browser = apps[1][1]
        self.assertEqual(apps[2][1][:3], ["/usr/bin/xterm", "-geometry", "211x13+0-0"], "the terminal along the bottom, in view")
        self.assertEqual(apps[2][1][3], "-e", "with a shell of its own: the sandbox user's login shell is nologin")
        self.assertIn("--window-size=1280,600", browser)
        self.assertIn("--user-data-dir=/tmp/desk/profile", browser)
        self.assertIn("--no-sandbox", browser, "the desk's sandbox replaces Chrome's own")
        self.assertNotIn("--headless=new", browser, "a window on the desktop")


@unittest.skipIf(os.name == "nt", "a lent desktop is a Linux desk host's; its pipe ends are fds 3 and 4")
class HelperPipeTest(unittest.TestCase):
    """SandboxDesktop's side of the pipe, against a stand-in helper: hello, pictures, xdotool, stop, and bad answers."""

    def stand_in(self, body):
        d = tempfile.mkdtemp(prefix="ov-helper-")
        self.addCleanup(lambda: shutil.rmtree(d, ignore_errors=True))
        path = os.path.join(d, "helper.py")
        with open(path, "w") as f:
            f.write(textwrap.dedent("""\
                import base64, json, os, sys
                rd, wr = os.fdopen(3, "rb", buffering=0), os.fdopen(4, "wb", buffering=0)
                def say(o):
                    wr.write((json.dumps(o) + "\\n").encode())
                PNG = base64.b64decode(%r)
                """ % base64.b64encode(tiny_png()).decode()) + textwrap.dedent(body))
        return SandboxDesktop(sandbox_argv=lambda: [sys.executable, path, "--"], apps=lambda w, h: [["wm", ["/usr/bin/openbox"]]],
                              which=lambda n: "/usr/bin/" + n, exists=lambda p: True)

    def test_it_speaks_one_line_each_way(self):
        d = self.stand_in("""
            assert sys.argv[2:5] == [sys.argv[2], "-I", "-S"] and json.loads(sys.argv[-1]) == [["wm", ["/usr/bin/openbox"]]]
            say({"ready": True, "apps": ["wm"]})
            for line in rd:
                req = json.loads(line)
                if req["op"] == "shot":
                    say({"note": "not an answer"})
                    say({"id": req["id"], "ok": True, "png": base64.b64encode(PNG).decode()})
                elif req["op"] == "xdotool":
                    say({"id": req["id"], "ok": req["argv"][1] == "key", "out": "", "err": "" if req["argv"][1] == "key" else "no"})
                elif req["op"] == "stop":
                    say({"id": req["id"], "ok": True})
                    sys.exit(0)
            """)
        st = d.start(800, 600)
        self.assertEqual((st["running"], st["width"], st["sandboxed"]), (True, 800, True))
        self.assertEqual(d.shot(), tiny_png())
        self.assertEqual(d.xdotool(["xdotool", "key", "--", "Return"])["ok"], True)
        self.assertEqual(d.xdotool(["xdotool", "type", "--", "x"])["ok"], False)
        with self.assertRaises(Fault):
            d.xdotool(["sh", "-c", "id"])
        with self.assertRaises(Fault):
            d.env()
        self.assertEqual(d.stop(), {"running": False, "stopped": True})
        self.assertFalse(d.running())

    def test_it_trusts_no_answer(self):
        d = self.stand_in("""
            say({"ready": True})
            for line in rd:
                req = json.loads(line)
                say({"id": req["id"], "ok": True, "png": base64.b64encode(b"GIF89a not a png").decode()})
            """)
        d.start()
        with self.assertRaises(Fault) as e:
            d.shot()
        self.assertIn("not a PNG", e.exception.message)
        d.stop()

    def test_a_helper_that_never_starts(self):
        d = self.stand_in("""
            say({"ready": False, "error": "the display did not start"})
            """)
        with self.assertRaises(Fault) as e:
            d.start()
        self.assertIn("the display did not start", e.exception.message)
        self.assertFalse(d.running())
        with self.assertRaises(Fault):
            d.shot()

    def test_what_the_machine_lacks(self):
        d = SandboxDesktop(sandbox_argv=lambda: [], apps=lambda w, h: [], which=lambda n: None, exists=lambda p: False)
        self.assertEqual(d.missing(), ["python3", "Xvfb", "xdotool", "imagemagick", "openbox"])
        with self.assertRaises(Fault) as e:
            d.start()
        self.assertIn("apt install xvfb", e.exception.message)
        home = SandboxDesktop(sandbox_argv=lambda: [], apps=lambda w, h: [], which=lambda n: "/home/me/bin/" + n, exists=lambda p: True)
        self.assertIn("Xvfb", home.missing(), "a program outside /usr is not in the sandbox")

    def test_the_helper_is_plain_python(self):
        compile(HELPER, "<helper>", "exec")


@unittest.skipUnless(os.environ.get("OPENVIBE_DESK_SANDBOX_TESTS"), "a real lent desktop needs root, a delegated cgroup and the Node")
class RealLentDesktopTest(unittest.TestCase):
    def test_a_real_desktop_in_the_sandbox(self):
        self.assertTrue(os.environ.get("OPENVIBE_LINK_EXE"), "CI's root step builds the Node")
        out = Out()
        plugin = Computer(platform="linux")
        rt = Runtime(plugin, infile=io.StringIO(""), outfile=out)
        rt.dispatch({"op": "hello", "config": {"level": "trusted", "notify": False, "desk_isolation": "sandbox", "desk_egress": "none",
                                                "desk_shared_kinds": ["desktop"], "desk_dir": tempfile.mkdtemp(prefix="ov-desks-")}})
        self.assertEqual(out.msgs()[-1]["op"], "ready", out.msgs())
        self.assertEqual(out.msgs()[0]["capabilities"]["desk"]["kinds"], ["desktop"],
                         "CI's root step installs Xvfb, xdotool, ImageMagick, openbox and xterm: %r" % plugin.sandbox_desktop_factory().missing())
        n = [0]

        def cmd(kind, value):
            n[0] += 1
            cid = "d%d" % n[0]
            rt.dispatch({"op": "command", "id": cid, "kind": kind, "value": value, "deadline_ms": 60000})
            return out.wait_for(lambda m: m.get("id") == cid and m.get("op") in ("ack", "nack"), timeout=120)
        r = cmd("desk", {"action": "start", "desk": "desk-a", "kind": "desktop", "width": 800, "height": 600})
        lent = plugin.desks.get("desk-a")
        self.assertEqual(r["op"], "ack", "%r\nthe sandbox said: %s" % (r, "\n".join(getattr(lent, "stderr_tail", []) or [])))
        try:
            shot = cmd("screen", {"desk": "desk-a", "max_width": 400})
            self.assertEqual(shot["op"], "ack", "%r\nthe sandbox said: %s" % (shot, "\n".join(lent.stderr_tail)))
            self.assertIn(shot["data"]["format"], ("jpeg", "png"))
            self.assertEqual(cmd("input", {"action": "click", "x": 400, "y": 300, "desk": "desk-a"})["op"], "ack")
            self.assertEqual(cmd("input", {"action": "type", "text": "echo sandboxed", "desk": "desk-a"})["op"], "ack")
            self.assertEqual(lent.xdotool(["xdotool", "getdisplaygeometry"])["out"].split(), ["800", "600"], "its own display")
            # Its programs run (the terminal needs the desk's own ptys, Link v0.65.1), and say why when they do not.
            info = lent.logs()
            self.assertTrue(info["running"].get("display") and info["running"].get("wm"), info)
            self.assertTrue(info["running"].get("terminal"), "the terminal runs: %r" % info)
            proc = lent.proc
        finally:
            cmd("desk", {"action": "stop", "desk": "desk-a"})
        self.assertIsNotNone(proc.poll(), "the desktop's sandbox ended with it")
        self.assertEqual([d for d in os.listdir(tempfile.gettempdir()) if d.startswith("openvibe-desk-")], [],
                         "the sandbox removed its root (SIGTERM, not killed)")
        plugin.stop()


if __name__ == "__main__":
    unittest.main()
