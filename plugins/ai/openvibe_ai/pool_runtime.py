"""Local pool lifecycle (the pooled machines design, 2026-11-06). Registry loss, changed membership and any busy or
stopped member fail closed.

Every machine runs a PoolRuntime. A member opens the private link (pool.Tunnel) on its private address and, only while
the head says RUN and keeps calling, runs `rpc-server` inside its own network namespace. The head asks every member's
status each half second, pauses the whole pool when any member is busy (idle-only), stopped (the kill switch) or
unreachable, measures every link before it starts, and runs `llama-server --rpc` in its own namespace with the members
reached only through the authenticated link. Leaving (a removal, a sign-out, another owner, a revoked pool) closes the
link and deletes the key; a plain close (the Node stopping) keeps the key for the next start.
"""
import http.client
import ipaddress
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
from .pool import Identity, Tunnel, fit_table, probe, relay, validate_pool


def _tree(pid):
    """pid and its descendants, read from /proc (Linux); just pid elsewhere."""
    found, todo = set(), [pid]
    while todo:
        current = todo.pop()
        if current in found:
            continue
        found.add(current)
        for task in Path('/proc/%d/task' % current).glob('*'):
            try:
                todo.extend(int(x) for x in (task / 'children').read_text().split())
            except (OSError, ValueError):
                pass
    return found


class Namespace:
    def __init__(self, cfg):
        if sys.platform != 'linux':
            raise RuntimeError('pool isolation is currently supported on Linux only')
        cfg = dict(cfg, host_namespace=os.readlink('/proc/self/ns/net'))
        # setpriv --pdeathsig: the engine dies with the thread that started it, never outliving a crashed plugin.
        guard = ['setpriv', '--pdeathsig', 'KILL'] if shutil.which('setpriv') else []
        self.process = subprocess.Popen(guard + ['unshare', '--user', '--map-root-user', '--net', '--fork', '--kill-child',
                                         sys.executable, '-m', 'openvibe_ai.pool_worker'],
                                        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                        start_new_session=True)
        self.process.stdin.write((json.dumps(cfg) + '\n').encode())
        self.process.stdin.close()
        time.sleep(.15)
        if self.process.poll() is not None:
            raise RuntimeError('pool isolation failed; no host-network fallback')

    def close(self):
        if self.process.poll() is None:
            # This process group belongs exclusively to this namespace worker.
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(4)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait()


class Forwarder:
    """A private Unix socket on the head that the namespaced llama-server's --rpc reaches: each connection becomes a
    new authenticated link to one member's tunnel, which pins that member's certificate."""

    def __init__(self, path, identity, peer, address):
        self.path, self.identity, self.peer, self.address = Path(path), identity, peer, address
        self.stopped = threading.Event()
        self.listener = socket.socket(socket.AF_UNIX)
        self.listener.bind(str(path))
        os.chmod(path, 0o600)
        self.listener.listen(8)
        self.listener.settimeout(.2)
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def run(self):
        while not self.stopped.is_set():
            conn = remote = None
            try:
                conn, _ = self.listener.accept()
                remote = self.identity.connect(self.address, self.peer['certificate'])
                remote.sendall(b'RPC\n')
                if remote.recv(3) != b'OK\n':
                    raise OSError('member refused the tensor link')
                threading.Thread(target=relay, args=(conn, remote, self.stopped), daemon=True).start()
            except socket.timeout:
                continue
            except OSError:
                # Only this attempt's sockets: a relay already running keeps its own.
                for sock in (conn, remote):
                    if sock is not None:
                        sock.close()

    def close(self):
        self.stopped.set()
        self.listener.close()
        self.thread.join(1)
        self.path.unlink(missing_ok=True)


