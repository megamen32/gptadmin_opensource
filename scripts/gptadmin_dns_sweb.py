#!/usr/bin/env python3
"""Single-writer SpaceWeb A-record adapter; JSON stdin/stdout, no write retries.

Requires swebmimic with -single-attempt support. DNS propagation is separate
from provider acceptance. The provider does not supply an atomic CAS primitive.
"""
import argparse
import ipaddress
import json
import subprocess
import sys


def execute(request, zone, name, rpc):
    if (not isinstance(request, dict) or request.get('name') != name
            or not name.endswith('.' + zone) or request.get('action') not in ('read', 'compare_and_set')):
        raise ValueError('invalid_request_or_name')
    relative = name[:-(len(zone) + 1)]
    if not relative or '*' in relative:
        raise ValueError('exact_record_required')

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
    parser.add_argument('--zone', required=True)
    parser.add_argument('--name', required=True, help='one allowed fully qualified hostname')
    parser.add_argument('--transport', default='/usr/local/bin/swebmimic')
    parser.add_argument('--creds', required=True, help='existing SpaceWeb credential file')
    parser.add_argument('--rpc-timeout', type=int, default=25)
    args = parser.parse_args()
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

    print(json.dumps(execute(request, args.zone.rstrip('.'), args.name.rstrip('.'), rpc)), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Provider bodies, subprocess stderr and credentials never enter logs.
        print(json.dumps({'ok': False, 'error': type(error).__name__}), flush=True)
        sys.exit(1)
