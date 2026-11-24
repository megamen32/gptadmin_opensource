#!/usr/bin/env python3
"""SpaceWeb A-record adapter; JSON stdin/stdout, no write retries.

Requires swebmimic with -single-attempt support. DNS propagation is separate
from provider acceptance. The provider does not supply an atomic CAS primitive.
"""
import argparse
from collections import Counter
import ipaddress
import json
import os
import subprocess
import sys


def pool_snapshot(relative, zone, rpc):
    response = rpc('info', {'domain': zone})
    rows = response.get('result') if isinstance(response, dict) else None
    if not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows):
        raise ValueError('provider_read_failed')
    records = {}
    others = Counter()
    for row in rows:
        rtype = row.get('type')
        owner = row.get('domain', row.get('name')) if rtype == 'TXT' else row.get('name')
        if not isinstance(owner, str):
            raise ValueError('invalid_provider_owner')
        exact = owner.rstrip('.').lower() in (relative, f'{relative}.{zone}')
        payload = json.dumps({k: v for k, v in row.items() if k != 'index'}, sort_keys=True)
        if exact and rtype == 'A':
            address = str(ipaddress.IPv4Address(row.get('value')))
            if address in records:
                raise ValueError('distinct_a_records_required')
            records[address] = payload
        else:
            if exact and rtype in ('CNAME', 'DNAME', 'NS', 'ALIAS', 'ANAME'):
                raise ValueError('incompatible_exact_record')
            others[payload] += 1
    return records, others


def public_relay_address(value):
    if not isinstance(value, str):
        raise ValueError('public_unicast_ipv4_required')
    address = ipaddress.IPv4Address(value)
    if not address.is_global or address.is_multicast:
        raise ValueError('public_unicast_ipv4_required')
    return str(address)


def append_a(relative, zone, value, rpc):
    """Append or create one A record at the configured exact owner."""
    value = public_relay_address(value)
    before, other_records = pool_snapshot(relative, zone, rpc)
    if value in before:
        return {'ok': True, 'value': value, 'changed': False}

    try:
        rpc('editMain', {'domain': zone, 'action': 'add', 'name': relative,
                         'type': 'A', 'value': value, 'prefix': ''})
    except Exception:
        # A lost response can follow a committed add. Observe before returning.
        pass
    try:
        after, after_others = pool_snapshot(relative, zone, rpc)
    except Exception:
        return {'ok': False, 'error': 'write_unresolved'}
    # Other writers can append to this pool between our two reads.
    if (value not in after or after_others != other_records
            or any(after.get(address) != record for address, record in before.items())):
        return {'ok': False, 'error': 'write_unresolved'}
    return {'ok': True, 'value': value, 'changed': True}


def execute(request, zone, name, rpc, *, allow_append_a=False):
    actions = ('read', 'compare_and_set')
    if allow_append_a:
        actions += ('append_a', 'read_a_pool')
    if (not isinstance(request, dict) or request.get('name') != name
            or not name.endswith('.' + zone) or request.get('action') not in actions):
        raise ValueError('invalid_request_or_name')
    relative = name[:-(len(zone) + 1)]
    if not relative or '*' in relative:
        raise ValueError('exact_record_required')
    if request['action'] == 'append_a':
        return append_a(relative, zone, request.get('value'), rpc)
    if request['action'] == 'read_a_pool':
        records, _ = pool_snapshot(relative, zone, rpc)
        return {'ok': True, 'values': sorted(records)}

    def read():
        response = rpc('info', {'domain': zone})
        rows = response.get('result') if isinstance(response, dict) else None
        if not isinstance(rows, list) or not all(isinstance(r, dict) for r in rows):
            raise ValueError('provider_read_failed')
        matches = [r for r in rows if r.get('name') == relative]
        if len(matches) != 1 or matches[0].get('type') != 'A':
            raise ValueError('existing_single_A_record_required')
        row = matches[0]
        ipaddress.IPv4Address(row.get('value'))
        others = sorted(json.dumps({k: v for k, v in r.items() if k != 'index'}, sort_keys=True)
                        for r in rows if r.get('name') != relative)
        return row, others

    before, other_records = read()
    if request['action'] == 'read':
        return {'ok': True, 'value': before['value']}
    expected = str(ipaddress.IPv4Address(request.get('expected')))
    desired = str(ipaddress.IPv4Address(request.get('value')))
    if before['value'] != expected:
        return {'ok': False, 'value': before['value'], 'error': 'record_conflict'}
    if before['value'] == desired:
        return {'ok': True, 'value': desired, 'changed': False}
    if type(before.get('index')) not in (int, str) or before['index'] == '':
        raise ValueError('record_index_missing')
    params = {'domain': zone, 'action': 'edit', 'name': relative, 'type': 'A',
              'value': desired, 'prefix': '', 'index': before['index']}
    # One write only. Its response cannot establish that a timeout means
    # rejection, so always read the actual record afterward.
    try:
        rpc('editMain', params)
    except Exception:
        pass
    after, after_others = read()
    if after_others != other_records:
        return {'ok': False, 'value': after['value'], 'error': 'other_records_changed'}
    return {'ok': after['value'] == desired, 'value': after['value'],
            'changed': after['value'] == desired,
            **({} if after['value'] == desired else {'error': 'write_unresolved'})}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--zone', default=os.environ.get('GPTADMIN_FRP_DNS_ZONE'))
    parser.add_argument('--name', default=os.environ.get('GPTADMIN_FRP_DNS_SPACEWEB_NAME'),
                        help='one allowed fully qualified hostname')
    parser.add_argument('--transport', default=os.environ.get('GPTADMIN_FRP_DNS_SPACEWEB_TRANSPORT')
                        or '/usr/local/bin/swebmimic')
    parser.add_argument('--creds', default=os.environ.get('GPTADMIN_FRP_DNS_SPACEWEB_CREDENTIALS_FILE'),
                        help='existing SpaceWeb credential file')
    parser.add_argument('--allow-append-a', action='store_true',
                        help='allow an additive write to the configured exact A pool')
    parser.add_argument('--rpc-timeout', type=int, default=25)
    args = parser.parse_args()
    if not args.zone or not args.name or not args.creds:
        parser.error('zone, exact name and credentials file are required')
    if not 1 <= args.rpc_timeout <= 90:
        parser.error('RPC timeout must be 1..90 seconds')
    raw = sys.stdin.buffer.read(4097)
    if len(raw) > 4096:
        raise ValueError('request_oversize')
    request = json.loads(raw)

    def rpc(method, params):
        result = subprocess.run([args.transport, '-single-attempt', '-creds', args.creds,
                                 '-timeout', str(args.rpc_timeout) + 's', '-method', method,
                                 '-params', json.dumps(params, separators=(',', ':'))],
                                capture_output=True, timeout=args.rpc_timeout + 3)
        if result.returncode != 0 or len(result.stdout) > 4 << 20:
            raise RuntimeError('provider_rpc_failed')
        response = json.loads(result.stdout)
        if not isinstance(response, dict) or response.get('error'):
            raise RuntimeError('provider_rpc_rejected')
        return response

    print(json.dumps(execute(request, args.zone.rstrip('.').lower(), args.name.rstrip('.').lower(), rpc,
                             allow_append_a=args.allow_append_a)), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Provider bodies, subprocess stderr and credentials never enter logs.
        print(json.dumps({'ok': False, 'error': type(error).__name__}), flush=True)
        sys.exit(1)
