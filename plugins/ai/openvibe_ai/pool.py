"""Private RPC transport. The TLS listener never launches a command supplied by a peer.

Raw RPC lives in a separate Linux user/network namespace with only loopback. A
mode-0700 directory holds the Unix bridge and the locally generated identity.
Only the pinned, locally approved head certificate can enter a member's tunnel.
"""
import hashlib
import ipaddress
import json
import math
import os
from pathlib import Path
import select
import shutil
import socket
import ssl
import subprocess
import threading
import time

PRIVATE = tuple(ipaddress.ip_network(x) for x in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))
PROBE_BYTES = 16 * 1024 * 1024


def lan_address(value):
    address = ipaddress.ip_address(value)
    if not any(address in net for net in PRIVATE):
        raise ValueError("pool links require a literal RFC1918 address")
    return str(address)


def same_network(a, b):
    try:
        left, right = ipaddress.ip_interface(a["address"]), ipaddress.ip_interface(b["address"])
        lan_address(str(left.ip))
        lan_address(str(right.ip))
        return (a["public_ip"] == b["public_ip"] and bool(a["public_ip"]) and
                left.network == right.network and left.ip != right.ip)
    except (KeyError, ValueError, TypeError):
        return False


def validate_pool(doc):
    members = doc.get("machines", [])
    ids = [m.get("id") for m in members]
    if not 2 <= len(members) <= 8 or len(set(ids)) != len(ids) or doc.get("head") not in ids:
        raise ValueError("a pool needs 2-8 distinct machines and a member head")
    if not doc.get("owner") or any(m.get("owner") != doc["owner"] for m in members):
        raise ValueError("all machines must belong to the pool owner")
    head = next(m for m in members if m["id"] == doc["head"])
    for member in members:
        if not member.get("certificate"):
            raise ValueError("every machine must approve locally")
        ssl.PEM_cert_to_DER_cert(member["certificate"])
        if member != head and not same_network(head, member):
            raise ValueError("members must share a public address and private subnet")
    certificates = [m["certificate"] for m in members]
    if len(set(certificates)) != len(certificates):
        raise ValueError("each approval needs its own identity")
    return head


def fit_table(machines, links, models, benchmarks):
    """Only exact measured hardware/model matches have speed numbers. MB is used throughout."""
    result = []
    for model in models:
        threshold = 1000 if model["parameters_b"] > 30 else 300
        good_links = len(links) == len(machines) - 1 and all(
            math.isfinite(x["rtt_ms"]) and 0 <= x["rtt_ms"] < 5 and
            math.isfinite(x["mbps"]) and x["mbps"] >= threshold for x in links)
        memory = sum(m["free_vram"] + m["free_ram"] for m in machines)
        key = sorted(str(m.get("hardware", "unknown")) for m in machines)
        measured = next((b for b in benchmarks if b["model"] == model["model"] and
                         b["hardware"] == key and b.get("source") and b.get("measured_at")), None)
        result.append({"model": model["model"], "fits": bool(good_links and memory >= model["memory_mb"]),
                       "free_vram_mb": sum(m["free_vram"] for m in machines),
                       "free_ram_mb": sum(m["free_ram"] for m in machines),
                       "tokens_per_second": measured["tokens_per_second"] if measured else None,
                       "measurement": measured})
    return result


class Identity:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.certificate = (self.directory / "certificate.pem").read_text()

    @classmethod
    def approve(cls, directory):
        """Called only after a local approval. Never returns or logs the private key."""
        directory = Path(directory)
        directory.mkdir(mode=0o700, parents=True, exist_ok=False)
        try:
            # openssl writes the private key itself; the umask protects it from creation.
            subprocess.run(["openssl", "req", "-x509", "-newkey", "ed25519", "-nodes", "-days", "30",
                            "-subj", "/CN=OpenVibe-pool", "-keyout", str(directory / "key.pem"),
                            "-out", str(directory / "certificate.pem")], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, umask=0o077)
            return cls(directory)
        except BaseException:
            shutil.rmtree(directory)
            raise

    def context(self, peer, server=False):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER if server else ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_3
        if not server:
            context.check_hostname = False  # identity is the exact certificate, not a DNS name
        context.verify_mode = ssl.CERT_REQUIRED
        context.load_verify_locations(cadata=peer)
        context.load_cert_chain(str(self.directory / "certificate.pem"), str(self.directory / "key.pem"))
        return context

    def connect(self, address, peer):
        raw = socket.create_connection(address, timeout=3)
        raw.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        try:
            conn = self.context(peer).wrap_socket(raw, server_hostname=None)
            verify_pin(conn, peer)
            return conn
        except BaseException:
            raw.close()
            raise

    def revoke(self):
        shutil.rmtree(self.directory, ignore_errors=True)


def verify_pin(conn, certificate):
    expected = hashlib.sha256(ssl.PEM_cert_to_DER_cert(certificate)).digest()
    actual = hashlib.sha256(conn.getpeercert(binary_form=True)).digest()
    if actual != expected:
        raise ssl.SSLError("unapproved pool peer")


