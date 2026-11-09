"""OpenVibe Link ai plugin: the person's own models for their own OpenVibe work (contracts 0.219.0, kind ``ai``).

Many people already run models at home (Ollama, llama.cpp's server, LM Studio). This plugin lets OpenVibe use them for
their owner, for Vibe's answers or an agent's thinking, without the prompt leaving the machine's network and with no
open port: OpenVibe.AI asks OpenVibe.Bot, Bot sends the Node one ``ai`` command over the connection the Node already
keeps, and this plugin relays it to the local model server and answers with the reply.

It reaches one place only, the server in its config, and it reads and changes nothing else on the machine. A request
runs at once up to the owner's ``offer.concurrency`` (1 by default: a model is heavy); another is refused as busy,
never queued. The owner turns it on twice: by adding the plugin here, and by "Let my AI use this computer's models" on
openvibe.link/machines.

Offer to the network (the /sell settings, plugin 0.3.0): ``offer.enabled`` lets other people's requests that opt in to
community capacity run here; the plugin advertises the offer in ``describe`` (status.capabilities.ai.ai.offer), Bot
publishes it as a Network user-owned offer, and OpenVibe.AI places verified, opted-in requests on it. The plugin
enforces enabled, the local-time ``hours``, the ``models`` offered and ``concurrency``; the router enforces
``audience`` (everyone, followers, or one group) because only it can resolve a requester's followers. The prompt is
visible to this machine, so only requests that opt in ever come here, and every answer says so.

Config (the plugin's block in config.json):

* ``endpoint``: the model server's OpenAI-compatible base, without ``/v1``. By default the first one that answers of
  Ollama (``http://127.0.0.1:11434``), LM Studio (``http://127.0.0.1:1234``) and llama.cpp's server
  (``http://127.0.0.1:8080``).
* ``models``: the models offered (a list); by default every model the server lists.
* ``max_tokens``: the most a reply may be (default 2048, at most 32768).
* ``timeout_s``: how long one reply may take (default 180, at most 900).
* ``offer``: the network offer, off unless ``enabled`` is true: ``models`` (default: the served ones), ``hours``
  (``{start, end, tz}``, HH:MM local time, an overnight window allowed, start == end means all day), ``concurrency``
  (1-8), ``price`` (``{in_per_million, out_per_million}`` credits, 0 to volunteer) and ``audience``
  (``everyone``, ``followers``, or ``group`` with a ``group`` id), ``idle_only`` (default true: pause while the GPU,
  a game/OBS/Steam process or the person at the keyboard is using the machine) and ``quiet_minutes`` (default 5) before
  it resumes. The offer also carries the privacy sentence shown when it is turned on: a community machine sees the
  prompt, only opted-in requests come here, and every answer is labelled.

Command ``ai`` ``{"model", "messages": [{"role", "content"}], "max_tokens"?, "temperature"?, "tools"?, "tool_choice"?}``
→ ``{model, message: {role: "assistant", content, tool_calls?}, usage?, finish_reason?}``.

Tools (contracts 0.277.0, plugin 0.2.0): the functions the model may ask for, passed to the model server as written.
The plugin never runs one: it hands back the calls the model asked for, and the caller (an agent on OpenVibe) runs
them and sends the next command with the assistant's message (its ``tool_calls``) and one ``tool`` message per call.
``describe`` says ``tools: true`` so a caller knows this machine can.

Machine pools (plugin 0.4.0, Linux; the pooled machines design, 2026-11-06): with ``pool_directory`` set, this machine
serves one model together with other machines of the same owner on the same local network, through llama.cpp RPC
(``rpc-server`` on the members, ``llama-server --rpc`` on the head). The pool is proposed on openvibe.link/machines
and approved on each machine with ``python -m openvibe_ai.pool_cli approve``; until then the plugin announces the
machine every minute (``pool_credential_file``, ``pool_address``, ``pool_port``). ``pool_engine`` names the engine:
``{rpc_binary, llama_binary, model_path, model: {model, parameters_b, memory_mb}, context?}``. The RPC engine is
reachable only through the pool's private, mutually authenticated link and only from the head; idle-only and the kill
switch on any machine pause the whole pool; leaving the pool drops the link and the key (docs/ai.md, "Machine pools").
"""

import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

from openvibe_plugin import Deferred, Fault, Plugin

VERSION = "0.4.0"
DEFAULT_ENDPOINTS = ("http://127.0.0.1:11434", "http://127.0.0.1:1234", "http://127.0.0.1:8080")
MAX_REPLY_CHARS = 200 * 1024     # well inside the core's answer limit (512 KB)
MAX_MESSAGES = 200
MAX_CONTENT = 64 * 1024
ROLES = ("system", "user", "assistant", "tool")
MAX_TOOLS = 64
MAX_CALLS = 32
MAX_TOOLS_BYTES = 256 * 1024
NAME = re.compile(r"^[A-Za-z0-9_.-]{1,64}$")
HOUR = re.compile(r"^([01][0-9]|2[0-3]):([0-5][0-9])$")
AUDIENCES = ("everyone", "followers", "group")


