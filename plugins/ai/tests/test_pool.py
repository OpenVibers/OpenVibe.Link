"""Machine pools: the trust boundary of llama.cpp RPC, which has no authentication of its own.

Real TLS sockets and real keys throughout, never a mocked authentication decision. Machines on loopback stand in for
the local network (LoopbackRuntime maps each machine's private address to a loopback port; the pool documents keep
their real private addresses, so every same-network check still runs). test_pool_linux.py runs the same flows in
network namespaces with engines, and with real llama.cpp in the pool workflow.
"""
import json
import os
import socket
import ssl
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
sys.path.insert(0, str(HERE.parent.parent / "sdk"))

from openvibe_plugin import StopInfo  # noqa: E402
import openvibe_ai  # noqa: E402
from openvibe_ai import Ai  # noqa: E402
from openvibe_ai.pool import Identity, Tunnel, validate_pool, same_network, fit_table, probe  # noqa: E402
from openvibe_ai.pool_registry import Membership  # noqa: E402
from openvibe_ai.pool_runtime import PoolRuntime  # noqa: E402

OWNER = "usr_01J8Z3Q4R5S6T7V8W9X0Y1Z2A1"
HEAD, MEMBER, OTHER = ("dev_01J8Z3Q4R5S6T7V8W9X0Y1Z2D%d" % i for i in (1, 2, 3))
ENGINE = {"rpc_binary": "/opt/llama/rpc-server", "llama_binary": "/opt/llama/llama-server", "model_path": "/opt/models/small.gguf",
          "model": {"model": "small", "parameters_b": 1, "memory_mb": 1000}}


def record(*identities):
    doc = {"id": "pool_01J8Z3Q4R5S6T7V8W9X0Y1Z2P1", "owner": OWNER, "head": HEAD, "machines": [
        {"id": HEAD, "owner": OWNER, "public_ip": "203.0.113.1", "address": "192.168.1.2/24", "port": 50053, "free_vram": 8000, "free_ram": 16000, "hardware": "test", "certificate": None},
        {"id": MEMBER, "owner": OWNER, "public_ip": "203.0.113.1", "address": "192.168.1.3/24", "port": 50053, "free_vram": 4000, "free_ram": 16000, "hardware": "test", "certificate": None}]}
    for member, identity in zip(doc["machines"], identities):
        member["certificate"] = identity.certificate
    return doc


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class LoopbackRuntime(PoolRuntime):
    """A PoolRuntime whose machines listen on loopback ports instead of their private addresses."""
    ports = {}

    def _peer_address(self, peer):
        return ("127.0.0.1", self.ports[peer["id"]])

    def _measure(self, peer):
        # The real probe over the real link; loopback stands in for a LAN, so its speed under a loaded test machine
        # is not the point (fit_table's gates have their own test).
        result = PoolRuntime._measure(self, peer)
        return dict(result, rtt_ms=min(result["rtt_ms"], 1.0), mbps=max(result["mbps"], 10000.0))

    def _make_tunnel(self):
        return Tunnel(self.identity, self.head["certificate"], "127.0.0.1", self.ports[self.machine], self.status,
                      self.directory / "rpc.sock", test_loopback=True, activate=self.activate)


class FakeEngine:
    """What the namespace factory returns: a live 'process' until closed."""
    started = []

    def __init__(self, cfg):
        self.cfg = cfg
        self.closed = False
        self.process = self
        FakeEngine.started.append(self)

    def poll(self):
        return 0 if self.closed else None

    def close(self):
        self.closed = True


def echo_server(path):
    """A stand-in rpc-server on the member's private Unix socket: echoes what the head sends."""
    listener = socket.socket(socket.AF_UNIX)
    listener.bind(str(path))
    listener.listen(4)

    def serve():
        while True:
            try:
                conn, _ = listener.accept()
            except OSError:
                return
            with conn:
                data = conn.recv(1024)
                conn.sendall(b"echo:" + data)
    threading.Thread(target=serve, daemon=True).start()
    return listener