def relay(left, right, stopped):
    """Bounded bidirectional tensor transport; close both ends on stop or loss."""
    try:
        while not stopped.is_set():
            readable, _, _ = select.select([left, right], [], [], .2)
            for source in readable:
                target = right if source is left else left
                data = source.recv(65536)
                if not data:
                    return
                target.sendall(data)
                # A TLS socket may hold decrypted bytes select() cannot see.
                while getattr(source, "pending", lambda: 0)():
                    data = source.recv(65536)
                    if not data:
                        return
                    target.sendall(data)
    except (OSError, ValueError):
        pass
    finally:
        left.close()
        right.close()


class Tunnel:
    def __init__(self, identity, head_certificate, address, port, status, rpc_socket=None, test_loopback=False, activate=None):
        if not (test_loopback and address == "127.0.0.1"):
            lan_address(address)
        self.identity, self.head_certificate = identity, head_certificate
        self.address, self.status, self.rpc_socket = (address, port), status, rpc_socket
        self.activate = activate
        self.context = identity.context(head_certificate, server=True)
        self.stopped = threading.Event()
        self.sessions = set()
        self.lock = threading.Lock()
        self.listener = None
        self.last_head = time.monotonic()
        self.slots = threading.BoundedSemaphore(16)
        self.rpc_connections = 0

    def start(self):
        self.listener = socket.socket()
        self.listener.bind(self.address)
        self.listener.listen(8)
        self.listener.settimeout(.2)
        self.address = self.listener.getsockname()
        self.thread = threading.Thread(target=self._accept, daemon=True)
        self.thread.start()

    def _accept(self):
        while not self.stopped.is_set():
            try:
                conn, _ = self.listener.accept()
                if not self.slots.acquire(blocking=False):
                    conn.close()
                    continue
                with self.lock:
                    self.sessions.add(conn)
                threading.Thread(target=self._serve, args=(conn,), daemon=True).start()
            except socket.timeout:
                continue
            except OSError:
                break

    def _serve(self, raw):
        conn = raw
        try:
            raw.settimeout(3)
            raw.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            conn = self.context.wrap_socket(raw, server_side=True)
            with self.lock:
                self.sessions.discard(raw)
                self.sessions.add(conn)
            verify_pin(conn, self.head_certificate)
            operation = b""
            while len(operation) < 16 and not operation.endswith(b"\n"):
                chunk = conn.recv(1)
                if not chunk:
                    return
                operation += chunk
            self.last_head = time.monotonic()
            if operation in (b"RUN\n", b"PAUSE\n") and self.activate:
                self.activate(operation == b"RUN\n")
                conn.sendall(b"OK\n")
            elif operation == b"STATUS\n":
                conn.sendall(json.dumps(self.status()).encode() + b"\n")
            elif operation == b"PING\n":
                conn.sendall(b"PONG\n")
            elif operation == b"PROBE\n":
                block = bytes(65536)
                for _ in range(PROBE_BYTES // len(block)):
                    conn.sendall(block)
            elif operation == b"RPC\n" and self.rpc_socket and self.status().get("ready"):
                local = socket.socket(socket.AF_UNIX)
                local.settimeout(3)
                local.connect(str(self.rpc_socket))
                self.rpc_connections += 1
                conn.sendall(b"OK\n")
                conn.settimeout(5)
                relay(conn, local, self.stopped)
        except (OSError, ValueError):
            pass
        finally:
            with self.lock:
                self.sessions.discard(raw)
                self.sessions.discard(conn)
            conn.close()
            self.slots.release()

    def close(self):
        self.stopped.set()
        if self.listener:
            self.listener.close()
        with self.lock:
            for conn in list(self.sessions):
                try:
                    conn.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                conn.close()
        if hasattr(self, "thread"):
            self.thread.join(1)


def probe(identity, source, target, address=None):
    """Measure the link to one member before serving: three round trips and a 16 MB transfer over the authenticated
    link. A machine off this local network (another public address or subnet) is refused before any connection."""
    if not same_network(source, target):
        raise ValueError("off-network machine refused before connection")
    address = address or (str(ipaddress.ip_interface(target["address"]).ip), target["port"])
    rtts = []
    for _ in range(3):
        with identity.connect(address, target["certificate"]) as conn:
            start = time.monotonic()
            conn.sendall(b"PING\n")
            if conn.recv(5) != b"PONG\n":
                raise ValueError("link probe failed")
            rtts.append((time.monotonic() - start) * 1000)
    with identity.connect(address, target["certificate"]) as conn:
        start, received = time.monotonic(), 0
        conn.sendall(b"PROBE\n")
        while received < PROBE_BYTES:
            block = conn.recv(min(65536, PROBE_BYTES - received))
            if not block:
                raise ValueError("incomplete link probe")
            received += len(block)
    return {"machine": target["id"], "rtt_ms": max(rtts), "mbps": received * 8 / (time.monotonic() - start) / 1e6}