def _call(c):
    """One tool call as the contract has it, or None."""
    if not isinstance(c, dict) or c.get("type", "function") != "function":
        return None
    fn = c.get("function") if isinstance(c.get("function"), dict) else {}
    name, args, cid = fn.get("name"), fn.get("arguments"), c.get("id")
    if not isinstance(name, str) or not NAME.match(name):
        return None
    if isinstance(args, (dict, list)):
        args = json.dumps(args)   # Ollama may give the arguments as an object
    if args is None:
        args = "{}"
    if not isinstance(args, str) or len(args) > MAX_CONTENT:
        return None
    if cid is not None and (not isinstance(cid, str) or not cid or len(cid) > 200):
        return None
    return {"id": cid, "type": "function", "function": {"name": name, "arguments": args}}


def _message(m):
    """A message as the model server gets it, or None when it breaks the contract."""
    if not isinstance(m, dict) or m.get("role") not in ROLES or not isinstance(m.get("content"), str) or len(m["content"]) > MAX_CONTENT:
        return None
    out = {"role": m["role"], "content": m["content"]}
    calls, call_id = m.get("tool_calls"), m.get("tool_call_id")
    if calls is not None:
        if m["role"] != "assistant" or not isinstance(calls, list) or not 0 < len(calls) <= MAX_CALLS:
            return None
        made = [_call(c) for c in calls]
        if any(c is None or c["id"] is None for c in made):
            return None
        out["tool_calls"] = made
    if m["role"] == "tool":
        if not isinstance(call_id, str) or not call_id or len(call_id) > 200:
            return None
        out["tool_call_id"] = call_id
    elif call_id is not None:
        return None
    return out


def _tools(tools):
    """The tools as the model server gets them, or None when they break the contract."""
    if not isinstance(tools, list) or not 0 < len(tools) <= MAX_TOOLS:
        return None
    out = []
    for t in tools:
        fn = t.get("function") if isinstance(t, dict) else None
        if not isinstance(fn, dict) or t.get("type") != "function" or not isinstance(fn.get("name"), str) or not NAME.match(fn["name"]):
            return None
        one = {"name": fn["name"]}
        if fn.get("description") is not None:
            if not isinstance(fn["description"], str) or len(fn["description"]) > 4096:
                return None
            one["description"] = fn["description"]
        if fn.get("parameters") is not None:
            if not isinstance(fn["parameters"], dict):
                return None
            one["parameters"] = fn["parameters"]
        out.append({"type": "function", "function": one})
    if len(json.dumps(out)) > MAX_TOOLS_BYTES:
        return None
    return out


def _int(v, name, lo, hi, default):
    if v is None:
        return default
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        raise Fault("bad_value", "%s must be a number" % name)
    return int(max(lo, min(hi, v)))


def _amount(v, name):
    """A price per million tokens: a number from 0 to 1,000,000 credits (0 volunteers the model)."""
    if isinstance(v, bool) or not isinstance(v, (int, float)) or not 0 <= v <= 1000000:
        raise Fault("bad_value", "%s must be a number from 0 to 1000000" % name)
    return float(v)


