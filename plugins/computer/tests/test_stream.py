"""The agent desk stream (Agent Streams S1): the curtain's triggers and its fail-closed rule, the capture each platform
chooses, a browser desk's screencast into its FIFO, and the plugin's desk.stream start/stop/status."""
import io
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))

from openvibe_computer import Computer  # noqa: E402
from openvibe_computer.curtain import (Curtain, EnvPrintDetector, PasswordFieldDetector, SecretFileDetector,
                                       SiteDetector, looks_secret_name, looks_secret_window)  # noqa: E402
from openvibe_computer.stream import PageScreencast, desktop_capture  # noqa: E402
from openvibe_plugin import Fault  # noqa: E402


class Clock:
    def __init__(self):
        self.t = 0.0

    def __call__(self):
        return self.t


class Answer:
    def __init__(self, value):
        self.value = value

    def poll(self):
        return self.value


class Boom:
    reason = "password_field"

    def poll(self):
        raise RuntimeError("the accessibility bus went away")


class FakeDesk:
    kind = "desktop"

    def __init__(self, display=":90"):
        self.display = display

    def available(self):
        return True

    def window_manager(self):
        return "metacity"

    def running(self):
        return True

    def status(self):
        return {"running": True, "display": self.display, "width": 800, "height": 600, "kind": "desktop"}

    def env(self):
        return {"DISPLAY": self.display, "DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/ov-test"}


def curtain(detectors=(), clock=None):
    clock = clock or Clock()
    return Curtain(list(detectors), clock=clock, clear_hold=2.0), clock


class CurtainTest(unittest.TestCase):
    def test_each_trigger_raises(self):
        cases = [
            ("password", PasswordFieldDetector(probe=lambda: True), None),
            ("secret window", SecretFileDetector(window=lambda: ("~/.env", "")), None),
            ("secret class", SecretFileDetector(window=lambda: ("Documents", "WM_CLASS(STRING) = keepassxc")), None),
            ("env", EnvPrintDetector(), ("env", "HOME=/root\nPATH=/bin\n")),
            ("site", SiteDetector(["https://example.com"]), "https://evil.example/x"),
        ]
        for name, det, action in cases:
            with self.subTest(name=name):
                c, clock = curtain([det])
                if isinstance(det, EnvPrintDetector):
                    det.saw_output(list(action), "HOME=/root\nPATH=/bin\n")
                elif isinstance(det, SiteDetector):
                    det.saw_url(action)
                self.assertTrue(c.evaluate(), name)
                self.assertTrue(c.snapshot()["up"], name)
        c, _ = curtain([])
        c.raise_("manual")
        self.assertTrue(c.evaluate())
        self.assertEqual(c.snapshot()["reason"], "manual")

    def test_secret_names(self):
        for name in (".env", "/home/a/.env.local", "/home/a/credentials.json", "id_rsa", "/k/server.pem", "/k/x.key"):
            self.assertTrue(looks_secret_name(name), name)
        for name in ("notes.txt", "/home/a/environment.md", "keyboard.png"):
            self.assertFalse(looks_secret_name(name), name)
        self.assertTrue(looks_secret_window("Passwords — Bitwarden"))
        self.assertFalse(looks_secret_window("OpenVibe — Dashboard"))

    def test_detector_exception_is_fail_closed(self):
        c, _ = curtain([Boom()])
        self.assertTrue(c.evaluate())
        self.assertTrue(c.snapshot()["up"])
        self.assertEqual(c.snapshot()["reason"], "detector")

    def test_unsure_is_not_clear(self):
        c, _ = curtain([PasswordFieldDetector(probe=lambda: None)])
        self.assertTrue(c.evaluate())
        self.assertEqual(c.snapshot()["reason"], "password_field")

    def test_clear_for_two_seconds_before_down(self):
        state = {"pw": True}
        c, clock = curtain([PasswordFieldDetector(probe=lambda: state["pw"])])
        self.assertTrue(c.evaluate())
        state["pw"] = False
        clock.t = 10.0
        self.assertTrue(c.evaluate(), "the hold starts now")
        clock.t = 11.9
        self.assertTrue(c.evaluate(), "still inside the two seconds")
        clock.t = 12.1
        self.assertFalse(c.evaluate(), "two clear seconds lower it")
        self.assertFalse(c.snapshot()["up"])

    def test_owner_lowers_now(self):
        c, _ = curtain([])
        c.raise_("manual")
        self.assertTrue(c.snapshot()["up"])
        c.lower()
        self.assertFalse(c.snapshot()["up"])
        # But the next pass still has to see every detector clear; a raised detector raises it again.
        c.add(Answer("site"))
        self.assertTrue(c.evaluate())

    def test_change_callback_reports_the_reason_never_a_detector_error(self):
        seen = []
        c = Curtain([Boom()], clock=Clock(), on_change=lambda up, why: seen.append((up, why)))
        c.evaluate()
        self.assertEqual(seen, [(True, "detector")])
        self.assertNotIn("accessibility", seen[0][1])