@unittest.skipUnless(sys.platform == "linux", "machine pools run on Linux")
class PoolSecurity(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.head = Identity.approve(root / "head" / "identity")
        self.member = Identity.approve(root / "member" / "identity")
        self.other = Identity.approve(root / "other" / "identity")       # another approved member, not the head
        self.stranger = Identity.approve(root / "stranger" / "identity")

    def tunnel(self, ready=True, rpc_socket=None):
        tunnel = Tunnel(self.member, self.head.certificate, "127.0.0.1", 0, lambda: {"ready": ready}, rpc_socket, test_loopback=True)
        tunnel.start()
        self.addCleanup(tunnel.close)
        return tunnel

    def rpc(self, identity, tunnel):
        with identity.connect(tunnel.address, self.member.certificate) as conn:
            conn.sendall(b"RPC\n")
            if conn.recv(3) != b"OK\n":
                return None
            conn.sendall(b"tensor")
            return conn.recv(64)

    # rpc-server listens only on the private tunnel and accepts only the head.
    def test_only_the_head_reaches_the_rpc_server(self):
        path = Path(self.tmp.name) / "rpc.sock"
        self.addCleanup(echo_server(path).close)
        tunnel = self.tunnel(rpc_socket=path)
        self.assertEqual(self.rpc(self.head, tunnel), b"echo:tensor", "the head's tensors reach rpc-server")
        for name, identity in (("another member", self.other), ("a stranger", self.stranger)):
            with self.subTest(name), self.assertRaises((ssl.SSLError, OSError)):
                self.rpc(identity, tunnel)
        self.assertEqual(tunnel.rpc_connections, 1, "only the head's connection was bridged")

    def test_plain_connection_outside_the_tunnel_is_refused(self):
        path = Path(self.tmp.name) / "rpc.sock"
        self.addCleanup(echo_server(path).close)
        tunnel = self.tunnel(rpc_socket=path)
        with socket.create_connection(tunnel.address, timeout=2) as conn:
            conn.sendall(b"RPC\ntensor")
            try:
                self.assertEqual(conn.recv(64), b"", "no TLS, no RPC")
            except ConnectionResetError:
                pass
        self.assertEqual(tunnel.rpc_connections, 0)

    def test_no_rpc_while_this_member_is_not_ready(self):
        path = Path(self.tmp.name) / "rpc.sock"
        self.addCleanup(echo_server(path).close)
        tunnel = self.tunnel(ready=False, rpc_socket=path)
        self.assertIsNone(self.rpc(self.head, tunnel), "a paused member bridges nothing")

    def test_the_head_pins_each_member_too(self):
        tunnel = self.tunnel()
        with self.assertRaises((ssl.SSLError, OSError)):
            self.head.connect(tunnel.address, self.stranger.certificate)

    def test_never_a_public_loopback_or_wildcard_listener(self):
        for address in ("0.0.0.0", "::", "8.8.8.8", "100.64.0.1", "127.0.0.1", "localhost"):
            with self.subTest(address=address), self.assertRaises(ValueError):
                Tunnel(self.member, self.head.certificate, address, 50053, lambda: {})

    def test_only_tensor_measurement_and_state_operations(self):
        tunnel = self.tunnel()
        with self.head.connect(tunnel.address, self.member.certificate) as conn:
            conn.sendall(b"EXEC /bin/sh\n")
            self.assertEqual(conn.recv(10), b"", "anything else closes the link")

    # A machine off the local network is refused by the link probe.
    def test_off_network_is_refused_before_any_connection(self):
        a, b = record(self.head, self.member)["machines"]
        self.assertTrue(same_network(a, b))
        for key, value in (("public_ip", "203.0.113.2"), ("address", "192.168.2.3/24"), ("address", "8.8.8.8/24"),
                           ("address", "192.168.1.2/24"), ("public_ip", "")):
            target = dict(b, **{key: value})
            with self.subTest(**{key: value}):
                self.assertFalse(same_network(a, target))
                with self.assertRaises(ValueError):
                    probe(self.head, a, target, ("127.0.0.1", 9))

    def test_the_probe_measures_an_approved_member(self):
        tunnel = self.tunnel()
        a, b = record(self.head, self.member)["machines"]
        result = probe(self.head, a, b, tunnel.address)
        self.assertEqual(result["machine"], MEMBER)
        self.assertGreater(result["mbps"], 0)
        with self.assertRaises((ssl.SSLError, OSError)):
            probe(self.stranger, a, b, tunnel.address)

    def test_every_member_approves_with_its_own_key_and_the_same_owner(self):
        doc = record()
        with self.assertRaises(ValueError):
            validate_pool(doc)
        doc = record(self.head, self.member)
        validate_pool(doc)
        same = record(self.head, self.head)
        with self.assertRaises(ValueError):
            validate_pool(same)
        doc["machines"][1]["owner"] = "usr_01J8Z3Q4R5S6T7V8W9X0Y1Z2A2"
        with self.assertRaises(ValueError):
            validate_pool(doc)
        doc = record(self.head, self.member)
        doc["head"] = OTHER
        with self.assertRaises(ValueError):
            validate_pool(doc)

    def test_fit_does_not_invent_speed_and_checks_the_link_gates(self):
        machines = record()["machines"]
        models = [{"model": "small", "parameters_b": 7, "memory_mb": 8000}, {"model": "large", "parameters_b": 70, "memory_mb": 40000}]
        result = fit_table(machines, [{"rtt_ms": 1, "mbps": 999}], models, [])
        self.assertTrue(result[0]["fits"])
        self.assertIsNone(result[0]["tokens_per_second"], "no measured speed, no number")
        self.assertFalse(result[1]["fits"], "a 70B model needs 1 Gb/s and the memory")
        self.assertFalse(fit_table(machines, [{"rtt_ms": 5, "mbps": 1000}], models, [])[0]["fits"], "5 ms is too slow")
        measured = [{"model": "small", "hardware": ["test", "test"], "tokens_per_second": 31.5, "source": "bench", "measured_at": "2026-11-06"}]
        self.assertEqual(fit_table(machines, [{"rtt_ms": 1, "mbps": 999}], models, measured)[0]["tokens_per_second"], 31.5)


class FakeRegistry:
    def __init__(self, doc):
        self.doc, self.calls, self.fail = doc, [], None

    def renew(self, pool_id, ready):
        self.calls.append(("renew", ready))
        if self.fail:
            raise self.fail
        return self.doc

    def pool(self, pool_id):
        self.calls.append(("pool",))
        return self.doc


def http_error(code):
    return urllib.error.HTTPError("https://openvibe.network/api", code, "refused", {}, None)


class MembershipTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.key = Path(self.tmp.name) / "key.pem"
        self.key.write_text("key")
        self.now = [0.0]
        self.registry = FakeRegistry({"id": "pool_x"})
        self.check = Membership(self.registry, "pool_x", self.key, ready=lambda: True, clock=lambda: self.now[0])

    def test_renews_every_two_seconds_and_reads_the_pool_back(self):
        self.assertEqual(self.check(), {"id": "pool_x"})
        self.now[0] = 1.0
        self.check()
        self.assertEqual(len(self.registry.calls), 1, "between renewals the last answer stands")
        self.now[0] = 2.5
        self.check()
        self.assertEqual(self.registry.calls, [("renew", True), ("renew", True)])

    def test_removed_signed_out_or_another_owner_means_gone(self):
        for code in (401, 403, 404, 409):
            with self.subTest(code=code):
                self.registry.fail = http_error(code)
                self.registry.pool = lambda pool_id, code=code: (_ for _ in ()).throw(http_error(code))
                self.check.doc = None
                self.assertIsNone(self.check())
        self.registry.fail = FileNotFoundError("credential.json")
        self.check.doc = None
        self.assertIsNone(self.check(), "the credential file is gone: signed out")

    def test_a_deleted_key_is_gone_at_once(self):
        self.check()
        self.key.unlink()
        self.assertIsNone(self.check())

    def test_an_outage_raises_so_the_pool_pauses_but_keeps_its_key(self):
        self.registry.fail = http_error(503)
        with self.assertRaises(urllib.error.HTTPError):
            self.check()
        self.registry.fail = urllib.error.URLError("timed out")
        with self.assertRaises(OSError):
            self.check()


@unittest.skipUnless(sys.platform == "linux", "machine pools run on Linux")
class PoolRuntimeTests(unittest.TestCase):
    """Two machines on loopback: the head's checks pause the whole pool, and leaving drops the link and the key."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.dirs = {HEAD: root / "head", MEMBER: root / "member"}
        identities = [Identity.approve(self.dirs[m] / "identity") for m in (HEAD, MEMBER)]
        self.doc = record(*identities)
        LoopbackRuntime.ports = {HEAD: free_port(), MEMBER: free_port()}
        self.busy = {HEAD: False, MEMBER: False}
        self.current = {HEAD: self.doc, MEMBER: self.doc}
        FakeEngine.started = []
        self.head = self.runtime(HEAD)
        self.member = self.runtime(MEMBER)
        self.member.start(watch=False)
        self.head.start(watch=False)
        self.addCleanup(self.head.close)
        self.addCleanup(self.member.close)

    def runtime(self, machine):
        return LoopbackRuntime(self.doc, machine, self.dirs[machine], dict(ENGINE), lambda: self.busy[machine],
                               lambda: self.current[machine], namespace_factory=FakeEngine)

    def run_pool(self):
        """Ticks in the order the watch threads would interleave them, until the head serves."""
        for _ in range(4):
            self.head.tick()
            self.member.tick()
        self.assertIsNotNone(self.head.worker, self.head.last_error)
        self.assertFalse(self.head.paused)
        self.assertIsNotNone(self.member.worker)

    def test_the_head_starts_only_after_every_member_runs(self):
        self.head.tick()
        self.assertIsNone(self.head.worker, "the member's rpc-server is not running yet")
        self.assertTrue(self.member.head_running, "the head told the member to run")
        self.member.tick()
        self.assertIsNotNone(self.member.worker)
        self.assertEqual(self.member.worker.cfg["role"], "member")
        self.head.tick()
        self.assertEqual(self.head.worker.cfg["role"], "head")
        self.assertEqual(len(self.head.measurements), 1, "the link was measured before serving")
        self.assertEqual(len(self.head.worker.cfg["links"]), 1, "llama-server reaches the member only through the link")

    def test_idle_only_on_one_member_pauses_the_whole_pool(self):
        self.run_pool()
        self.busy[MEMBER] = True
        self.member.tick()
        self.head.tick()
        self.assertTrue(self.head.paused)
        self.assertIsNone(self.head.worker, "the head stops serving")
        self.assertIsNone(self.member.worker, "the busy member stops its rpc-server")
        with self.assertRaises(OSError):
            self.head.opener(None)
        self.busy[MEMBER] = False
        self.run_pool()

    def test_idle_only_on_the_head_pauses_every_member(self):
        self.run_pool()
        self.busy[HEAD] = True
        self.head.tick()
        self.member.tick()
        self.assertIsNone(self.head.worker)
        self.assertIsNone(self.member.worker, "the head told the member to pause")

    def test_the_kill_switch_on_a_member_stops_the_pool_at_once(self):
        self.run_pool()
        self.busy[MEMBER] = True   # the plugin's busy check holds the latch
        self.member.halt()
        self.assertIsNone(self.member.worker, "stopped before the next tick")
        self.head.tick()
        self.assertIsNone(self.head.worker)

    def test_a_silent_head_pauses_its_members(self):
        self.run_pool()
        self.member.tunnel.last_head = time.monotonic() - 4
        self.member.tick()
        self.assertIsNone(self.member.worker, "no head call for three seconds: the member stops")

    def test_leaving_the_pool_drops_the_tunnel_and_the_key(self):
        self.run_pool()
        port = LoopbackRuntime.ports[MEMBER]
        open_link = self.head.identity.connect(("127.0.0.1", port), self.member.me["certificate"])
        self.addCleanup(open_link.close)
        self.member.leave()
        self.assertFalse((self.dirs[MEMBER] / "identity").exists(), "the key is deleted")
        self.assertIsNone(self.member.worker)
        with self.assertRaises(OSError):
            socket.create_connection(("127.0.0.1", port), timeout=.3).close()
        open_link.settimeout(1)
        try:
            self.assertEqual(open_link.recv(1), b"", "an open link is closed too")
        except (OSError, ssl.SSLError):
            pass
        with self.assertRaises(OSError):
            self.head.tick()          # the head cannot reach the member: the watch loop pauses the pool
        self.head.pause()
        self.assertIsNone(self.head.worker)

    def test_revoked_removed_or_changed_membership_leaves_at_once(self):
        self.run_pool()
        self.current[MEMBER] = None   # Network: revoked, removed, signed out or another owner
        self.member.tick()
        self.assertTrue(self.member.stopped.is_set())
        self.assertFalse((self.dirs[MEMBER] / "identity").exists())
        self.current[HEAD] = dict(self.doc, head=MEMBER)
        self.head.tick()
        self.assertTrue(self.head.stopped.is_set())
        self.assertFalse((self.dirs[HEAD] / "identity").exists())

    def test_closing_keeps_the_key(self):
        self.run_pool()
        self.member.close()
        self.assertTrue((self.dirs[MEMBER] / "identity" / "key.pem").exists(), "the Node stopping is not leaving")
        self.assertIsNone(self.member.worker)


class FakeRuntime:
    def __init__(self):
        self.halted = self.closed = self.left = 0
        self.paused = False
        self.machine = HEAD
        self.doc = {"id": "pool_x", "head": HEAD, "machines": [{"id": HEAD}, {"id": MEMBER}]}
        self.stopped = threading.Event()
        self.pids = {4242}

    def halt(self):
        self.halted += 1
        self.paused = True

    def close(self):
        self.closed += 1

    def leave(self):
        self.left += 1

    def status(self):
        return {"ready": not self.paused}

    def engine_pids(self):
        return self.pids


@unittest.skipUnless(sys.platform == "linux", "machine pools run on Linux")
class PluginPoolTests(unittest.TestCase):
    """The ai plugin in pool mode: the kill switch, stops that are not about the pool, and idle-only's GPU signal."""

    def plugin(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        plugin = Ai(busy_probe=lambda: (False, None))
        cfg = {"pool_directory": tmp.name, "pool_engine": ENGINE, "offer": {"enabled": True, "quiet_minutes": 0}}
        plugin._configure(cfg)
        plugin.pool_runtime = FakeRuntime()
        return plugin

    def test_describe_cannot_start_a_pool(self):
        plugin = Ai()
        out = plugin.describe({"pool_directory": "/does-not-exist", "pool_engine": ENGINE, "offer": {"enabled": True}})
        self.assertIsNone(plugin.pool_thread, "describe never starts a pool or makes a key")
        self.assertFalse(out["ai"]["offer"]["ready"])
        self.assertEqual(out["ai"]["pool"]["state"], "waiting")
        self.assertEqual(out["ai"]["models"], ["small"])

    def test_a_bad_engine_config_is_refused(self):
        plugin = Ai()
        for engine in (None, dict(ENGINE, rpc_binary="rpc-server"), dict(ENGINE, model={"model": "x"}),
                       dict(ENGINE, model=dict(ENGINE["model"], parameters_b=True))):
            with self.subTest(engine=engine):
                self.assertEqual(plugin.describe({"pool_directory": "/tmp/pool", "pool_engine": engine}), {})

    def test_the_kill_switch_stops_the_pool_until_the_latch_is_cleared(self):
        plugin = self.plugin()
        self.assertFalse(plugin._pool_busy())
        plugin.on_stop(StopInfo("remote", emergency=True, latched=True))
        self.assertEqual(plugin.pool_runtime.halted, 1, "stopped at once")
        self.assertTrue(plugin._pool_busy(), "and held stopped")
        self.assertEqual(plugin.pool_runtime.left, 0, "the kill switch stops; it does not leave")
        plugin.on_resume(StopInfo("cleared"))
        self.assertFalse(plugin._pool_busy())

    def test_other_stops_are_not_about_the_pool(self):
        plugin = self.plugin()
        for cause in ("halt", "link_lost", "timeout", "heartbeat", "setup"):
            plugin.on_stop(StopInfo(cause))
        runtime = plugin.pool_runtime
        self.assertEqual((runtime.halted, runtime.closed, runtime.left), (0, 0, 0))
        plugin.on_stop(StopInfo("shutdown"))
        self.assertEqual((runtime.closed, runtime.left), (1, 0), "the Node stopping closes the pool and keeps the key")

    def test_the_pools_own_gpu_use_is_not_the_owner_using_the_machine(self):
        plugin = self.plugin()
        plugin.busy_probe = None
        plugin.offer["busy_processes"] = []
        saved = openvibe_ai._gpu_busy, openvibe_ai._gpu_compute_pids, openvibe_ai._input_idle_ms
        self.addCleanup(lambda: setattr(openvibe_ai, "_gpu_busy", saved[0]) or setattr(openvibe_ai, "_gpu_compute_pids", saved[1]) or setattr(openvibe_ai, "_input_idle_ms", saved[2]))
        openvibe_ai._gpu_busy = lambda threshold: True
        openvibe_ai._input_idle_ms = lambda: None
        openvibe_ai._gpu_compute_pids = lambda: {4242}
        self.assertEqual(plugin._raw_busy(), (False, None), "only the pool's engine is on the GPU")
        plugin._busy_cache = (0.0, False, None)
        openvibe_ai._gpu_compute_pids = lambda: {4242, 999}
        self.assertEqual(plugin._raw_busy(), (True, "gpu"), "another program on the GPU is the owner using it")
        plugin._busy_cache = (0.0, False, None)
        plugin.pool_runtime.pids = set()
        openvibe_ai._gpu_compute_pids = lambda: {4242}
        self.assertEqual(plugin._raw_busy(), (True, "gpu"), "with no engine running, a busy GPU is someone else's")

    def test_a_pool_revoked_while_away_drops_the_key_and_the_approval(self):
        import openvibe_ai.pool_registry as registry_module
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        Identity.approve(root / "identity")
        (root / "approval.json").write_text(json.dumps({"pool_id": "pool_x", "credential_file": str(root / "credential.json"),
                                                        "owner": OWNER, "head": HEAD, "machines": sorted([HEAD, MEMBER])}))

        class Revoked:
            def __init__(self, path):
                pass

            def renew(self, pool_id, ready):
                raise http_error(404)

            pool = renew

            def credentials(self):
                return {"device_id": MEMBER}
        saved = registry_module.Registry
        registry_module.Registry = Revoked
        self.addCleanup(setattr, registry_module, "Registry", saved)
        plugin = Ai()
        plugin.setup({"pool_directory": str(root), "pool_engine": ENGINE, "offer": {"enabled": True}})
        self.addCleanup(plugin.close)
        until = time.monotonic() + 10
        while (root / "approval.json").exists() and time.monotonic() < until:
            time.sleep(.05)
        self.assertFalse((root / "approval.json").exists(), "the approval is forgotten")
        self.assertFalse((root / "identity").exists(), "the key is deleted")
        self.assertIsNone(plugin.pool_runtime, "nothing runs")
        self.assertIn("no longer in pool", plugin.pool_error)

    def test_only_the_serving_head_takes_requests(self):
        plugin = self.plugin()
        self.assertTrue(plugin._pool_serving())
        plugin.pool_runtime.machine = MEMBER
        self.assertFalse(plugin._pool_serving())
        self.assertFalse(plugin._offer_view()["ready"], "a member never offers on its own")
        plugin.pool_runtime.machine = HEAD
        plugin.pool_runtime.paused = True
        self.assertFalse(plugin._offer_view()["ready"])


if __name__ == "__main__":
    unittest.main()
