"""Machine pools in real Linux network namespaces: the engines' isolation and the whole two-machine flow.

Uses bubblewrap when it can make the namespace (Ubuntu's AppArmor profile lets it even with
kernel.apparmor_restrict_unprivileged_userns=1), otherwise unshare. Skips, with a reason, only when neither can.
The real llama.cpp run needs POOL_RPC_BINARY, POOL_LLAMA_BINARY and POOL_MODEL (the pool workflow supplies them).
"""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
sys.path.insert(0, str(HERE.parent.parent / "sdk"))

from openvibe_ai.pool_runtime import (ISOLATION_HELP, Namespace, isolation_method,  # noqa: E402
                                      namespace_prefix)

SCENARIO = HERE / "pool_linux_scenario.py"
REAL = all(os.environ.get(k) for k in ("POOL_RPC_BINARY", "POOL_LLAMA_BINARY", "POOL_MODEL"))
METHOD = isolation_method() if sys.platform == "linux" else None
if METHOD is not None:
    SKIP = None
elif sys.platform != "linux":
    SKIP = "machine pools run on Linux"
else:
    SKIP = "needs bubblewrap or unprivileged user namespaces; neither can make one here"


def run_scenario(real):
    env = dict(os.environ, PYTHONPATH=os.pathsep.join([str(HERE.parent), str(HERE.parent.parent / "sdk"), os.environ.get("PYTHONPATH", "")]))
    if not real:
        for key in ("POOL_RPC_BINARY", "POOL_LLAMA_BINARY", "POOL_MODEL"):
            env.pop(key, None)
    return subprocess.run(namespace_prefix(METHOD, net_admin=True) + [sys.executable, str(SCENARIO)],
                          text=True, capture_output=True, timeout=240, env=env)


@unittest.skipUnless(sys.platform == "linux", "machine pools run on Linux")
class WorkerRefusal(unittest.TestCase):
    def test_the_engine_never_runs_on_the_host_network(self):
        cfg = {"host_namespace": os.readlink("/proc/self/ns/net"), "role": "member"}
        result = subprocess.run([sys.executable, "-m", "openvibe_ai.pool_worker"], input=json.dumps(cfg) + "\n", text=True,
                                capture_output=True, timeout=30,
                                env=dict(os.environ, PYTHONPATH=os.pathsep.join([str(HERE.parent), str(HERE.parent.parent / "sdk")])))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("isolated network namespace", result.stderr)


@unittest.skipIf(METHOD is None, SKIP)
class Isolation(unittest.TestCase):
    def test_rpc_server_is_reachable_only_through_its_private_socket(self):
        with tempfile.TemporaryDirectory() as root:
            fake = Path(root) / "fake-rpc"
            fake.write_text("#!%s\nimport socket, sys\nargs = dict(zip(sys.argv[1::2], sys.argv[2::2]))\n"
                            "s = socket.socket(); s.bind((args['--host'], int(args['--port']))); s.listen(4)\n"
                            "while True:\n    c, _ = s.accept(); c.sendall(b'rpc:' + c.recv(64)); c.close()\n" % sys.executable)
            fake.chmod(0o755)
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", 0))
                port = probe.getsockname()[1]
            path = Path(root) / "rpc.sock"
            engine = Namespace({"role": "member", "rpc_binary": str(fake), "rpc_port": port, "rpc_socket": str(path)})
            try:
                until = time.monotonic() + 10
                while not path.exists() and time.monotonic() < until:
                    time.sleep(.1)
                self.assertEqual(oct(path.stat().st_mode & 0o777), "0o600", "only this user reaches the bridge")
                answer = b""
                while not answer and time.monotonic() < until:
                    try:   # the bridge may be up a moment before rpc-server listens
                        with socket.socket(socket.AF_UNIX) as conn:
                            conn.connect(str(path))
                            conn.sendall(b"tensor")
                            answer = conn.recv(64)
                    except OSError:
                        answer = b""
                    if not answer:
                        time.sleep(.1)
                self.assertEqual(answer, b"rpc:tensor", "the private socket reaches rpc-server")
                with self.assertRaises(OSError, msg="rpc-server is not on the host's loopback"):
                    socket.create_connection(("127.0.0.1", port), timeout=.5).close()
            finally:
                engine.close()
            self.assertFalse(path.exists(), "closing removes the bridge")

    def test_two_machines_pool_and_serve_through_rpc(self):
        result = run_scenario(real=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("PASS: stand-in engines", result.stdout)

    @unittest.skipUnless(REAL, "needs real llama.cpp RPC binaries and a model; the pool workflow supplies them")
    def test_two_machines_serve_a_real_model(self):
        result = run_scenario(real=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("PASS: real llama.cpp", result.stdout)


@unittest.skipUnless(sys.platform == "linux", "pool isolation runs on Linux")
class IsolationChoice(unittest.TestCase):
    """The probes are faked, so these prove the choice and the refusal without making a namespace."""

    def setUp(self):
        import openvibe_ai.pool_runtime as runtime
        self.runtime = runtime
        runtime._method = runtime._UNSET
        self.addCleanup(setattr, runtime, "_method", runtime._UNSET)

    def test_bwrap_is_chosen_first_when_both_work(self):
        probes = []

        def run(command, **_):
            probes.append(command)
            return subprocess.CompletedProcess(command, 0)

        with mock.patch.object(self.runtime.shutil, "which", return_value="/usr/bin/tool"), \
                mock.patch.object(self.runtime.subprocess, "run", side_effect=run):
            self.assertEqual(self.runtime.isolation_method(), "bwrap")
        self.assertIn("bwrap", probes[0])
        self.assertNotIn("unshare", probes[0])
        self.assertEqual(len(probes), 1, "bwrap works, so unshare is never probed")

    def test_neither_method_means_the_plain_refusal_not_the_host_network(self):
        def run(command, **_):
            return subprocess.CompletedProcess(command, 1)

        with mock.patch.object(self.runtime.shutil, "which", return_value="/usr/bin/tool"), \
                mock.patch.object(self.runtime.subprocess, "run", side_effect=run), \
                mock.patch.object(self.runtime.subprocess, "Popen") as popen:
            with self.assertRaises(RuntimeError) as refusal:
                self.runtime.Namespace({"role": "member", "rpc_binary": "/bin/true", "rpc_port": 1,
                                        "rpc_socket": "/nonexistent/rpc.sock"})
        self.assertEqual(str(refusal.exception), ISOLATION_HELP)
        self.assertIn("Install bubblewrap", str(refusal.exception))
        popen.assert_not_called()

    def test_unshare_is_used_when_bwrap_cannot_make_the_namespace(self):
        def run(command, **_):
            return subprocess.CompletedProcess(command, 0 if "unshare" in command else 1)

        with mock.patch.object(self.runtime.shutil, "which", return_value="/usr/bin/tool"), \
                mock.patch.object(self.runtime.subprocess, "run", side_effect=run):
            self.assertEqual(self.runtime.isolation_method(), "unshare")


if __name__ == "__main__":
    unittest.main()
