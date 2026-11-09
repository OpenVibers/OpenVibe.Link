"""Network reads for a machine pool (bot.machine-pool@1), with this machine's own Node pairing.

The node credential stays in the Node's credential file: it buys a short-lived node token (audience
openvibe.network) and never enters a pool document, a log line or the pool directory.
"""
import hashlib
import ipaddress
import json
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

# Network's answers that mean "this machine is no longer in this pool": signed out or another owner (401), not a
# member (403), the pool revoked or gone (404), its membership changed (409). Anything else (a timeout, a 5xx) is an
# outage: the pool pauses but keeps its key.
GONE = (401, 403, 404, 409)
RENEW_S = 2.0    # a member renews its readiness this often; Network's window is ten seconds


class Registry:
    def __init__(self, credential_file, timeout=3.0):
        self.path = Path(credential_file)
        self.timeout = timeout
        self.token, self.expires = None, 0
        self.pairing = None

    def credentials(self):
        return json.loads(self.path.read_text())

    def call(self, method, path, body=None):
        # Re-read every call to notice a sign-out, a removal or another owner on this machine.
        cred = self.credentials()
        pairing = hashlib.sha256(json.dumps(cred, sort_keys=True).encode()).digest()
        if pairing != self.pairing:
            self.expires = 0
            self.pairing = pairing
        network = str(cred.get('network') or '').rstrip('/')
        if not network.startswith('https://'):
            raise ValueError('pool registry requires an https:// Network (a Network-paired machine)')
        if time.monotonic() >= self.expires:
            data = urllib.parse.urlencode({'grant_type': 'client_credentials', 'client_id': cred['principal'],
                                           'client_secret': cred['link_credential'], 'audience': 'openvibe.network'}).encode()
            request = urllib.request.Request(network + '/oauth/token', data=data,
                                             headers={'Content-Type': 'application/x-www-form-urlencoded', 'Accept': 'application/json'})
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                token = json.loads(response.read(65536))
            self.token, self.expires = token['access_token'], time.monotonic() + min(int(token['expires_in']), 240)
        request = urllib.request.Request(network + '/api/v1/link/self/pools' + path,
                                         method=method, data=json.dumps(body).encode() if body is not None else None,
                                         headers={'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=self.timeout) as response:
            return json.loads(response.read(131072))

    def pool(self, pool_id):
        return self.call('GET', '/' + urllib.parse.quote(pool_id, safe=''))['pool']

    def renew(self, pool_id, ready):
        """Renew this machine's readiness; the answer carries the pool, so one request also rechecks membership."""
        return self.call('POST', '/' + urllib.parse.quote(pool_id, safe='') + '/status', {'ready': bool(ready)})['pool']

    def announce(self, address, port, memory):
        return self.call('POST', '/announce', dict(memory, address=str(address), port=int(port)))['machine']


class Membership:
    """The pool runtime's registry check: called every tick, it answers the current pool document, or None when this
    machine is no longer in the pool (its key was deleted locally, it was signed out, removed, or the pool revoked).
    Network is asked at most every RENEW_S; between renewals the last answer stands. An outage raises, so the runtime
    pauses (fails closed) without deleting the key."""

    def __init__(self, registry, pool_id, key_file, ready=lambda: False, interval=RENEW_S, clock=time.monotonic):
        self.registry, self.pool_id, self.key_file = registry, pool_id, Path(key_file)
        self.ready, self.interval, self.clock = ready, interval, clock
        self.last, self.doc = None, None

    def __call__(self):
        if not self.key_file.exists():
            return None
        if self.doc is not None and self.last is not None and self.clock() - self.last < self.interval:
            return self.doc
        try:
            try:
                doc = self.registry.renew(self.pool_id, self.ready())
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise
                # 403 from a renewal before this machine's own approval reached Network: read instead.
                doc = self.registry.pool(self.pool_id)
        except FileNotFoundError:
            return None      # the credential file is gone: signed out
        except urllib.error.HTTPError as error:
            if error.code in GONE:
                return None
            raise
        self.doc, self.last = doc, self.clock()
        return doc


def local_address(address=None):
    """This machine's private IPv4 address and prefix (such as 192.168.1.2/24), configured on one of its interfaces.
    Without an address, the source address of the default route. Never a public, loopback or wildcard address."""
    interfaces = json.loads(subprocess.check_output(['ip', '-j', 'address', 'show'], timeout=3))
    configured = [ipaddress.ip_interface('%s/%s' % (a['local'], a['prefixlen']))
                  for iface in interfaces for a in iface.get('addr_info', []) if a.get('family') == 'inet']
    if address is None:
        routes = json.loads(subprocess.check_output(['ip', '-j', 'route', 'get', '1.1.1.1'], timeout=3) or b'[]')
        source = routes[0].get('prefsrc') if routes else None
        found = [i for i in configured if str(i.ip) == source]
        if not found:
            raise ValueError('no private address on the default route; pass the address')
        chosen = found[0]
    else:
        chosen = ipaddress.ip_interface(address)
        if chosen not in configured:
            raise ValueError('address is not configured on this machine')
    from .pool import lan_address
    lan_address(str(chosen.ip))
    if not 8 <= chosen.network.prefixlen <= 30:
        raise ValueError('a pool needs a /8 to /30 private subnet')
    return chosen
