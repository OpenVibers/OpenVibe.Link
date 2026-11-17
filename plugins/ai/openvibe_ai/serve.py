"""Models this machine runs itself (Link v0.79, plan §AD.23): llama.cpp's server, one process per model, on loopback.

The owner, 2026-11-17: the models, speech and checks the production host ran "should be able to be processed via link
on the shared node or allow users to self host on their own machines". So the ai plugin can run its models instead of
expecting Ollama or a llama-server someone started by hand: the plugin's ``serve`` list names each model (from the
catalog below, or a URL with its sha256), and for each one the plugin

* downloads the GGUF into its state folder, checked against its sha256 (written whole, then moved into place), and
  llama.cpp's pinned CPU build for this machine, checked the same way;
* runs ``llama-server`` on 127.0.0.1 at a free port (``--embedding`` for an embedding model), held to its threads;
* waits for /health, restarts it with backoff when it exits, and stops it with the plugin.

The ai command then routes by model alias to that model's server. Nothing listens beyond loopback: OpenVibe reaches the
models through the Link's own connection (AI → Bot → this plugin), as before.

    serve: [{"model": "qwen2.5-1.5b-instruct"}, {"model": "bge-small-en-v1.5"},
            {"alias": "my-model", "url": "https://…/x.gguf", "sha256": "…", "threads": 4, "ctx": 8192, "parallel": 2}]
"""
from __future__ import annotations

import hashlib
import os
import platform
import shutil
import socket
import subprocess
import sys
import tarfile
import threading
import time
import urllib.request

# The models OpenVibe's production host served (plan §AD.23), each checked against Hugging Face's own sha256. Exchange
# offers these by name; any other model is a url with its sha256.
CATALOG = {
    "qwen2.5-1.5b-instruct": {
        "url": "https://huggingface.co/Qwen/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/qwen2.5-1.5b-instruct-q4_k_m.gguf",
        "sha256": "6a1a2eb6d15622bf3c96857206351ba97e1af16c30d7a74ee38970e434e9407e", "size": 1117320736,
        "license": "Apache-2.0"},
    "llama-3.2-1b-instruct": {
        "url": "https://huggingface.co/bartowski/Llama-3.2-1B-Instruct-GGUF/resolve/main/Llama-3.2-1B-Instruct-Q4_K_M.gguf",
        "sha256": "6f85a640a97cf2bf5b8e764087b1e83da0fdb51d7c9fab7d0fece9385611df83", "size": 807694464,
        "license": "Llama 3.2 Community License"},
    "bge-small-en-v1.5": {
        "url": "https://huggingface.co/CompendiumLabs/bge-small-en-v1.5-gguf/resolve/main/bge-small-en-v1.5-q8_0.gguf",
        "sha256": "ec38e8da142596baa913124ae50550de284b6916bf59577ef2f0cb9660c2f514", "size": 36806944,
        "license": "MIT", "embedding": True},
}

# llama.cpp's CPU build, the release the production host ran (b11222), checked against GitHub's own asset digest.
LLAMA = {
    "x86_64": ("https://github.com/ggml-org/llama.cpp/releases/download/b11222/llama-b11222-bin-ubuntu-x64.tar.gz",
               "cfd2323f9ffec9657ca247140129be68df62d98f30a8448b20b1ca7a1796e1cb"),
    "aarch64": ("https://github.com/ggml-org/llama.cpp/releases/download/b11222/llama-b11222-bin-ubuntu-arm64.tar.gz",
                "9099b2dd5fbd8391b4e189d78c117d263e4afe23717af92f8349fc1b67e4d40d"),
}
LLAMA_BUILD = "b11222"
ALIAS_CHARS = set("abcdefghijklmnopqrstuvwxyz0123456789.-_")


class ServeError(Exception):
    pass