class PoolRuntime:
    def __init__(self, doc, machine, directory, config, busy, registry_check, namespace_factory=Namespace):
        head = validate_pool(doc)
        self.doc, self.machine, self.config = doc, machine, config
        self.me = next(m for m in doc['machines'] if m['id'] == machine)
        self.head = head
        self.directory = Path(directory)
        self.identity = Identity(self.directory / 'identity')
        if self.identity.certificate != self.me['certificate']:
            raise ValueError('local approval does not match this membership')
        self.busy, self.registry_check, self.factory = busy, registry_check, namespace_factory
        self.stopped = threading.Event()
        self.worker, self.tunnel, self.links = None, None, []
        self.paused = True
        self.lock = threading.RLock()
        self.measurements = []
        self.head_running = False
        self.last_error = None

    def status(self):
        return {'ready': not self.stopped.is_set() and not self.busy() and
                self.worker is not None and self.worker.process.poll() is None,
                'paused': self.paused, 'available': not self.busy() and not self.stopped.is_set(), 'pool': self.doc['id']}

    def _peer_address(self, peer):
        """Where a peer's private link listens: its private address and port."""
        return (str(ipaddress.ip_interface(peer['address']).ip), peer['port'])

    def _measure(self, peer):
        """The head's direct link probe to one member (round trips and throughput), over the authenticated link."""
        return probe(self.identity, self.me, peer, self._peer_address(peer))

    def _make_tunnel(self):
        """A member's private link on its own private address; only the head's pinned certificate gets in."""
        return Tunnel(self.identity, self.head['certificate'], *self._peer_address(self.me),
                      status=self.status, rpc_socket=self.directory / 'rpc.sock', activate=self.activate)

    def start(self, watch=True):
        if self.machine != self.doc['head']:
            self.tunnel = self._make_tunnel()
            self.tunnel.start()
        if watch:
            self.thread = threading.Thread(target=self.watch, daemon=True)
            self.thread.start()

    def activate(self, running):
        self.head_running = running
        if not running:
            with self.lock:
                self.paused = True
                self.pause()

    def tell_members(self, running):
        """RUN or PAUSE to every member. A RUN that a member does not acknowledge raises (the pool stays paused); a
        PAUSE goes to every member it can reach, and one it cannot pauses itself within three seconds of silence."""
        failed = None
        for peer in self.doc["machines"]:
            if peer["id"] == self.machine:
                continue
            try:
                with self.identity.connect(self._peer_address(peer), peer["certificate"]) as conn:
                    conn.sendall(b"RUN\n" if running else b"PAUSE\n")
                    if conn.recv(3) != b"OK\n":
                        raise OSError("member did not acknowledge pool state")
            except OSError as error:
                failed = failed or error
                if running:
                    raise
        if failed and running:
            raise failed

    def halt(self):
        """The kill switch on this machine: stop its part now, before the next tick (the head also pauses every
        member). The busy check keeps it stopped until the latch is cleared."""
        with self.lock:
            self.paused = True
            self.pause()
        if self.machine == self.doc["head"]:
            self.tell_members(False)

    def tick(self):
        with self.lock:
            if self.stopped.is_set():
                return
            # The authoritative record (renewed every two seconds) checks ownership, revocation and membership.
            current = self.registry_check()
            if current is None or current != self.doc:
                self._leave_locked()
                return
            if self.worker and self.worker.process.poll() is not None:
                self.last_error = "isolated engine exited"
                self.pause()
            busy = bool(self.busy())
            all_ready = True
            if self.machine == self.doc['head']:
                for peer in self.doc['machines']:
                    if peer['id'] == self.machine:
                        continue
                    with self.identity.connect(self._peer_address(peer), peer['certificate']) as conn:
                        conn.sendall(b'STATUS\n')
                        raw = conn.makefile('rb').readline(4097)
                        state = json.loads(raw)
                        busy = busy or state.get('available') is not True or state.get('pool') != self.doc['id']
                        all_ready = all_ready and state.get('ready') is True
                self.tell_members(not busy)
            elif not self.head_running or (self.tunnel and time.monotonic() - self.tunnel.last_head > 3):
                busy = True
            self.paused = busy or (self.machine == self.doc["head"] and not all_ready)
            if busy:
                self.pause()
            elif self.worker is None and all_ready:
                self.launch()

    def watch(self):
        while not self.stopped.is_set():
            try:
                self.tick()
            except Exception as error:
                self.last_error = type(error).__name__ + ": " + str(error)
                # Registry or peer unavailable: no cached permission to keep serving.
                with self.lock:
                    self.paused = True
                    self.pause()
                    if self.machine == self.doc["head"]:
                        try:
                            self.tell_members(False)
                        except OSError:
                            pass
            self.stopped.wait(.5)

    def launch(self):
        cfg = dict(self.config, role='head' if self.machine == self.doc['head'] else 'member',
                   rpc_port=50052, rpc_socket=str(self.directory / 'rpc.sock'), api_socket=str(self.directory / 'api.sock'))
        if cfg['role'] == 'head':
            peers = [m for m in self.doc['machines'] if m['id'] != self.machine]
            self.measurements = [self._measure(m) for m in peers]
            if not fit_table(self.doc['machines'], self.measurements, [self.config['model']], [])[0]['fits']:
                raise ValueError('the measured links or memory do not fit this model')
            try:
                for i, peer in enumerate(peers):
                    self.links.append(Forwarder(self.directory / ('link%d.sock' % i), self.identity, peer, self._peer_address(peer)))
                cfg['links'] = [str(link.path) for link in self.links]
                self.worker = self.factory(cfg)
            except BaseException:
                self.pause()
                raise
        else:
            self.worker = self.factory(cfg)

    def pause(self):
        if self.worker:
            self.worker.close()
            self.worker = None
        for link in self.links:
            link.close()
        self.links = []
        for path in ('rpc.sock', 'api.sock'):
            (self.directory / path).unlink(missing_ok=True)

    def close(self):
        """Stop the engines and close the link, keeping the key: the Node is stopping, not leaving the pool."""
        with self.lock:
            self.stopped.set()
            self.paused = True
            if self.tunnel:
                self.tunnel.close()
            self.pause()

    def leave(self):
        """Leave the pool: close the link and delete this machine's key at once."""
        with self.lock:
            self._leave_locked()

    def _leave_locked(self):
        self.stopped.set()
        self.paused = True
        if self.tunnel:
            self.tunnel.close()
        self.pause()
        self.identity.revoke()

    def engine_pids(self):
        """The process ids of this machine's pool engine (the namespace and everything under it), empty when none
        runs: idle-only does not count the pool's own GPU use as the owner's."""
        worker = self.worker
        process = getattr(worker, 'process', None)
        if process is None or process.poll() is not None:
            return set()
        return _tree(process.pid)

    def opener(self, request, timeout=30):
        if self.paused or self.stopped.is_set() or self.machine != self.doc['head']:
            raise OSError('pool is paused')
        conn = http.client.HTTPConnection('localhost', timeout=timeout)
        conn.sock = socket.socket(socket.AF_UNIX)
        conn.sock.settimeout(timeout)
        conn.sock.connect(str(self.directory / 'api.sock'))
        url = urllib.parse.urlsplit(request.full_url)
        conn.request(request.get_method(), url.path, body=request.data, headers=dict(request.header_items()))
        response = conn.getresponse()
        if response.status >= 400:
            raise urllib.error.HTTPError(request.full_url, response.status, 'pool model server refused request', response.headers, response)
        return response