def _offer_config(cfg, served):
    """The owner's offer to the network (the /sell settings), normalized; enabled is false by default.

    ``offer.models`` defaults to the models this machine serves; ``hours`` is one local-time window (start == end
    means all day and an overnight window is allowed); ``concurrency`` caps how many community replies run at once;
    ``price`` is credits per million input and output tokens (0 is volunteering); ``audience`` is everyone, followers
    or one group. The Node enforces enabled, hours, models and concurrency; the router enforces the audience, since
    only it can resolve a requester's followers and groups.
    """
    raw = cfg.get("offer")
    if raw is None:
        raw = {}
    if not isinstance(raw, dict):
        raise Fault("bad_value", "offer is the network-offer settings object")
    enabled = raw.get("enabled", False)
    if not isinstance(enabled, bool):
        raise Fault("bad_value", "offer.enabled is true or false")
    models = raw.get("models", served)
    if not isinstance(models, list) or not all(isinstance(m, str) and 0 < len(m) <= 200 for m in models):
        raise Fault("bad_value", "offer.models is a list of model names this machine serves")
    models = [m for m in models if m in served] if served else []
    hours = raw.get("hours") or {}
    if not isinstance(hours, dict):
        raise Fault("bad_value", "offer.hours is {start, end, tz}")
    start = hours.get("start", "00:00")
    end = hours.get("end", "00:00")
    if not isinstance(start, str) or not HOUR.match(start) or not isinstance(end, str) or not HOUR.match(end):
        raise Fault("bad_value", "offer.hours.start and offer.hours.end are HH:MM (24-hour)")
    tz = hours.get("tz", "UTC")
    if not isinstance(tz, str) or not tz or len(tz) > 64:
        raise Fault("bad_value", "offer.hours.tz is an IANA time zone such as UTC or Europe/Lisbon")
    if tz != "UTC":   # Windows Python has no system zone database unless the tzdata package is installed; UTC never needs it.
        try:
            from zoneinfo import ZoneInfo
            ZoneInfo(tz)
        except Exception:
            raise Fault("bad_value", "offer.hours.tz is an IANA time zone such as UTC or Europe/Lisbon")
    concurrency = _int(raw.get("concurrency"), "offer.concurrency", 1, 8, 1)
    price = raw.get("price") or {}
    if not isinstance(price, dict):
        raise Fault("bad_value", "offer.price is {in_per_million, out_per_million}")
    in_per_million = _amount(price.get("in_per_million", 0), "offer.price.in_per_million")
    out_per_million = _amount(price.get("out_per_million", 0), "offer.price.out_per_million")
    audience = raw.get("audience", "everyone")
    if audience not in AUDIENCES:
        raise Fault("bad_value", "offer.audience is everyone, followers or group")
    idle_only = raw.get("idle_only", True)
    if not isinstance(idle_only, bool):
        raise Fault("bad_value", "offer.idle_only is true or false")
    quiet_minutes = _int(raw.get("quiet_minutes"), "offer.quiet_minutes", 0, 120, 5)
    busy_gpu_percent = _int(raw.get("busy_gpu_percent"), "offer.busy_gpu_percent", 1, 100, 20)
    busy_processes = raw.get("busy_processes", ["obs", "obs64", "obs-studio", "steam", "steamwebhelper", "lutris", "heroic", "gameoverlayui"])
    if not isinstance(busy_processes, list) or not all(isinstance(x, str) and 0 < len(x) <= 80 for x in busy_processes):
        raise Fault("bad_value", "offer.busy_processes is a list of process names")
    group = raw.get("group")
    if audience == "group" and (not isinstance(group, str) or not group.strip() or len(group) > 80):
        raise Fault("bad_value", "offer.group is the group id whose members may use this offer")
    if audience != "group":
        group = None
    return {
        "enabled": enabled,
        "models": models,
        "hours": {"start": start, "end": end, "tz": tz},
        "concurrency": concurrency,
        "price": {"in_per_million": in_per_million, "out_per_million": out_per_million},
        "audience": audience,
        "group": group,
        "idle_only": idle_only,
        "quiet_minutes": quiet_minutes,
        "busy_gpu_percent": busy_gpu_percent,
        "busy_processes": [x.lower() for x in busy_processes],
    }


def _pool_engine(raw):
    """The pool's engine (pool_engine in config): llama.cpp's rpc-server and llama-server built with GGML_RPC, the
    model file, its name, size and the memory it needs (weights and context), and the context length."""
    if not isinstance(raw, dict):
        raise Fault("bad_value", "pool_engine is {rpc_binary, llama_binary, model_path, model: {model, parameters_b, memory_mb}, context?}")
    for key in ("rpc_binary", "llama_binary", "model_path"):
        if not isinstance(raw.get(key), str) or not raw[key] or len(raw[key]) > 1024 or not os.path.isabs(raw[key]):
            raise Fault("bad_value", "pool_engine.%s is an absolute path on this machine" % key)
    model = raw.get("model")
    if not isinstance(model, dict) or not isinstance(model.get("model"), str) or not 0 < len(model["model"]) <= 200:
        raise Fault("bad_value", "pool_engine.model.model is the model's name")
    size = model.get("parameters_b")
    if isinstance(size, bool) or not isinstance(size, (int, float)) or not 0 < size <= 4000:
        raise Fault("bad_value", "pool_engine.model.parameters_b is the model's size in billions of parameters")
    memory = _int(model.get("memory_mb"), "pool_engine.model.memory_mb", 1, 16 * 1024 * 1024, None)
    if memory is None:
        raise Fault("bad_value", "pool_engine.model.memory_mb is the memory the model needs, weights and context")
    return {"rpc_binary": raw["rpc_binary"], "llama_binary": raw["llama_binary"], "model_path": raw["model_path"],
            "context": _int(raw.get("context"), "pool_engine.context", 256, 131072, 2048),
            "model": {"model": model["model"], "parameters_b": float(size), "memory_mb": memory}}


def _gpu_compute_pids():
    """The processes on the NVIDIA GPUs (nvidia-smi), or None when they cannot be listed."""
    if not shutil.which("nvidia-smi"):
        return None
    out = _run(["nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader,nounits"])
    try:
        return {int(x) for x in out.split()}
    except ValueError:
        return None


def _minutes(hhmm):
    return int(hhmm[:2]) * 60 + int(hhmm[3:])


def _run(cmd, timeout=2.0):
    """A short local probe's output, or '' when the tool is absent, times out or fails."""
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout).stdout
    except (OSError, subprocess.SubprocessError, ValueError):
        return ""


