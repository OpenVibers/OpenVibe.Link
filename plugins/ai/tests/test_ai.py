"""The ai plugin (openvibe_ai): the person's own model server, reached from OpenVibe with no open port. Against a fake
OpenAI-compatible server: it offers the server's models, relays one chat completion and answers the reply, refuses a
model the machine does not offer and a second request while one runs, and says so when no server answers."""
import http.server
import io
import json
import os
import socketserver
import sys
import threading
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))

from openvibe_plugin import Runtime  # noqa: E402
from openvibe_ai import Ai  # noqa: E402


class FakeServer(http.server.BaseHTTPRequestHandler):
    """An OpenAI-compatible model server (as Ollama, LM Studio and llama.cpp's server answer)."""
    seen = []
    hold = threading.Event()

    def _send(self, status, doc):
        body = json.dumps(doc).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/v1/models":
            return self._send(200, {"object": "list", "data": [{"id": "llama3.1:8b"}, {"id": "qwen2.5:7b"}, {"id": "broken"}, {"id": "slow"}]})
        self._send(404, {"error": "not found"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        FakeServer.seen.append((self.path, body))
        if body.get("model") == "slow":
            FakeServer.hold.wait(5)
        if self.path != "/v1/chat/completions":
            return self._send(404, {"error": "not found"})
        if body.get("model") == "broken":
            return self._send(500, {"error": "out of memory"})
        if body.get("tools") and body["messages"][-1]["role"] != "tool":
            # The model asks for tools: one call with an id and JSON text, one (as Ollama may) with neither.
            calls = [{"id": "call_x", "type": "function", "function": {"name": body["tools"][0]["function"]["name"], "arguments": "{\"q\": \"Lisbon\"}"}},
                     {"type": "function", "function": {"name": "clock", "arguments": {"tz": "UTC"}}}]
            return self._send(200, {"model": body["model"], "choices": [{"index": 0, "finish_reason": "tool_calls",
                                                                        "message": {"role": "assistant", "content": None, "tool_calls": calls}}]})
        last = body["messages"][-1]["content"]
        self._send(200, {"model": body["model"], "choices": [{"index": 0, "finish_reason": "stop",
                                                                "message": {"role": "assistant", "content": "You said: " + last}}],
                         "usage": {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16}})

    def log_message(self, *a):
        pass


class Out(io.StringIO):
    def msgs(self):
        return [json.loads(x) for x in self.getvalue().splitlines() if x]

    def wait_for(self, pred, timeout=10):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            found = [m for m in self.msgs() if pred(m)]
            if found:
                return found[0]
            time.sleep(0.02)
        raise AssertionError("no such message: %s" % self.getvalue()[-500:])


class AiPluginTest(unittest.TestCase):
    def setUp(self):
        self.srv = socketserver.ThreadingTCPServer(("127.0.0.1", 0), FakeServer)
        self.srv.daemon_threads = True
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.endpoint = "http://127.0.0.1:%d" % self.srv.server_address[1]
        FakeServer.seen = []
        FakeServer.hold.clear()

    def tearDown(self):
        FakeServer.hold.set()
        self.srv.shutdown()

    def boot(self, **config):
        self.out = Out()
        self.p = Ai()
        self.p.log = lambda *a: None
        self.rt = Runtime(self.p, infile=io.StringIO(""), outfile=self.out)
        self.rt.feed_line(json.dumps({"op": "hello", "config": {"endpoint": self.endpoint, **config}}))
        self.n = 0
        return self.out.msgs()

    def ask(self, value):
        self.n += 1
        cid = "a%d" % self.n
        self.rt.feed_line(json.dumps({"op": "command", "id": cid, "kind": "ai", "value": value, "deadline_ms": 30000}))
        return self.out.wait_for(lambda m: m.get("id") == cid and m.get("op") in ("ack", "nack"))

    def test_offers_the_servers_models_and_relays_one_reply(self):
        caps = self.boot()[0]["capabilities"]["ai"]
        self.assertEqual((caps["models"][:2], caps["ready"], caps["max_tokens"]), (["llama3.1:8b", "qwen2.5:7b"], True, 2048))
        r = self.ask({"model": "llama3.1:8b", "messages": [{"role": "system", "content": "Be brief."}, {"role": "user", "content": "hello"}],
                      "max_tokens": 50, "temperature": 0.2})
        self.assertEqual(r["op"], "ack", r)
        self.assertEqual(r["data"]["message"], {"role": "assistant", "content": "You said: hello"})
        self.assertEqual((r["data"]["model"], r["data"]["finish_reason"]), ("llama3.1:8b", "stop"))
        self.assertEqual(r["data"]["usage"], {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16})
        path, body = FakeServer.seen[-1]
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual((body["max_tokens"], body["temperature"], body["stream"]), (50, 0.2, False))

    def test_tools_go_to_the_model_and_its_calls_come_back_never_run(self):
        caps = self.boot()[0]["capabilities"]["ai"]
        self.assertIs(caps["tools"], True, "the machine says it can pass tools")
        tools = [{"type": "function", "function": {"name": "web_search", "description": "Search the web",
                                                    "parameters": {"type": "object", "properties": {"q": {"type": "string"}}}}},
                 {"type": "function", "function": {"name": "clock"}}]
        first = [{"role": "user", "content": "Weather in Lisbon?"}]
        r = self.ask({"model": "qwen2.5:7b", "messages": first, "tools": tools})
        self.assertEqual(r["op"], "ack", r)
        msg = r["data"]["message"]
        self.assertEqual(msg["content"], "", "a reply that only asks for tools has empty text")
        self.assertEqual(msg["tool_calls"], [
            {"id": "call_x", "type": "function", "function": {"name": "web_search", "arguments": '{"q": "Lisbon"}'}},
            {"id": "call_2", "type": "function", "function": {"name": "clock", "arguments": '{"tz": "UTC"}'}},
        ], "an id is made where the server gave none, and object arguments become JSON text")
        self.assertEqual(r["data"]["finish_reason"], "tool_calls")
        body = FakeServer.seen[-1][1]
        self.assertEqual((body["tools"], body["tool_choice"]), (tools, "auto"), "the tools reach the model server as written")
        # The caller ran them; the next command carries the calls and their results.
        second = first + [msg, {"role": "tool", "tool_call_id": "call_x", "content": "19 C, sunny"},
                          {"role": "tool", "tool_call_id": "call_2", "content": "15:40"}]
        r = self.ask({"model": "qwen2.5:7b", "messages": second, "tools": tools, "tool_choice": "auto"})
        self.assertEqual(r["data"]["message"], {"role": "assistant", "content": "You said: 15:40"})
        sent = FakeServer.seen[-1][1]["messages"]
        self.assertEqual(sent[1]["tool_calls"][0]["id"], "call_x")
        self.assertEqual((sent[2]["role"], sent[2]["tool_call_id"]), ("tool", "call_x"))
        # Without tools, a reply's tool calls are never passed on (nothing asked for them).
        r = self.ask({"model": "qwen2.5:7b", "messages": [{"role": "user", "content": "plain"}]})
        self.assertNotIn("tool_calls", r["data"]["message"])
        self.assertNotIn("tools", FakeServer.seen[-1][1])

    def test_tool_messages_that_break_the_contract_are_refused(self):
        self.boot()
        bad = [
            {"messages": [{"role": "user", "content": "x"}, {"role": "tool", "content": "orphan"}]},
            {"messages": [{"role": "user", "content": "x", "tool_call_id": "c1"}]},
            {"messages": [{"role": "user", "content": "x", "tool_calls": [{"id": "c", "type": "function", "function": {"name": "a", "arguments": "{}"}}]}]},
            {"messages": [{"role": "assistant", "content": "", "tool_calls": [{"type": "function", "function": {"name": "a", "arguments": "{}"}}]}]},
            {"messages": [{"role": "user", "content": "x"}], "tools": [{"type": "function", "function": {"name": "rm -rf"}}]},
            {"messages": [{"role": "user", "content": "x"}], "tools": []},
            {"messages": [{"role": "user", "content": "x"}], "tools": [{"type": "function", "function": {"name": "a"}}], "tool_choice": "required"},
        ]
        for v in bad:
            r = self.ask({"model": "qwen2.5:7b", **v})
            self.assertEqual((r["op"], r.get("fault_code")), ("nack", "bad_value"), v)
        self.assertEqual(FakeServer.seen, [], "nothing reached the model server")

    def test_it_says_how_fast_each_model_answers_here(self):
        self.boot()
        self.assertEqual(self.p.poll(), {"sensors": {"ai_busy": 0}}, "nothing measured before the first answer")
        ticks = iter([10.0, 10.5, 20.0, 21.0])   # the first answer takes 0.5 s, the second 1 s
        self.p.clock = lambda: next(ticks)
        self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "hi"}]})
        self.assertEqual(self.p.poll()["sensors"], {"ai_busy": 0, "ai_model": "llama3.1:8b", "ai_tokens_per_s": 8.0},
                         "4 reply tokens in half a second")
        self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "again"}]})
        self.assertEqual(self.p.poll()["sensors"]["ai_tokens_per_s"], round(8.0 * 0.7 + 4.0 * 0.3, 1), "the latest counts most")

    def test_a_config_can_offer_fewer_models_and_cap_the_reply(self):
        caps = self.boot(models=["qwen2.5:7b"], max_tokens=100)[0]["capabilities"]["ai"]
        self.assertEqual(caps["models"], ["qwen2.5:7b"])
        r = self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "x"}]})
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "bad_value"), "a model the machine does not offer")
        self.ask({"model": "qwen2.5:7b", "messages": [{"role": "user", "content": "x"}], "max_tokens": 5000})
        self.assertEqual(FakeServer.seen[-1][1]["max_tokens"], 100, "capped by the owner's max_tokens")

    def test_refusals(self):
        self.boot(models=["llama3.1:8b", "broken", "slow"])
        bad = [
            {"model": "llama3.1:8b", "messages": []},
            {"model": "llama3.1:8b", "messages": [{"role": "tool", "content": "x"}]},
            {"model": "", "messages": [{"role": "user", "content": "x"}]},
            {"model": "llama3.1:8b", "messages": [{"role": "user", "content": "x"}], "temperature": 5},
        ]
        for value in bad:
            self.assertEqual(self.ask(value)["fault_code"], "bad_value", value)
        self.assertEqual(self.ask({"model": "broken", "messages": [{"role": "user", "content": "x"}]})["fault_code"], "hardware")
        # One at a time: a second request while one runs is refused, not queued.
        self.n += 1
        self.rt.feed_line(json.dumps({"op": "command", "id": "slow1", "kind": "ai", "value": {"model": "slow", "messages": [{"role": "user", "content": "x"}]}, "deadline_ms": 30000}))
        time.sleep(0.2)
        self.assertEqual(self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "x"}]})["fault_code"], "not_ready")
        FakeServer.hold.set()
        self.assertEqual(self.out.wait_for(lambda m: m.get("id") == "slow1" and m.get("op") in ("ack", "nack"))["op"], "ack")
        self.assertEqual(self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "again"}]})["op"], "ack", "free again")

    def test_no_server_answers(self):
        self.srv.shutdown()
        self.srv.server_close()
        caps = self.boot()[0]["capabilities"]["ai"]
        self.assertEqual((caps["models"], caps["ready"]), ([], False))
        r = self.ask({"model": "llama3.1:8b", "messages": [{"role": "user", "content": "x"}]})
        self.assertEqual(r["fault_code"], "not_ready")


if __name__ == "__main__":
    unittest.main()