def spec_of(entry):
    """One serve entry, checked → {alias, url, sha256, embedding, threads, ctx, parallel}."""
    if not isinstance(entry, dict):
        raise ServeError("each serve entry is {model} from the catalog, or {alias, url, sha256}")
    known = CATALOG.get(entry.get("model")) if entry.get("model") else None
    if entry.get("model") and not known:
        raise ServeError("%s is not in the catalog (%s); give alias, url and sha256" % (entry.get("model"), ", ".join(sorted(CATALOG))))
    alias = str(entry.get("alias") or entry.get("model") or "").lower()
    url = (known or {}).get("url") or entry.get("url")
    sha = str((known or {}).get("sha256") or entry.get("sha256") or "").lower()
    if not alias or len(alias) > 64 or not set(alias) <= ALIAS_CHARS:
        raise ServeError("alias is 1 to 64 of a-z, 0-9, dot, dash and underscore")
    if not isinstance(url, str) or not url.startswith("https://"):
        raise ServeError("%s: url is an https address" % alias)
    if len(sha) != 64 or any(c not in "0123456789abcdef" for c in sha):
        raise ServeError("%s: sha256 is 64 hex characters" % alias)

    def num(k, lo, hi, default):
        v = entry.get(k, default)
        if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
            raise ServeError("%s: %s is %d to %d" % (alias, k, lo, hi))
        return v
    return {"alias": alias, "url": url, "sha256": sha, "embedding": bool((known or {}).get("embedding") or entry.get("embedding")),
            "threads": num("threads", 1, 64, max(1, min(4, (os.cpu_count() or 2) // 2))), "ctx": num("ctx", 512, 131072, 8192),
            "parallel": num("parallel", 1, 8, 2)}


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def fetch_checked(url, sha, dest, opener=urllib.request.urlopen, log=lambda m: None):
    """dest, downloaded from url when it is not there already, checked against sha (written whole, then moved)."""
    if os.path.exists(dest) and sha256_file(dest) == sha:
        return dest
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    part = dest + ".part"
    log("downloading %s" % url.rsplit("/", 1)[-1])
    h = hashlib.sha256()
    req = urllib.request.Request(url, headers={"User-Agent": "openvibe-link"})
    with opener(req, timeout=60) as r, open(part, "wb") as out:
        for block in iter(lambda: r.read(1 << 20), b""):
            h.update(block)
            out.write(block)
    if h.hexdigest() != sha:
        os.unlink(part)
        raise ServeError("%s does not match its sha256: refused" % url.rsplit("/", 1)[-1])
    os.replace(part, dest)
    return dest


def llama_server(root, opener=urllib.request.urlopen, log=lambda m: None, machine=None):
    """The pinned llama-server for this machine (Linux x86_64 or aarch64), unpacked under root once."""
    # llama.cpp's pinned builds are Linux ones (the shared node and people's Linux machines): elsewhere, say so in words.
    if machine is None and not sys.platform.startswith("linux"):
        raise ServeError("serve runs llama.cpp's Linux builds: on %s, run your own server and set endpoint" % sys.platform)
    arch = machine or platform.machine().lower()
    arch = {"amd64": "x86_64", "arm64": "aarch64"}.get(arch, arch)
    if arch not in LLAMA:
        raise ServeError("no pinned llama.cpp build for %s here: run your own server and set endpoint" % arch)
    home = os.path.join(root, "llama.cpp", LLAMA_BUILD)
    found = _find(home, "llama-server")
    if found:
        return found
    url, sha = LLAMA[arch]
    tgz = fetch_checked(url, sha, os.path.join(root, "llama.cpp", url.rsplit("/", 1)[-1]), opener, log)
    tmp = home + ".part"
    shutil.rmtree(tmp, ignore_errors=True)
    os.makedirs(tmp)
    with tarfile.open(tgz) as t:
        for m in t.getmembers():
            if m.name.startswith("/") or ".." in m.name.split("/") or m.issym() and (m.linkname.startswith("/") or ".." in m.linkname.split("/")):
                raise ServeError("the llama.cpp archive has a path outside itself: refused")
        # The data filter (Python 3.12+) refuses links and devices outside the folder too; the check above covers older ones.
        t.extractall(tmp, **({"filter": "data"} if hasattr(tarfile, "data_filter") else {}))
    os.replace(tmp, home)
    os.unlink(tgz)
    found = _find(home, "llama-server")
    if not found:
        raise ServeError("the llama.cpp archive has no llama-server")
    os.chmod(found, 0o755)
    return found


def _find(home, name):
    for d, _, files in os.walk(home) if os.path.isdir(home) else []:
        if name in files:
            return os.path.join(d, name)
    return None


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Served:
    """One model's llama-server: started, watched (restarted with backoff when it exits) and stopped."""

    def __init__(self, spec, binary, model_path, log=lambda m: None, popen=subprocess.Popen, ready=None):
        self.spec, self.binary, self.model_path, self.log, self.popen = spec, binary, model_path, log, popen
        self.port = None
        self.proc = None
        self.ready = False
        self.problem = None
        self.stopping = threading.Event()
        self._ready_probe = ready
        self.thread = threading.Thread(target=self._run, name="ai-serve-" + spec["alias"], daemon=True)

    @property
    def endpoint(self):
        return "http://127.0.0.1:%d" % self.port if self.port else None

    def start(self):
        self.thread.start()
        return self

    def argv(self):
        s = self.spec
        a = [self.binary, "--model", self.model_path, "--alias", s["alias"], "--host", "127.0.0.1", "--port", str(self.port),
             "--threads", str(s["threads"]), "--ctx-size", str(s["ctx"]), "--parallel", str(s["parallel"])]
        return a + (["--embedding"] if s["embedding"] else [])

    def _healthy(self):
        if self._ready_probe:
            return self._ready_probe(self)
        try:
            with urllib.request.urlopen(self.endpoint + "/health", timeout=2) as r:
                return r.status == 200
        except Exception:
            return False

    def _run(self):
        backoff = 1.0
        while not self.stopping.is_set():
            self.port = free_port()
            started = time.monotonic()
            try:
                self.proc = self.popen(self.argv(), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                       start_new_session=True)
            except OSError as e:
                self.problem = "llama-server did not start: %s" % e
                self.stopping.wait(backoff)
                backoff = min(60.0, backoff * 2)
                continue
            while not self.stopping.is_set() and self.proc.poll() is None:
                if not self.ready and self._healthy():
                    self.ready, self.problem = True, None
                    self.log("serving %s on 127.0.0.1:%d" % (self.spec["alias"], self.port))
                self.stopping.wait(0.5)
            self.ready = False
            if self.stopping.is_set():
                break
            self.problem = "llama-server for %s exited (%s); starting it again" % (self.spec["alias"], self.proc.returncode)
            self.log(self.problem)
            if time.monotonic() - started > 300:
                backoff = 1.0
            self.stopping.wait(backoff)
            backoff = min(60.0, backoff * 2)
        self._kill()

    def _kill(self):
        p = self.proc
        if p is not None and p.poll() is None:
            p.terminate()
            try:
                p.wait(10)
            except subprocess.TimeoutExpired:
                p.kill()

    def stop(self):
        self.stopping.set()
        self._kill()