def _gpu_busy(threshold):
    """NVIDIA (nvidia-smi) or AMD (rocm-smi) utilization over the threshold; unknown tools mean 'not busy'."""
    if shutil.which("nvidia-smi"):
        out = _run(["nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits"])
        for part in re.split(r"[,\s]+", out):
            if part.isdigit() and int(part) >= threshold:
                return True
    if shutil.which("rocm-smi"):
        out = _run(["rocm-smi", "--showuse"])
        for pct in re.findall(r"(?:GPU use|GPU utilization)[^0-9]*(\d+)", out, re.I):
            if int(pct) >= threshold:
                return True
    return False


def _process_busy(names):
    """A game, OBS or another heavy process the owner listed is running."""
    if not names:
        return False
    if sys.platform == "win32":
        out = _run(["tasklist", "/FO", "CSV", "/NH"], 3.0)
    else:
        out = _run(["ps", "-A", "-o", "comm="], 3.0)
    low = out.lower()
    return any(name in low for name in names)


def _input_idle_ms():
    """How long since the person last touched the keyboard or mouse, or None when it cannot be read."""
    try:
        if sys.platform == "win32":
            import ctypes
            class LASTINPUTINFO(ctypes.Structure):
                _fields_ = [("cbSize", ctypes.c_uint), ("dwTime", ctypes.c_uint)]
            info = LASTINPUTINFO()
            info.cbSize = ctypes.sizeof(info)
            if ctypes.windll.user32.GetLastInputInfo(ctypes.byref(info)):
                return max(0, int(ctypes.windll.kernel32.GetTickCount()) - int(info.dwTime))
        elif sys.platform == "darwin":
            out = _run(["ioreg", "-c", "IOHIDSystem"], 2.0)
            m = re.search(r'"HIDIdleTime"\s*=\s*(\d+)', out)
            if m:
                return int(m.group(1)) // 1000000
        elif shutil.which("xprintidle"):
            out = _run(["xprintidle"], 1.0).strip()
            if out.isdigit():
                return int(out)
    except Exception:
        return None
    return None


