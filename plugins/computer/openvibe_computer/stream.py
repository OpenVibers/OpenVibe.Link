"""How the desk stream captures a picture (Agent Streams S1).

For a whole desk (an Xvfb display) and the machine's own screen the core runs ffmpeg itself, with the display and size
this module reports: x11grab or PipeWire on Linux, ddagrab on Windows, AVFoundation on macOS. A browser desk has no
display to grab, so its own page is captured here over the DevTools pipe at about five frames a second (the floor of
the design's 5–30 VFR) and written as JPEGs into a small FIFO that the core's ffmpeg reads with `-f image2pipe`.

The FIFO is 0600 in a 0700 folder and is removed with the stream. Nothing here calls the owner's screen: a browser desk
uses its own headless browser, and a named desk's display is its Xvfb one.
"""
from __future__ import annotations

import errno
import os
import subprocess
import sys
import tempfile
import threading
import time

PAGE_STREAM_FPS = 5
PAGE_STREAM_WIDTH = 1280
PAGE_STREAM_QUALITY = 70
FIFO_NAME = "frames.fifo"


def ffmpeg_has_pipewire(ffmpeg):
    """Whether this ffmpeg has the pipewire input device (7.1 and later)."""
    try:
        out = subprocess.run([ffmpeg or "ffmpeg", "-hide_banner", "-devices"],
                             capture_output=True, text=True, timeout=5).stdout
    except (OSError, subprocess.SubprocessError):
        return False
    return "pipewire" in out


def capture_kinds(platform=None):
    platform = platform or sys.platform
    if platform == "win32":
        return ["ddagrab"]
    if platform == "darwin":
        return ["avfoundation"]
    return ["x11grab", "pipewire"]


def desktop_capture(platform, env, width, height, ffmpeg="", pipewire_node=None):
    """The capture spec for a display (a desk's Xvfb or the owner's screen), or a Fault-worthy message."""
    width, height = _size(width, height)
    platform = platform or sys.platform
    if platform == "win32":
        return {"kind": "ddagrab", "width": width, "height": height}
    if platform == "darwin":
        return {"kind": "avfoundation", "device": os.environ.get("OPENVIBE_AVFOUNDATION_SCREEN", "1:none"),
                "width": width, "height": height}
    display = (env or {}).get("DISPLAY")
    if display:
        # x11grab takes `:N.0`; the desk already carries its own display number.
        if not display.endswith(".0"):
            display += ".0"
        return {"kind": "x11grab", "display": display, "width": width, "height": height}
    if (env or {}).get("WAYLAND_DISPLAY"):
        node = pipewire_node or os.environ.get("OPENVIBE_PIPEWIRE_NODE")
        if not node:
            raise ValueError("a Wayland desk needs OPENVIBE_PIPEWIRE_NODE set to the screen-cast node id")
        if not ffmpeg_has_pipewire(ffmpeg):
            raise ValueError("this ffmpeg has no pipewire input; install ffmpeg 7.1+ or set a display")
        return {"kind": "pipewire", "pipewire": str(node), "width": width, "height": height}
    raise ValueError("no display to capture on this machine")


def _size(width, height):
    try:
        width, height = int(width), int(height)
    except (TypeError, ValueError):
        width, height = 1280, 800
    width = max(320, min(3840, width)) & ~15
    height = max(180, min(2160, height)) & ~15
    return width, height


class PageScreencast:
    """Write a browser desk's page into a FIFO as JPEGs for the core's ffmpeg (image2pipe)."""

    def __init__(self, browser, width=PAGE_STREAM_WIDTH, height=800, fps=PAGE_STREAM_FPS, clock=time.monotonic):
        if not hasattr(os, "mkfifo"):
            raise ValueError("a browser desk's stream needs a named pipe (Linux or macOS), which this system has not")
        self.browser = browser
        self.width = width
        self.height = height
        self.fps = max(1, min(10, fps))
        self.clock = clock
        self.stop_event = threading.Event()
        self.thread = None
        self.dir = tempfile.mkdtemp(prefix="openvibe-page-stream-")
        self.fifo = os.path.join(self.dir, FIFO_NAME)
        os.mkfifo(self.fifo, 0o600)
        self.error = None
        self.frames = 0

    def start(self):
        self.thread = threading.Thread(target=self._loop, name="computer-page-stream", daemon=True)
        self.thread.start()
        return self

    def _loop(self):
        fd = None
        try:
            # OPENVIBE's ffmpeg opens the read end; wait for it without blocking the plugin's stop.
            while not self.stop_event.is_set():
                try:
                    fd = os.open(self.fifo, os.O_WRONLY | os.O_NONBLOCK)
                    break
                except OSError as e:
                    if e.errno not in (errno.ENXIO, errno.ENOENT):
                        self.error = str(e)
                        return
                    self.stop_event.wait(0.1)
            if fd is None:
                return
            os.set_blocking(fd, False)
            frame = os.path.join(self.dir, "frame-%d.jpg" % os.getpid())
            period = 1.0 / self.fps
            failures = 0
            while not self.stop_event.is_set():
                started = self.clock()
                try:
                    self.browser.frame(frame, self.width, PAGE_STREAM_QUALITY)
                    with open(frame, "rb") as f:
                        data = f.read()
                    view = memoryview(data)
                    while view:
                        try:
                            n = os.write(fd, view)
                        except BlockingIOError:
                            # ffmpeg is behind: drop this frame rather than grow a queue.
                            n = len(view)
                        view = view[n:]
                    self.frames += 1
                    failures = 0
                except OSError as e:
                    if e.errno == errno.EPIPE:
                        return
                    self.error = str(e)
                    return
                except Exception as e:  # a page that is navigating, a browser that restarted: try again briefly
                    failures += 1
                    if failures >= 10:
                        self.error = str(e)
                        return
                self.stop_event.wait(max(0.0, period - (self.clock() - started)))
        finally:
            if fd is not None:
                try:
                    os.close(fd)
                except OSError:
                    pass
            self._cleanup()

    def _cleanup(self):
        try:
            os.unlink(self.fifo)
        except OSError:
            pass
        try:
            os.rmdir(self.dir)
        except OSError:
            pass

    def status(self):
        return {"fifo": self.fifo, "width": self.width, "height": self.height, "fps": self.fps,
                "frames": self.frames, "error": self.error}

    def stop(self):
        self.stop_event.set()
        thread, self.thread = self.thread, None
        if thread is not None and thread is not threading.current_thread() and thread.is_alive():
            thread.join(2.0)
        self._cleanup()
