"""Two machines pool and serve a model through llama.cpp RPC, on a throwaway local network.

Run by test_pool_linux.py inside a private user/network namespace (bubblewrap or unshare): that namespace is the
"LAN" (192.168.77.2 and .3 on its loopback), and each machine's engine runs in a nested namespace of its own, exactly
as on a real machine. With POOL_RPC_BINARY, POOL_LLAMA_BINARY and POOL_MODEL it runs real llama.cpp (the pool
workflow); without them, stand-in engines that speak TCP and HTTP the same way, so the transport, the isolation and
the pool's rules are checked on every run. Prints one PASS line.
"""
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from openvibe_ai.pool import Identity  # noqa: E402
from openvibe_ai.pool_runtime import PoolRuntime  # noqa: E402

FAKE_RPC = r'''
import socket, sys, threading
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
server = socket.socket(); server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
server.bind((args['--host'], int(args['--port']))); server.listen(8)
def serve(conn):
    with conn:
        while True:
            data = conn.recv(65536)
            if not data:
                return
            conn.sendall(b'tensor-ok:' + data)
while True:
    conn, _ = server.accept()
    threading.Thread(target=serve, args=(conn,), daemon=True).start()
'''

FAKE_LLAMA = r'''
import http.server, json, socket, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
members = [x.rsplit(':', 1) for x in args['--rpc'].split(',')]
class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length') or 0))
        replies = []
        for host, port in members:
            with socket.create_connection((host, int(port)), timeout=5) as conn:
                conn.sendall(b'layer-17')
                replies.append(conn.recv(64).decode())
        body = json.dumps({'content': 'Once upon a time', 'rpc': replies}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self, *a):
        pass
http.server.HTTPServer((args['--host'], int(args['--port'])), Handler).serve_forever()
'''


def wait(check, seconds=60, what='pool condition'):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            if check():
                return
        except (OSError, ValueError, ssl.SSLError):
            pass
        time.sleep(.2)
    raise AssertionError('%s did not become true' % what)


def refused(address):
    try:
        socket.create_connection(address, timeout=.3).close()
    except OSError:
        return True
    return False


def engines(root):
    if all(os.environ.get(k) for k in ('POOL_RPC_BINARY', 'POOL_LLAMA_BINARY', 'POOL_MODEL')):
        return 'real llama.cpp', {'rpc_binary': os.environ['POOL_RPC_BINARY'], 'llama_binary': os.environ['POOL_LLAMA_BINARY'],
                                  'model_path': os.environ['POOL_MODEL'], 'context': 256,
                                  'model': {'model': 'stories15M', 'parameters_b': .015, 'memory_mb': 100}}
    paths = {}
    for name, source in (('rpc', FAKE_RPC), ('llama', FAKE_LLAMA)):
        path = Path(root) / ('fake-%s' % name)
        path.write_text('#!%s\n%s' % (sys.executable, source))
        path.chmod(0o755)
        paths[name] = str(path)
    return 'stand-in engines', {'rpc_binary': paths['rpc'], 'llama_binary': paths['llama'], 'model_path': '/dev/null',
                                'context': 256, 'model': {'model': 'stand-in', 'parameters_b': .015, 'memory_mb': 100}}


