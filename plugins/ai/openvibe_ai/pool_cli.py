"""A machine pool on this machine: python -m openvibe_ai.pool_cli (never reachable through an ai command).

    announce   put this machine on its owner's "Pool these machines" list (the plugin also does it every minute)
    approve    show a proposed pool, ask the person at this machine, make this machine's key and send its public
               certificate (the private key never leaves the machine)
    fit        on the approved head: measure every link and say which models fit (measured speeds only)
    leave      delete this machine's key at once, even offline, then tell Network (which revokes the whole pool)
"""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
from .pool import Identity, fit_table, probe, validate_pool
from .pool_registry import Registry, local_address


def memory():
    ram = os.sysconf('SC_AVPHYS_PAGES') * os.sysconf('SC_PAGE_SIZE') // (1024 * 1024)
    vram = 0
    try:
        data = subprocess.check_output(['nvidia-smi', '--query-gpu=memory.free', '--format=csv,noheader,nounits'], timeout=2)
        vram = sum(int(x) for x in data.decode().splitlines())
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    amd_devices = set()
    for card in Path('/sys/class/drm').glob('card*/device'):
        device = card.resolve()
        if device in amd_devices:
            continue
        amd_devices.add(device)
        try:
            total = int((device / 'mem_info_vram_total').read_text())
            used = int((device / 'mem_info_vram_used').read_text())
            vram += max(0, total - used) // (1024 * 1024)
        except (OSError, ValueError):
            pass
    return {'free_ram': ram, 'free_vram': vram, 'hardware': platform.machine()}


def main():
    if sys.platform != "linux":
        raise RuntimeError("pool isolation currently requires Linux")
    parser = argparse.ArgumentParser(description='Approve only your own same-network machines. Keys stay on this machine.')
    parser.add_argument('action', choices=['announce', 'approve', 'fit', 'leave'])
    parser.add_argument('--credential-file', required=True)
    parser.add_argument('--directory', required=True)
    parser.add_argument('--pool')
    parser.add_argument('--models', help='local JSON array of model, parameters_b and measured memory_mb')
    parser.add_argument('--benchmarks', help='optional local JSON array of dated, sourced measured speeds')
    parser.add_argument('--address', help='this machine private IPv4 address/prefix, such as 192.168.1.2/24 (default: the default route)')
    parser.add_argument('--port', type=int, default=50053)
    args = parser.parse_args()
    registry = Registry(args.credential_file)
    directory = Path(args.directory).resolve()
    if args.action == 'announce':
        # The address must be configured on one of this machine's interfaces, not just a claimed subnet.
        result = registry.announce(local_address(args.address), args.port, memory())
        print(json.dumps(result))  # the machine id, its addresses and free memory; never a credential
    elif args.action == 'fit':
        doc = registry.pool(args.pool)
        head = validate_pool(doc)
        identity = Identity(directory / 'identity')
        if identity.certificate != head['certificate']:
            raise ValueError('run the fit measurement on the approved head')
        links = [probe(identity, head, m) for m in doc['machines'] if m['id'] != doc['head']]
        models = json.loads(Path(args.models).read_text())
        benchmarks = json.loads(Path(args.benchmarks).read_text()) if args.benchmarks else []
        print(json.dumps({'links': links, 'fit': fit_table(doc['machines'], links, models, benchmarks)}, indent=2))
    elif args.action == 'approve':
        doc = registry.pool(args.pool)
        print('Pool %s; owner %s; head %s; members %s' %
              (doc['id'], doc['owner'], doc['head'], ', '.join(m['id'] for m in doc['machines'])))
        print('The head will see prompts; members receive tensors. Type approve on this machine:')
        if input().strip() != 'approve':
            raise ValueError('local approval declined')
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        identity = Identity.approve(directory / 'identity')
        try:
            registry.call('POST', '/' + args.pool + '/approve', {'certificate': identity.certificate})
            (directory / 'approval.json').write_text(json.dumps({'pool_id': args.pool, 'credential_file': str(Path(args.credential_file).resolve()), 'owner': doc['owner'], 'head': doc['head'], 'machines': sorted(m['id'] for m in doc['machines'])}))
            os.chmod(directory / 'approval.json', 0o600)
        except BaseException:
            identity.revoke()
            raise
        print('Approved. Enable pool_directory in the local ai plugin configuration.')
    else:
        # Local revocation first, even when Network is unreachable: the running plugin sees the key gone at its next
        # tick and closes the private link.
        import shutil
        shutil.rmtree(directory / 'identity', ignore_errors=True)
        (directory / 'approval.json').unlink(missing_ok=True)
        registry.call('POST', '/' + args.pool + '/leave', {})
        print('Left pool %s. This machine\'s key is deleted.' % args.pool)


if __name__ == '__main__':
    main()