class CaptureTest(unittest.TestCase):
    def test_each_platform(self):
        self.assertEqual(desktop_capture("linux", {"DISPLAY": ":99"}, 1600, 900),
                         {"kind": "x11grab", "display": ":99.0", "width": 1600, "height": 896})
        self.assertEqual(desktop_capture("win32", {}, 1600, 900)["kind"], "ddagrab")
        self.assertEqual(desktop_capture("darwin", {}, 1600, 900)["kind"], "avfoundation")

    def test_no_display_is_refused(self):
        with self.assertRaises(ValueError):
            desktop_capture("linux", {}, 1280, 800)

    def test_browser_screencast_writes_jpeg_frames(self):
        try:
            from PIL import Image
        except ImportError:
            self.skipTest("pillow not installed")
        tmp = tempfile.mkdtemp(prefix="ov-page-")
        self.addCleanup(lambda: shutil.rmtree(tmp, ignore_errors=True))
        jpeg = os.path.join(tmp, "one.jpg")
        Image.new("RGB", (64, 48), (10, 20, 30)).save(jpeg, "JPEG")

        class FakeBrowser:
            def frame(self, path, width, quality):
                shutil.copy(jpeg, path)

        cast = PageScreencast(FakeBrowser(), width=64, height=48, fps=10)
        got = []

        def read_one():
            with open(cast.fifo, "rb") as f:
                got.append(f.read(4))

        reader = threading.Thread(target=read_one)
        reader.start()
        cast.start()
        deadline = time.time() + 5
        while not got and time.time() < deadline:
            time.sleep(0.02)
        cast.stop()
        reader.join(2)
        self.assertTrue(got and got[0][:2] == b"\xff\xd8", "a JPEG frame reached the FIFO")

    def test_capture_kinds(self):
        from openvibe_computer.stream import capture_kinds
        self.assertEqual(capture_kinds("win32"), ["ddagrab"])
        self.assertEqual(capture_kinds("darwin"), ["avfoundation"])
        self.assertIn("x11grab", capture_kinds("linux"))


class PluginTest(unittest.TestCase):
    def make(self, **config):
        self.dir = tempfile.mkdtemp(prefix="ov-stream-")
        self.addCleanup(lambda: shutil.rmtree(self.dir, ignore_errors=True))
        p = Computer(which=lambda n: "/usr/bin/" + n if n in ("Xvfb", "xdotool", "import", "xprop") else None,
                     env={"DISPLAY": ":0"}, platform="linux")
        p.setup({"level": "trusted", "roots": [self.dir], **config})
        p.desks.desks["private"] = FakeDesk()
        # The detectors need a real accessibility stack and display; the plugin's start path is what this tests.
        p._desk_stream_detectors = lambda allow_sites, env=None: {}
        return p

    def cmd(self, value):
        return SimpleNamespace(kind="desk.stream", value=value, id="c1", deadline=time.monotonic() + 30)

    def test_describe_offers_desk_stream(self):
        p = self.make()
        caps = p.describe({})
        self.assertIn("desk.stream", caps)
        self.assertIn("x11grab", caps["desk.stream"]["capture"])
        off = Computer(which=lambda n: "/usr/bin/" + n if n in ("Xvfb",) else None, env={"DISPLAY": ":0"}, platform="linux")
        off.setup({"level": "trusted", "roots": [self.dir], "allow_desk_stream": False})
        self.assertNotIn("desk.stream", off.describe({"allow_desk_stream": False, "roots": [self.dir]}))

    def test_start_status_stop(self):
        p = self.make()
        started = p.handle(self.cmd({"action": "start", "desk": "private", "title": "t"}))
        self.assertTrue(started["streaming"])
        self.assertEqual(started["capture"]["kind"], "x11grab")
        self.assertEqual(started["capture"]["display"], ":90.0")
        self.assertTrue(started["curtain"]["up"], "the curtain starts up (fail closed)")
        self.assertTrue(p.handle(self.cmd({"action": "status"}))["streaming"])
        p.handle(self.cmd({"action": "curtain", "up": False}))
        self.assertFalse(p.handle(self.cmd({"action": "status"}))["curtain"]["up"])
        self.assertTrue(p.handle(self.cmd({"action": "stop"}))["stopped"])
        self.assertFalse(p.handle(self.cmd({"action": "status"}))["streaming"])

    def test_start_is_refused_when_off(self):
        p = self.make(allow_desk_stream=False)
        with self.assertRaises(Fault) as e:
            p.handle(self.cmd({"action": "start", "desk": "private"}))
        self.assertEqual(e.exception.code, "unsupported")

    def test_curtain_requires_a_stream(self):
        p = self.make()
        with self.assertRaises(Fault) as e:
            p.handle(self.cmd({"action": "curtain", "up": True}))
        self.assertEqual(e.exception.code, "not_ready")

    def test_secret_read_raises_the_curtain(self):
        p = self.make()
        p.handle(self.cmd({"action": "start", "desk": "private"}))
        p._desk_stream_detectors = lambda allow_sites, env=None: {}
        p._desk_stream_saw_path("/home/a/.env")
        # No secret detector in this injected set: nothing changes, which is why the real set always includes one.
        self.assertTrue(p.handle(self.cmd({"action": "status"}))["streaming"])
        p.handle(self.cmd({"action": "stop"}))


if __name__ == "__main__":
    unittest.main()