def main():
    subprocess.run(['ip', 'link', 'set', 'lo', 'up'], check=True)
    for address in ('192.168.77.2/24', '192.168.77.3/24'):
        subprocess.run(['ip', 'address', 'add', address, 'dev', 'lo'], check=True)
    with tempfile.TemporaryDirectory(prefix='ov-pool-') as root:
        mode, cfg = engines(root)
        paths = [Path(root) / x for x in ('head', 'member')]
        identities = [Identity.approve(p / 'identity') for p in paths]
        stranger = Identity.approve(Path(root) / 'stranger' / 'identity')
        owner = 'usr_01J8Z3Q4R5S6T7V8W9X0Y1Z2A1'
        members = [{'id': 'dev_01J8Z3Q4R5S6T7V8W9X0Y1Z2D%d' % (i + 1), 'owner': owner, 'public_ip': '203.0.113.1',
                    'address': '192.168.77.%d/24' % (i + 2), 'port': 50053, 'certificate': identities[i].certificate,
                    'free_vram': 0, 'free_ram': 2048, 'hardware': 'test-cpu'} for i in range(2)]
        doc = {'id': 'pool_01J8Z3Q4R5S6T7V8W9X0Y1Z2P1', 'owner': owner, 'head': members[0]['id'], 'machines': members}
        busy = [False, False]
        runtimes = [PoolRuntime(doc, members[i]['id'], paths[i], cfg, lambda i=i: busy[i], lambda: doc) for i in range(2)]
        head, member = runtimes
        serving = lambda: head.worker is not None and not head.paused and member.worker is not None  # noqa: E731
        paused = lambda: head.paused and head.worker is None and member.worker is None  # noqa: E731
        try:
            member.start()
            head.start()
            try:
                wait(serving, what='the pool serving')
            except AssertionError:
                raise AssertionError('head=%s member=%s measurements=%s' % (head.last_error, member.last_error, head.measurements))

            # rpc-server is reachable only through the tunnel: not on the LAN address, not on loopback.
            for address in (('192.168.77.3', 50052), ('127.0.0.1', 50052), ('192.168.77.2', 8080), ('127.0.0.1', 8080)):
                assert refused(address), 'an engine port escaped its namespace: %s:%d' % address
            # The member's tunnel takes only the head: a stranger's key and a plain connection are refused.
            # (TLS 1.3: the client's side of the handshake ends before the member checks its certificate, so the
            # refusal arrives on the first read.)
            try:
                with stranger.connect(('192.168.77.3', 50053), identities[1].certificate) as conn:
                    conn.sendall(b'STATUS\n')
                    answer = conn.recv(64)
            except (ssl.SSLError, OSError):
                answer = b''
            assert answer == b'', 'a stranger entered the tunnel: %r' % answer
            with socket.create_connection(('192.168.77.3', 50053), timeout=2) as plain:
                plain.sendall(b'RPC\n')
                plain.settimeout(4)
                try:
                    assert plain.recv(16) == b'', 'plain TCP got an answer'
                except (ConnectionResetError, socket.timeout):
                    pass

            def inference():
                request = urllib.request.Request('http://localhost/completion', data=json.dumps({'prompt': 'Once upon a time', 'n_predict': 4}).encode(),
                                                 headers={'Content-Type': 'application/json'})
                with head.opener(request, timeout=30) as response:
                    result = json.loads(response.read())
                assert result.get('content'), result
                if 'rpc' in result:
                    assert result['rpc'] == ['tensor-ok:layer-17'], result
                return True
            wait(inference, what='an answer through the pool')
            assert member.tunnel.rpc_connections > 0, 'inference never entered the member tunnel'

            # Idle-only on one member pauses the whole pool, and it resumes when the member is idle again.
            busy[1] = True
            wait(paused, 10, 'the pool pausing for a busy member')
            busy[1] = False
            wait(serving, what='the pool resuming')
            # The kill switch on a member stops the pool at once.
            busy[1] = True
            member.halt()
            wait(paused, 5, 'the pool stopping for the kill switch')
            busy[1] = False
            wait(serving, what='the pool resuming after the latch is cleared')

            # Leaving drops the tunnel and the key; the head stops.
            member.leave()
            wait(lambda: head.paused and head.worker is None, 10, 'the head stopping after a member left')
            assert not (paths[1] / 'identity').exists(), 'the key outlived the membership'
            assert refused(('192.168.77.3', 50053)), 'the tunnel outlived the membership'
            print('PASS: %s: inference through the tunnel, engines isolated, only the head admitted, idle-only and the '
                  'kill switch pause the pool, leaving drops the tunnel and the key' % mode)
        finally:
            for runtime in runtimes:
                runtime.close()


if __name__ == '__main__':
    main()
