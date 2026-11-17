"""Models the machine runs itself (plugin 0.5.0, Link v0.79, plan §AD.23). With a stand-in llama-server (a small HTTP
server that answers /health, /v1/models and /v1/chat/completions as llama.cpp's does), packed in a tarball like
llama.cpp's release, and the downloads served through an opener: entries are checked; downloads that do not match their
sha256 are refused and leave nothing; an archive with a path outside itself is refused; a server that exits is started
again; and the plugin fetches, serves and answers by alias, then stops its servers with it."""
import hashlib
import io
import json
import os
import sys
import tarfile
import tempfile
import threading
import time
import unittest
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))
from openvibe_plugin import Runtime  # noqa: E402
from openvibe_ai import Ai  # noqa: E402
from openvibe_ai import serve  # noqa: E402

FAKE_SERVER = r'''#!%s
import http.server, json, sys
args = sys.argv[1:]
port = int(args[args.index("--port") + 1]); alias = args[args.index("--alias") + 1]
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _send(self, doc):
        b = json.dumps(doc).encode(); self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path == "/health": return self._send({"status": "ok"})
        if self.path == "/v1/models": return self._send({"data": [{"id": alias}]})
        self.send_response(404); self.end_headers()
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0); body = json.loads(self.rfile.read(n) or b"{}")
        self._send({"choices": [{"message": {"role": "assistant", "content": "served by %%s" %% alias}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 3}})
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
''' % sys.executable


def tarball(members):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as t:
        for name, data, mode in members:
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(data), mode
            t.addfile(info, io.BytesIO(data))
    return buf.getvalue()


class Files:
    """An opener that serves https://test.invalid/<name> from memory and passes everything else on."""

    def __init__(self, files):
        self.files, self.asked = files, []

    def __call__(self, req, timeout=None):
        url = req.full_url if hasattr(req, "full_url") else req
        if url.startswith("https://test.invalid/"):
            self.asked.append(url)
            return io.BytesIO(self.files[url.rsplit("/", 1)[-1]])
        return urllib.request.urlopen(req, timeout=timeout)


GGUF = b"GGUF" + b"\0" * 1000
LLAMA_TGZ = tarball([("build/bin/llama-server", FAKE_SERVER.encode(), 0o755)])
SHA = lambda b: hashlib.sha256(b).hexdigest()  # noqa: E731


class Out(io.StringIO):
    def msgs(self):
        return [json.loads(x) for x in self.getvalue().splitlines() if x]

    def wait_for(self, pred, timeout=15):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            found = [m for m in self.msgs() if pred(m)]
            if found:
                return found[0]
            time.sleep(0.05)
        raise AssertionError("no such message: %s" % self.getvalue()[-500:])


class ServeTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.saved = dict(serve.LLAMA)
        serve.LLAMA["x86_64"] = serve.LLAMA["aarch64"] = ("https://test.invalid/llama.tgz", SHA(LLAMA_TGZ))

    def tearDown(self):
        serve.LLAMA.clear()
        serve.LLAMA.update(self.saved)
        import shutil
        shutil.rmtree(self.root, ignore_errors=True)

    def test_entries_are_checked(self):
        q = serve.spec_of({"model": "qwen2.5-1.5b-instruct"})
        self.assertEqual((q["alias"], q["sha256"][:8], q["embedding"]), ("qwen2.5-1.5b-instruct", "6a1a2eb6", False))
        self.assertTrue(serve.spec_of({"model": "bge-small-en-v1.5"})["embedding"])
        for bad, why in (({"model": "nope"}, "not in the catalog"), ({"alias": "x", "url": "http://a/b.gguf", "sha256": "0" * 64}, "https"),
                         ({"alias": "x", "url": "https://a/b.gguf", "sha256": "zz"}, "sha256"), ({"alias": "../x", "url": "https://a/b", "sha256": "0" * 64}, "alias"),
                         ({"model": "bge-small-en-v1.5", "threads": 0}, "threads")):
            with self.assertRaises(serve.ServeError) as e:
                serve.spec_of(bad)
            self.assertIn(why, str(e.exception))

    def test_a_download_that_does_not_match_is_refused_and_leaves_nothing(self):
        files = Files({"m.gguf": GGUF})
        dest = os.path.join(self.root, "models", "m.gguf")
        self.assertEqual(serve.fetch_checked("https://test.invalid/m.gguf", SHA(GGUF), dest, opener=files), dest)
        serve.fetch_checked("https://test.invalid/m.gguf", SHA(GGUF), dest, opener=files)
        self.assertEqual(len(files.asked), 1, "a file already there and matching is not fetched again")
        other = os.path.join(self.root, "models", "other.gguf")
        with self.assertRaises(serve.ServeError):
            serve.fetch_checked("https://test.invalid/m.gguf", "0" * 64, other, opener=files)
        self.assertFalse(os.path.exists(other) or os.path.exists(other + ".part"))

    def test_an_archive_with_a_path_outside_itself_is_refused(self):
        evil = tarball([("../evil", b"x", 0o644)])
        serve.LLAMA["x86_64"] = serve.LLAMA["aarch64"] = ("https://test.invalid/evil.tgz", SHA(evil))
        with self.assertRaises(serve.ServeError):
            serve.llama_server(self.root, opener=Files({"evil.tgz": evil}))
        self.assertFalse(os.path.exists(os.path.join(self.root, "evil")))

    def test_a_server_that_exits_is_started_again(self):
        binary = serve.llama_server(self.root, opener=Files({"llama.tgz": LLAMA_TGZ}))
        s = serve.Served(serve.spec_of({"alias": "tiny", "url": "https://test.invalid/m.gguf", "sha256": SHA(GGUF)}), binary, "/dev/null").start()
        self.addCleanup(s.stop)
        end = time.monotonic() + 15
        while not s.ready and time.monotonic() < end:
            time.sleep(0.05)
        self.assertTrue(s.ready, s.problem)
        first = s.proc.pid
        s.proc.kill()
        end = time.monotonic() + 20
        while (not s.ready or s.proc.pid == first) and time.monotonic() < end:
            time.sleep(0.05)
        self.assertTrue(s.ready and s.proc.pid != first, "started again after it exited")

    def test_the_plugin_fetches_serves_and_answers_by_alias(self):
        files = Files({"llama.tgz": LLAMA_TGZ, "m.gguf": GGUF})
        out = Out()
        p = Ai(opener=files)
        p.log = lambda *a: None
        rt = Runtime(p, infile=io.StringIO(""), outfile=out)
        self.addCleanup(p.close)
        cfg = {"models_dir": self.root, "serve": [{"alias": "tiny", "url": "https://test.invalid/m.gguf", "sha256": SHA(GGUF)}]}
        rt.feed_line(json.dumps({"op": "hello", "config": cfg}))
        end = time.monotonic() + 20
        while time.monotonic() < end:
            view = p.describe(cfg)["ai"]
            if view["serving"][0]["state"] == "ready" and view["ready"]:
                break
            time.sleep(0.1)
        self.assertEqual(view["models"], ["tiny"], view)
        self.assertTrue(os.path.exists(os.path.join(self.root, "models", SHA(GGUF) + ".gguf")))
        rt.feed_line(json.dumps({"op": "command", "id": "c1", "kind": "ai", "value": {"model": "tiny", "messages": [{"role": "user", "content": "hi"}]}, "deadline_ms": 30000}))
        done = out.wait_for(lambda m: m.get("id") == "c1" and m.get("op") in ("ack", "nack"))
        self.assertEqual(done.get("op"), "ack", done)
        self.assertEqual(done["data"]["message"]["content"], "served by tiny")
        running = p.served["tiny"]
        p.close()
        self.assertIsNotNone(running.proc.poll(), "its server stops with the plugin")


if __name__ == "__main__":
    unittest.main()