class Ai(Plugin):
    driver = "ai"
    version = VERSION
    motion_kinds = frozenset()   # it moves nothing

    def __init__(self, opener=None, clock=time.monotonic, now=None, busy_probe=None):
        self.opener = opener or urllib.request.urlopen
        self.default_opener = self.opener
        self.clock = clock
        self.now = now or (lambda: datetime.datetime.now(datetime.timezone.utc))
        # Injected in tests: returns (busy, reason). In production this reads GPU use, heavy processes and input idle.
        self.busy_probe = busy_probe
        self.quiet_since = None
        self.busy_reason = None
        self._busy_cache = (0.0, False, None)
        self.endpoint = None
        self.models = []
        self.max_tokens = 2048
        self.timeout_s = 180
        # The community offer: how many replies may run at once and how many are running now. The owner's own
        # requests and the network's share the same slots; nothing is queued.
        self.offer = _offer_config({}, [])
        self.active = 0
        self.lock = threading.Lock()
        self.cfg = {}
        self.reachable = False
        # How fast each model answers here, in tokens a second of its reply (an average that favours the latest
        # answers), and the model that answered last: the telemetry Bot shows beside the machine's models.
        self.speed = {}
        self.last_model = None
        # What this machine served to the network, as sensors and for the /sell view: tokens counted by the local
        # server's usage, never the prompt or the answer.
        self.community = {"requests": 0, "tokens_in": 0, "tokens_out": 0}
        # A machine pool (pool_directory in config): its runtime once every machine approved, the thread that waits
        # for that, the kill switch latch, and the last problem, shown in describe.
        self.pool_runtime = None
        self.pool_thread = None
        self.pool_stopped = threading.Event()
        self.pool_killed = threading.Event()
        self.pool_allow_start = False
        self.pool_cfg = None
        self.pool_error = None

    # ---- the model server -----------------------------------------------------------------------------------------
    def _get(self, url, timeout):
        req = urllib.request.Request(url, headers={"Accept": "application/json"})
        with self.opener(req, timeout=timeout) as r:
            return json.loads(r.read(1024 * 1024).decode("utf-8"))

    def _list_models(self, endpoint, timeout=3.0):
        """The models a server lists (OpenAI-compatible /v1/models), or None when it does not answer."""
        try:
            doc = self._get(endpoint.rstrip("/") + "/v1/models", timeout)
        except (OSError, ValueError, urllib.error.URLError):
            return None
        data = doc.get("data") if isinstance(doc, dict) else None
        if not isinstance(data, list):
            return None
        return [str(m.get("id")) for m in data if isinstance(m, dict) and m.get("id")][:100]

    def _configure(self, config):
        cfg = config or {}
        self.cfg = cfg
        self.max_tokens = _int(cfg.get("max_tokens"), "max_tokens", 1, 32768, 2048)
        self.timeout_s = _int(cfg.get("timeout_s"), "timeout_s", 5, 900, 180)
        if cfg.get("pool_directory"):
            self._configure_pool(cfg)
            return
        endpoint = cfg.get("endpoint")
        if endpoint is not None and (not isinstance(endpoint, str) or not endpoint.startswith(("http://", "https://"))):
            raise Fault("bad_value", "endpoint is the model server's http(s) base, for example http://127.0.0.1:11434")
        listed = None
        self.reachable = False
        for candidate in ([endpoint] if endpoint else DEFAULT_ENDPOINTS):
            listed = self._list_models(candidate)
            if listed is not None:
                self.endpoint = candidate.rstrip("/")
                self.reachable = True
                break
        else:
            self.endpoint = endpoint.rstrip("/") if endpoint else None
        wanted = cfg.get("models")
        if wanted is not None and (not isinstance(wanted, list) or not all(isinstance(m, str) and m for m in wanted)):
            raise Fault("bad_value", "models is a list of model names")
        self.models = [m for m in (listed or []) if wanted is None or m in wanted] if listed is not None else list(wanted or [])
        before = self.offer.get("idle_only")
        self.offer = _offer_config(cfg, self.models)
        if before is None or before != self.offer["idle_only"]:
            self.quiet_since = None
            self._busy_cache = (0.0, False, None)

    def _raw_busy(self):
        """(busy, reason) from the injected probe or the machine's own signals, cached for five seconds."""
        if not self.offer["idle_only"]:
            return False, None
        now = self.clock()
        if self._busy_cache[0] and now - self._busy_cache[0] < 5.0:
            return self._busy_cache[1], self._busy_cache[2]
        if self.busy_probe is not None:
            busy, reason = self.busy_probe()
        else:
            if _gpu_busy(self.offer["busy_gpu_percent"]) and not self._gpu_is_ours():
                busy, reason = True, "gpu"
            elif _process_busy(self.offer["busy_processes"]):
                busy, reason = True, "process"
            else:
                idle = _input_idle_ms()
                if idle is not None and idle < (self.offer["quiet_minutes"] or 0) * 60000:
                    busy, reason = True, "keyboard"
                else:
                    busy, reason = False, None
        self._busy_cache = (now, bool(busy), reason)
        return bool(busy), reason

    def _paused(self):
        """Idle-only: busy now, or not yet quiet for the owner's minutes. Never pauses the owner's own requests."""
        if not self.offer["idle_only"]:
            return False
        busy, reason = self._raw_busy()
        if busy:
            self.quiet_since = None
            self.busy_reason = reason
            return True
        if self.quiet_since is None:
            self.quiet_since = self.clock()
        if self.clock() - self.quiet_since < (self.offer["quiet_minutes"] or 0) * 60:
            self.busy_reason = "quiet"
            return True
        self.busy_reason = None
        return False

    def _within_hours(self):
        """Is the offer open now, in its own time zone? start == end is all day; an overnight window wraps."""
        h = self.offer["hours"]
        try:
            if h["tz"] == "UTC":
                at = self.now().astimezone(datetime.timezone.utc)
            else:
                from zoneinfo import ZoneInfo
                at = self.now().astimezone(ZoneInfo(h["tz"]))
        except Exception:
            return False
        start, end = _minutes(h["start"]), _minutes(h["end"])
        minute = at.hour * 60 + at.minute
        if start == end:
            return True
        if start < end:
            return start <= minute < end
        return minute >= start or minute < end

    def _offer_view(self):
        """The offer as Bot, Network and AI see it (status.capabilities.ai.offer)."""
        open_now = self._within_hours()
        paused = self._paused() if self.offer["enabled"] else False
        view = {
            "enabled": self.offer["enabled"],
            "models": list(self.offer["models"]),
            "hours": dict(self.offer["hours"]),
            "concurrency": self.offer["concurrency"],
            "price": dict(self.offer["price"]),
            "audience": self.offer["audience"],
            "group": self.offer["group"],
            "idle_only": self.offer["idle_only"],
            "quiet_minutes": self.offer["quiet_minutes"],
            "open_now": open_now,
            "paused": paused,
            "busy_reason": self.busy_reason,
            "privacy": "A community machine sees the prompt. Only requests that allow community capacity are sent here, and every answer is labelled Community.",
            "ready": bool(self.offer["enabled"] and self.reachable and self.offer["models"] and open_now and not paused),
        }
        if self.cfg.get("pool_directory"):
            # A pool's offer is the head's, ready only while the whole pool runs (Network adds the pool from its own
            # record; this is what the machine itself sees). A member never offers on its own while it is in a pool.
            runtime = self.pool_runtime
            if runtime:
                view["pool"] = {"machines": sorted(m["id"] for m in runtime.doc["machines"]), "head": runtime.doc["head"]}
            view["ready"] = bool(view["ready"] and self._pool_serving())
        return view

    # ---- a machine pool (pool.py, pool_runtime.py, pool_registry.py) ---------------------------------------------
    def _configure_pool(self, cfg):
        """Pool mode (``pool_directory`` set): this machine serves one model together with the other machines of its
        pool. The model server is the pool's own ``llama-server`` on the head, reached through a private Unix socket;
        the offer's settings apply to the whole pool. Nothing starts before setup, and nothing runs until every machine
        has approved the pool on itself."""
        from pathlib import Path
        if sys.platform != "linux":
            raise Fault("unsupported", "machine pools run on Linux only: the RPC engine must be isolated in its own network namespace")
        engine = _pool_engine(cfg.get("pool_engine"))
        directory = Path(cfg["pool_directory"])
        if not directory.is_absolute():
            raise Fault("bad_value", "pool_directory is an absolute path on this machine")
        address = cfg.get("pool_address")
        if address is not None and not isinstance(address, str):
            raise Fault("bad_value", "pool_address is this machine's private address and prefix, such as 192.168.1.2/24")
        self.pool_cfg = {"directory": directory, "engine": engine, "address": address,
                         "port": _int(cfg.get("pool_port"), "pool_port", 1024, 65535, 50053)}
        self.models = [engine["model"]["model"]]
        self.offer = _offer_config(cfg, self.models)
        self.endpoint = "http://localhost"   # the head's llama-server, through the pool's private Unix socket
        self.reachable = self._pool_serving()
        if not self.pool_allow_start or self.pool_thread or self.pool_stopped.is_set():
            return
        self.pool_thread = threading.Thread(target=self._pool_loop, name="ai-pool", daemon=True)
        self.pool_thread.start()

    def _pool_serving(self):
        """This machine is the head of a running, unpaused pool."""
        runtime = self.pool_runtime
        return bool(runtime and runtime.machine == runtime.doc["head"] and not runtime.paused and not runtime.stopped.is_set())

    def _pool_ready(self):
        """What this machine renews on Network: its part of the pool is running and not paused."""
        runtime = self.pool_runtime
        return bool(runtime and not runtime.paused and runtime.status()["ready"])

    def _pool_busy(self):
        """Idle-only and the kill switch for a pool member: the kill switch latched, the offer off or outside its hours,
        or the machine in use. Any member busy pauses the whole pool (the head asks every member every half second)."""
        if self.pool_killed.is_set():
            return True
        if not self.offer["enabled"] or not self._within_hours():
            return True
        return self._paused()

    def _gpu_is_ours(self):
        """While this machine runs its pool's engine, the GPU's use is the pool's own, unless another program is on it
        (NVIDIA lists the processes; elsewhere the listed busy processes and the keyboard still count)."""
        runtime = self.pool_runtime
        ours = runtime.engine_pids() if runtime else set()
        if not ours:
            return False
        others = _gpu_compute_pids()
        return others is None or not (others - ours)

    def _pool_loop(self):
        """Announce this machine until a pool is approved here, then run it. A pool this machine left (revoked,
        removed, signed out, another owner) ends here: its key is gone and the machine announces itself again."""
        from .pool_registry import Membership, Registry, local_address
        from .pool_runtime import PoolRuntime
        cfg = self.pool_cfg
        directory, approval_file = cfg["directory"], cfg["directory"] / "approval.json"
        announced = 0.0
        while not self.pool_stopped.is_set():
            try:
                if not approval_file.exists():
                    # Not approved here: stay on the owner's "Pool these machines" list (Network forgets after 5 min).
                    if self.cfg.get("pool_credential_file") and time.monotonic() - announced > 60:
                        from .pool_cli import memory
                        registry = Registry(self.cfg["pool_credential_file"])
                        registry.announce(local_address(cfg["address"]), cfg["port"], memory())
                        announced = time.monotonic()
                    self.pool_stopped.wait(5)
                    continue
                approval = json.loads(approval_file.read_text())
                key = directory / "identity" / "key.pem"
                registry = Registry(approval["credential_file"])
                membership = Membership(registry, approval["pool_id"], key, ready=self._pool_ready)
                doc = membership()
                if doc is None or doc["owner"] != approval["owner"] or doc["head"] != approval["head"] or \
                        sorted(m["id"] for m in doc["machines"]) != approval["machines"]:
                    # Left, revoked or changed: drop the key and the approval at once; a new pool needs a new approval.
                    shutil.rmtree(str(directory / "identity"), ignore_errors=True)
                    approval_file.unlink(missing_ok=True)
                    self.pool_error = "this machine is no longer in pool %s" % approval["pool_id"]
                    continue
                machine = registry.credentials()["device_id"]
                # Raises ValueError until every machine has approved on itself.
                runtime = PoolRuntime(doc, machine, directory, cfg["engine"], self._pool_busy, membership)
                if self.pool_stopped.is_set():
                    return
                self.pool_runtime = runtime
                self.opener = runtime.opener
                runtime.start()
                self.pool_error = None
                while not runtime.stopped.wait(1):
                    self.pool_error = runtime.last_error
                if self.pool_stopped.is_set():
                    return
                # The runtime left the pool (its key is deleted): forget it and announce again.
                self.pool_runtime = None
                self.opener = self.default_opener
                approval_file.unlink(missing_ok=True)
            except (OSError, KeyError, ValueError, TypeError) as error:
                self.pool_error = "%s: %s" % (type(error).__name__, str(error)[:200])
                self.pool_stopped.wait(2)

    def on_stop(self, info):
        """The kill switch on this machine (an emergency stop) stops the whole pool now and holds it until the latch is
        cleared; the Node stopping closes it and keeps the key. A turn's halt, a lost link or a command's timeout is not
        about the pool. Without a pool, every stop is as before (nothing moves here)."""
        if not self.cfg.get("pool_directory"):
            return Plugin.on_stop(self, info)
        if info.emergency:
            self.pool_killed.set()
            runtime = self.pool_runtime
            if runtime:
                runtime.halt()
        elif info.cause == "shutdown":
            self.close()

    def on_resume(self, info):
        """The stop latch was cleared by its owner: the pool may run again, after every member's own checks."""
        self.pool_killed.clear()

    def close(self):
        """The plugin is ending: stop the pool's engines and its private link now; the key stays for the next start."""
        self.pool_stopped.set()
        runtime = self.pool_runtime
        if runtime:
            runtime.close()

    def describe(self, config):
        try:
            self._configure(config)
        except Fault:
            return {}
        out = {"ai": {"models": self.models, "ready": self.reachable and bool(self.models),
                      "max_tokens": self.max_tokens, "tools": True,
                      "speed": {m: round(r, 1) for m, r in self.speed.items()},
                      "offer": self._offer_view()}}
        if self.cfg.get("pool_directory"):
            runtime = self.pool_runtime
            out["ai"]["pool"] = {"id": runtime.doc["id"] if runtime else None,
                                 "role": (("head" if runtime.machine == runtime.doc["head"] else "member") if runtime else None),
                                 "state": ("stopped" if self.pool_killed.is_set() else "waiting" if not runtime
                                           else "serving" if not runtime.paused else "paused"),
                                 "problem": (self.pool_error or "")[:200] or None}
        return out

    def setup(self, config):
        self.pool_allow_start = True
        self._configure(config)
        if self.endpoint is None:
            self.log("no model server answers yet (Ollama, LM Studio or llama.cpp's server, or the endpoint in config)")

    # ---- one reply --------------------------------------------------------------------------------------------------
    def handle(self, cmd):
        if self.cfg.get("pool_directory") and not self._pool_serving():
            raise Fault("not_ready", "this machine's pool is paused, waiting for every machine to approve, or served by its head")
        if cmd.kind != "ai":
            raise Fault("unsupported", "the ai plugin takes ai commands only")
        v = cmd.value if isinstance(cmd.value, dict) else {}
        model = v.get("model")
        messages = v.get("messages")
        # The router marks a community request with the community/ model prefix (the ai command's value is closed to
        # extra fields); strip it before the local server sees the name. v.community is accepted for older callers.
        community = v.get("community") is True or (isinstance(model, str) and model.startswith("community/"))
        if isinstance(model, str) and model.startswith("community/"):
            model = model[len("community/"):]
        if not isinstance(model, str) or not model or len(model) > 200:
            raise Fault("bad_value", "model is the name of a model this machine offers")
        if community:
            if not self.offer["enabled"]:
                raise Fault("not_allowed", "this machine is not offering its models to the network")
            if not self._within_hours():
                raise Fault("not_allowed", "the owner\'s offer is outside its hours")
            if self._paused():
                raise Fault("not_allowed", "the machine is in use or has not been quiet for %d minutes" % (self.offer["quiet_minutes"] or 0))
            if model not in self.offer["models"]:
                raise Fault("bad_value", "this machine does not offer %s to the network (it offers: %s)" % (model, ", ".join(self.offer["models"][:20]) or "none"))
        sent = [_message(m) for m in messages] if isinstance(messages, list) and 0 < len(messages) <= MAX_MESSAGES else None
        if not sent or any(m is None for m in sent):
            raise Fault("bad_value", "messages are 1 to %d of {role: system|user|assistant|tool, content: text}; tool_calls on an assistant message, tool_call_id on a tool one" % MAX_MESSAGES)
        tools = None
        if v.get("tools") is not None:
            tools = _tools(v.get("tools"))
            if tools is None:
                raise Fault("bad_value", "tools are 1 to %d of {type: function, function: {name, description?, parameters?}}" % MAX_TOOLS)
        tool_choice = v.get("tool_choice")
        if tool_choice is not None and tool_choice not in ("auto", "none"):
            raise Fault("bad_value", "tool_choice is auto or none")
        if not self.reachable:
            self._configure_again()
        if not self.reachable:
            raise Fault("not_ready", "no model server answers on this machine (start Ollama, LM Studio or llama.cpp's server)")
        if model not in self.models:
            self._configure_again()
            if model not in self.models:
                raise Fault("bad_value", "this machine does not offer %s (it offers: %s)" % (model, ", ".join(self.models[:20]) or "none"))
        max_tokens = _int(v.get("max_tokens"), "max_tokens", 1, self.max_tokens, self.max_tokens)
        temperature = v.get("temperature")
        if temperature is not None and (isinstance(temperature, bool) or not isinstance(temperature, (int, float)) or not 0 <= temperature <= 2):
            raise Fault("bad_value", "temperature is 0 to 2")
        capacity = self.offer["concurrency"] if self.offer["enabled"] else 1
        with self.lock:
            if self.active >= capacity:
                raise Fault("not_ready", "the model is answering another request on this machine: try again in a moment")
            self.active += 1
        body = {"model": model, "messages": sent, "max_tokens": max_tokens, "stream": False}
        if temperature is not None:
            body["temperature"] = temperature
        if tools:
            body["tools"] = tools
            body["tool_choice"] = tool_choice or "auto"
        try:
            threading.Thread(target=self._reply, args=(cmd.id, body, community), name="ai-reply", daemon=True).start()
        except Exception:
            self._release()
            raise
        raise Deferred()

    def _measure(self, model, tokens, ms):
        """Keeps how fast this model answered: the reply's tokens over the whole answer's time (reading the prompt
        included, so it is what a person waits), averaged with the earlier answers, the latest counting most."""
        if not isinstance(tokens, int) or tokens < 1 or not ms or ms < 1:
            return
        rate = tokens / (ms / 1000.0)
        before = self.speed.get(model)
        self.speed[model] = rate if before is None else before * 0.7 + rate * 0.3
        self.last_model = model

    def _release(self):
        with self.lock:
            self.active = max(0, self.active - 1)

    def poll(self):
        """Sensors: whether a model is answering now (ai_busy), the last model's speed here (ai_model,
        ai_tokens_per_s) once one has answered, and what this machine served to the network (ai_community_*)."""
        with self.lock:
            busy = 1 if self.active else 0
            served = dict(self.community)
        sensors = {"ai_busy": busy}
        if self.last_model is not None and self.last_model in self.speed:
            sensors["ai_model"] = self.last_model[:120]
            sensors["ai_tokens_per_s"] = round(self.speed[self.last_model], 1)
        if served["requests"]:
            sensors["ai_community_requests"] = served["requests"]
            sensors["ai_community_tokens_in"] = served["tokens_in"]
            sensors["ai_community_tokens_out"] = served["tokens_out"]
        return {"sensors": sensors}

    def _configure_again(self):
        try:
            self._configure(self.cfg)
        except Fault:
            pass

    def _reply(self, cid, body, community=False):
        try:
            req = urllib.request.Request(self.endpoint + "/v1/chat/completions", data=json.dumps(body).encode("utf-8"),
                                         headers={"Content-Type": "application/json", "Accept": "application/json"})
            started = self.clock()
            with self.opener(req, timeout=self.timeout_s) as r:
                doc = json.loads(r.read(4 * 1024 * 1024).decode("utf-8"))
            choice = (doc.get("choices") or [{}])[0] if isinstance(doc, dict) else {}
            msg = choice.get("message") if isinstance(choice, dict) else None
            content = msg.get("content") if isinstance(msg, dict) else None
            calls = []
            if isinstance(msg, dict) and isinstance(msg.get("tool_calls"), list) and body.get("tools"):
                for i, c in enumerate(msg["tool_calls"][:MAX_CALLS]):
                    made = _call(c)
                    if made is not None:
                        made["id"] = made["id"] or "call_%d" % (i + 1)   # some servers give no id
                        calls.append(made)
            if not isinstance(content, str):
                if not calls:
                    return self.fail(cid, "hardware", "the model server gave no reply")
                content = ""
            out = {"model": str(doc.get("model") or body["model"]),
                   "message": {"role": "assistant", "content": content[:MAX_REPLY_CHARS]},
                   "ms": int((self.clock() - started) * 1000)}
            if calls:
                out["message"]["tool_calls"] = calls
            if isinstance(choice.get("finish_reason"), str):
                out["finish_reason"] = choice["finish_reason"]
            usage = doc.get("usage")
            if isinstance(usage, dict):
                out["usage"] = {k: int(usage[k]) for k in ("prompt_tokens", "completion_tokens", "total_tokens")
                                if isinstance(usage.get(k), (int, float))}
            self._measure(body["model"], (out.get("usage") or {}).get("completion_tokens"), out["ms"])
            if community:
                usage = out.get("usage") or {}
                with self.lock:
                    self.community["requests"] += 1
                    self.community["tokens_in"] += int(usage.get("prompt_tokens") or 0)
                    self.community["tokens_out"] += int(usage.get("completion_tokens") or 0)
                out["community"] = True
            self.complete(cid, out)
        except urllib.error.HTTPError as e:
            self.fail(cid, "hardware", "the model server answered %d" % e.code)
        except (OSError, ValueError, urllib.error.URLError) as e:
            self.fail(cid, "hardware", "the model server did not answer: %s" % (getattr(e, "reason", None) or e))
        finally:
            self._release()
