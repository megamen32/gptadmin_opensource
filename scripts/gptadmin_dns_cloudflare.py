#!/usr/bin/env python3
"""Cloudflare DNS-only IPv4 pool adapter. One JSON request on stdin."""

import http.client
import ipaddress
import json
import os
import re
import sys
from urllib.parse import urlencode


class AdapterError(Exception):
    """Only fixed error codes may be exposed to callers."""


class Cloudflare:
    def __init__(self, credentials):
        zone_id = credentials.get('zone_id')
        token = credentials.get('api_token')
        if not (isinstance(zone_id, str) and re.fullmatch(r'[0-9a-fA-F]{32}', zone_id)
                and isinstance(token, str) and token.strip()):
            raise AdapterError('invalid_credentials')
        self.path = '/client/v4/zones/' + zone_id + '/dns_records'
        self.token = token

    def request(self, method, *, query=None, record=None):
        path = self.path + ('?' + urlencode(query) if query else '')
        # HTTPSConnection verifies TLS and never follows redirects with the token.
        connection = http.client.HTTPSConnection('api.cloudflare.com', timeout=10)
        try:
            headers = {'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'}
            body = json.dumps(record).encode() if record is not None else None
            connection.request(method, path, body=body, headers=headers)
            response = connection.getresponse()
            if not 200 <= response.status < 300:
                raise AdapterError('provider_request_failed')
            raw = response.read((1 << 20) + 1)
            if len(raw) > 1 << 20:
                raise AdapterError('invalid_provider_response')
            payload = json.loads(raw)
            if not isinstance(payload, dict) or payload.get('success') is not True:
                raise AdapterError('provider_request_failed')
            return payload
        finally:
            connection.close()


def read_pool(name, request):
    values = set()
    page = 1
    while True:
        data = request('GET', query={'name.exact': name, 'page': page, 'per_page': 100})
        rows = data['result']
        pages = data['result_info']['total_pages']
        if not isinstance(rows, list) or type(pages) is not int or pages < 0:
            raise AdapterError('invalid_provider_response')
        for row in rows:
            if row['name'].rstrip('.').lower() != name:
                continue
            kind = row['type']
            # Cloudflare proxies the entire A/AAAA pool if any member is proxied.
            if kind in ('A', 'AAAA') and row.get('proxied') is not False:
                raise AdapterError('dns_only_pool_required')
            if kind in ('CNAME', 'NS'):
                raise AdapterError('incompatible_exact_record')
            if kind == 'A':
                values.add(str(ipaddress.IPv4Address(row['content'])))
        if page >= pages:
            return values
        page += 1


def execute(message, zone, name, request):
    if (not isinstance(message, dict) or message.get('name') != name
            or not zone or not name.endswith('.' + zone)
            or not all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label)
                       for label in name.split('.'))):
        raise AdapterError('invalid_request_or_name')
    action = message.get('action')
    if action == 'read_a_pool':
        return {'ok': True, 'values': sorted(read_pool(name, request))}
    if action != 'append_a':
        raise AdapterError('unsupported_action')
    value = message.get('value')
    try:
        address = ipaddress.IPv4Address(value) if isinstance(value, str) else None
    except ValueError:
        address = None
    if not address or not address.is_global or address.is_multicast:
        raise AdapterError('public_unicast_ipv4_required')

    if value in read_pool(name, request):
        return {'ok': True, 'value': value, 'changed': False}
    record = {'name': name, 'type': 'A', 'content': value, 'ttl': 1, 'proxied': False}
    try:
        created = request('POST', record=record)['result']
        if (created.get('name') == name and created.get('type') == 'A'
                and created.get('content') == value and created.get('proxied') is False):
            return {'ok': True, 'value': value, 'changed': True}
    except Exception:
        pass
    # A timeout or duplicate can follow a committed create. Never replay the POST.
    try:
        if value in read_pool(name, request):
            return {'ok': True, 'value': value}
    except Exception:
        pass
    return {'ok': False, 'error': 'write_unresolved'}


def main():
    raw = sys.stdin.buffer.read(4097)
    if len(raw) > 4096:
        raise AdapterError('request_oversize')
    name = os.environ['GPTADMIN_FRP_DNS_NAME'].lower().rstrip('.')
    zone = os.environ['GPTADMIN_FRP_DNS_ZONE'].lower().rstrip('.')
    with open(os.environ['GPTADMIN_FRP_DNS_CREDENTIALS_FILE'], encoding='utf-8') as file:
        provider = Cloudflare(json.load(file))
    return execute(json.loads(raw), zone, name, provider.request)


if __name__ == '__main__':
    try:
        result = main()
    except AdapterError as error:
        result = {'ok': False, 'error': str(error)}
    except Exception:
        # Provider payloads, credentials and exception messages stay local.
        result = {'ok': False, 'error': 'adapter_failed'}
    print(json.dumps(result))
