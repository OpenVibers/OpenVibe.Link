"""OpenVibe Node ai plugin: the person's own models for their own OpenVibe work (contracts 0.219.0, kind ``ai``).

Many people already run models at home (Ollama, llama.cpp's server, LM Studio). This plugin lets OpenVibe use them for
their owner, for Vibe's answers or an agent's thinking, without the prompt leaving the machine's network and with no
open port: OpenVibe.AI asks OpenVibe.Bot, Bot sends the Node one ``ai`` command over the connection the Node already
keeps, and this plugin relays it to the local model server and answers with the reply.

It reaches one place only, the server in its config, and it reads and changes nothing else on the machine. One request
runs at a time (a model is heavy); another is refused as busy, never queued. The owner turns it on twice: by adding the
plugin here, and by "Let my AI use this computer's models" on openvibe.bot/machines.

Config (the plugin's block in config.json):

* ``endpoint``: the model server's OpenAI-compatible base, without ``/v1``. By default the first one that answers of
  Ollama (``http://127.0.0.1:11434``), LM Studio (``http://127.0.0.1:1234``) and llama.cpp's server
  (``http://127.0.0.1:8080``).
* ``models``: the models offered (a list); by default every model the server lists.
* ``max_tokens``: the most a reply may be (default 2048, at most 32768).
* ``timeout_s``: how long one reply may take (default 180, at most 900).

Command ``ai`` ``{"model", "messages": [{"role", "content"}], "max_tokens"?, "temperature"?, "tools"?, "tool_choice"?}``
→ ``{model, message: {role: "assistant", content, tool_calls?}, usage?, finish_reason?}``.

Tools (contracts 0.277.0, plugin 0.2.0): the functions the model may ask for, passed to the model server as written.
The plugin never runs one: it hands back the calls the model asked for, and the caller (an agent on OpenVibe) runs
them and sends the next command with the assistant's message (its ``tool_calls``) and one ``tool`` message per call.
``describe`` says ``tools: true`` so a caller knows this machine can.
"""

import json
import re
import threading
import time
import urllib.error
import urllib.request

from openvibe_plugin import Deferred, Fault, Plugin

VERSION = "0.2.0"
DEFAULT_ENDPOINTS = ("http://127.0.0.1:11434", "http://127.0.0.1:1234", "http://127.0.0.1:8080")
MAX_REPLY_CHARS = 200 * 1024     # well inside the core's answer limit (512 KB)
MAX_MESSAGES = 200
MAX_CONTENT = 64 * 1024
ROLES = ("system", "user", "assistant", "tool")
MAX_TOOLS = 64
MAX_CALLS = 32
MAX_TOOLS_BYTES = 256 * 1024
NAME = re.compile(r"^[A-Za-z0-9_.-]{1,64}$")


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


class Ai(Plugin):
    driver = "ai"
    version = VERSION
    motion_kinds = frozenset()   # it moves nothing

    def __init__(self, opener=None, clock=time.monotonic):
        self.opener = opener or urllib.request.urlopen
        self.clock = clock
        self.endpoint = None
        self.models = []
        self.max_tokens = 2048
        self.timeout_s = 180
        self.busy = threading.Lock()
        self.cfg = {}
        self.reachable = False
        # How fast each model answers here, in tokens a second of its reply (an average that favours the latest
        # answers), and the model that answered last: the telemetry Bot shows beside the machine's models.
        self.speed = {}
        self.last_model = None

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

    def describe(self, config):
        try:
            self._configure(config)
        except Fault:
            return {}
        return {"ai": {"models": self.models, "ready": self.reachable and bool(self.models),
                       "max_tokens": self.max_tokens, "tools": True}}

    def setup(self, config):
        self._configure(config)
        if self.endpoint is None:
            self.log("no model server answers yet (Ollama, LM Studio or llama.cpp's server, or the endpoint in config)")

    # ---- one reply --------------------------------------------------------------------------------------------------
    def handle(self, cmd):
        if cmd.kind != "ai":
            raise Fault("unsupported", "the ai plugin takes ai commands only")
        v = cmd.value if isinstance(cmd.value, dict) else {}
        model = v.get("model")
        messages = v.get("messages")
        if not isinstance(model, str) or not model or len(model) > 200:
            raise Fault("bad_value", "model is the name of a model this machine offers")
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
        if not self.busy.acquire(blocking=False):
            raise Fault("not_ready", "the model is answering another request on this machine: try again in a moment")
        body = {"model": model, "messages": sent, "max_tokens": max_tokens, "stream": False}
        if temperature is not None:
            body["temperature"] = temperature
        if tools:
            body["tools"] = tools
            body["tool_choice"] = tool_choice or "auto"
        threading.Thread(target=self._reply, args=(cmd.id, body), name="ai-reply", daemon=True).start()
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

    def poll(self):
        """Sensors: whether a model is answering now (ai_busy), and the last model's speed here (ai_model,
        ai_tokens_per_s) once one has answered."""
        sensors = {"ai_busy": 1 if self.busy.locked() else 0}
        if self.last_model is not None and self.last_model in self.speed:
            sensors["ai_model"] = self.last_model[:120]
            sensors["ai_tokens_per_s"] = round(self.speed[self.last_model], 1)
        return {"sensors": sensors}

    def _configure_again(self):
        try:
            self._configure(self.cfg)
        except Fault:
            pass

    def _reply(self, cid, body):
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
            self.complete(cid, out)
        except urllib.error.HTTPError as e:
            self.fail(cid, "hardware", "the model server answered %d" % e.code)
        except (OSError, ValueError, urllib.error.URLError) as e:
            self.fail(cid, "hardware", "the model server did not answer: %s" % (getattr(e, "reason", None) or e))
        finally:
            self.busy.release()
