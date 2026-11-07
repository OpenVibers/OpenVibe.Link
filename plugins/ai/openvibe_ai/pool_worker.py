"""Runs ONLY inside a private user/network namespace (bubblewrap or unshare). No remotely chosen commands."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import threading
import time
from .pool import relay


def bridge(listener, target, unix, stopped):
    listener.settimeout(.2)
    while not stopped.is_set():
        try:
            conn, _ = listener.accept()
            other = socket.socket(socket.AF_UNIX if unix else socket.AF_INET)
            other.settimeout(5)
            other.connect(target)
            threading.Thread(target=relay, args=(conn, other, stopped), daemon=True).start()
        except socket.timeout:
            continue
        except OSError:
            if 'conn' in locals():
                conn.close()


def main():
    cfg = json.loads(sys.stdin.readline())
    # Never accidentally run the worker on the host network, even when called directly.
    if os.readlink('/proc/self/ns/net') == cfg['host_namespace']:
        raise RuntimeError('RPC requires an isolated network namespace')
    # bwrap brings the namespace's loopback up itself (and the engine cannot add capabilities on top); unshare needs
    # this and grants the capability. Best effort either way, then the engine's own bind is the real test.
    try:
        subprocess.run(['ip', 'link', 'set', 'lo', 'up'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except OSError:
        pass
    stopped = threading.Event()
    listeners = []
    def unix_bridge(path, target):
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(path)
        os.chmod(path, 0o600)
        listener.listen(8)
        listeners.append(listener)
        threading.Thread(target=bridge, args=(listener, target, False, stopped), daemon=True).start()
    if cfg['role'] == 'member':
        command = [cfg['rpc_binary'], '--host', '127.0.0.1', '--port', str(cfg['rpc_port'])]
        unix_bridge(cfg['rpc_socket'], ('127.0.0.1', cfg['rpc_port']))
    else:
        endpoints = []
        for i, path in enumerate(cfg['links']):
            listener = socket.socket()
            listener.bind(('127.0.0.1', 0))
            listener.listen(8)
            listeners.append(listener)
            endpoints.append('127.0.0.1:%s' % listener.getsockname()[1])
            threading.Thread(target=bridge, args=(listener, path, True, stopped), daemon=True).start()
        command = [cfg['llama_binary'], '--host', '127.0.0.1', '--port', '8080', '--model', cfg['model_path'],
                   '--alias', cfg['model']['model'], '--rpc', ','.join(endpoints), '--n-gpu-layers', '999', '--ctx-size', str(cfg.get('context', 2048))]
        unix_bridge(cfg['api_socket'], ('127.0.0.1', 8080))
    # RPC's optional RDMA negotiation must not bypass TLS. Do not enable its disk cache.
    child = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             env=dict(os.environ, GGML_RPC_NO_RDMA='1'))
    def stop(*_):
        stopped.set()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        while not stopped.wait(.2) and child.poll() is None:
            pass
    finally:
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        for listener in listeners:
            listener.close()
        for key in ('rpc_socket', 'api_socket'):
            if cfg.get(key):
                Path(cfg[key]).unlink(missing_ok=True)
    return child.returncode or 0


if __name__ == '__main__':
    sys.exit(main())
