"""Machine pools in real Linux network namespaces: the engines' isolation and the whole two-machine flow.

Needs unprivileged user namespaces (on Ubuntu 24.04, kernel.apparmor_restrict_unprivileged_userns=0, which CI sets).
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

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
SCENARIO = HERE / "pool_linux_scenario.py"
REAL = all(os.environ.get(k) for k in ("POOL_RPC_BINARY", "POOL_LLAMA_BINARY", "POOL_MODEL"))


def namespaces_work():
    if sys.platform != "linux":
        return False
    try:
        return subprocess.run(["unshare", "--user", "--map-root-user", "--net", "ip", "link", "set", "lo", "up"],
                              capture_output=True, timeout=10).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


NAMESPACES = namespaces_work()


def run_scenario(real):
    env = dict(os.environ, PYTHONPATH=os.pathsep.join([str(HERE.parent), str(HERE.parent.parent / "sdk"), os.environ.get("PYTHONPATH", "")]))
    if not real:
        for key in ("POOL_RPC_BINARY", "POOL_LLAMA_BINARY", "POOL_MODEL"):
            env.pop(key, None)
    return subprocess.run(["unshare", "--user", "--map-root-user", "--net", sys.executable, str(SCENARIO)],
                          text=True, capture_output=True, timeout=240, env=env)


@unittest.skipUnless(NAMESPACES, "needs Linux with unprivileged user and network namespaces")
class Isolation(unittest.TestCase):
    def test_the_engine_never_runs_on_the_host_network(self):
        cfg = {"host_namespace": os.readlink("/proc/self/ns/net"), "role": "member"}
        result = subprocess.run([sys.executable, "-m", "openvibe_ai.pool_worker"], input=json.dumps(cfg) + "\n", text=True,
                                capture_output=True, timeout=30,
                                env=dict(os.environ, PYTHONPATH=os.pathsep.join([str(HERE.parent), str(HERE.parent.parent / "sdk")])))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("isolated network namespace", result.stderr)

    def test_rpc_server_is_reachable_only_through_its_private_socket(self):
        from openvibe_ai.pool_runtime import Namespace
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


if __name__ == "__main__":
    unittest.main()
