#!/usr/bin/env python3
"""Verify a real Node execution before routing traffic to it (stdlib only)."""
import argparse
import http.client
import json
import os
import secrets
import signal
import socket
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', required=True, help='logical MCP origin, without /mcp')
    parser.add_argument('--target', required=True, help='explicit shell:<name> execution owner')
    parser.add_argument('--token-env', default='GPTADMIN_CODEX_MCP_BEARER')
    parser.add_argument('--connect-to', help='connect to this IP; retain URL hostname and verified TLS/SNI')
    parser.add_argument('--expect-commit', help='require this /version git_commit')
    parser.add_argument('--timeout', type=float, default=30, help='whole probe deadline in seconds')
    args = parser.parse_args()
    parsed = urllib.parse.urlsplit(args.url)
    if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
        parser.error('--url must be an origin without credentials, query or path')
    if not args.target.startswith('shell:') or not args.target[6:]:
        parser.error('--target must name one explicit shell owner')
    if args.timeout <= 0 or args.timeout > 600:
        parser.error('--timeout must be greater than zero and at most 600')
    if args.connect_to and parsed.scheme != 'https':
        parser.error('--connect-to requires verified HTTPS')
    token = os.environ.get(args.token_env)
    if not token:
        parser.error('the selected token environment variable is empty')
    if any(ord(char) < 33 or ord(char) > 126 for char in token):
        parser.error('the selected token must contain printable ASCII without whitespace')
    if hasattr(signal, 'setitimer'):
        def deadline_expired(signum, frame):
            raise TimeoutError('probe deadline expired; do not replay a possibly accepted command')
        signal.signal(signal.SIGALRM, deadline_expired)
        signal.setitimer(signal.ITIMER_REAL, args.timeout)

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    handlers = [NoRedirect(), urllib.request.ProxyHandler({})]
    if args.connect_to:
        class DirectHTTPSConnection(http.client.HTTPSConnection):
            def connect(self):
                sock = socket.create_connection((args.connect_to, self.port), self.timeout)
                self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        class DirectHTTPSHandler(urllib.request.HTTPSHandler):
            def https_open(self, req):
                return self.do_open(DirectHTTPSConnection, req, context=self._context, check_hostname=self._check_hostname)
        handlers.append(DirectHTTPSHandler())
    opener = urllib.request.build_opener(*handlers)
    deadline = time.monotonic() + args.timeout
    origin = args.url.rstrip('/')
    protocol = '2025-03-26'
    session = None

    def request(path, body=None):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('probe deadline expired; do not replay a possibly accepted command')
        headers = {'Content-Type':'application/json', 'Authorization':'Bearer '+token,
                   'Accept':'application/json, text/event-stream'}
        if body and body.get('method') != 'initialize':
            headers['MCP-Protocol-Version'] = protocol
            headers['Mcp-Method'] = body['method']
            if body['method'] == 'tools/call':
                headers['Mcp-Name'] = body['params']['name']
        if session:
            headers['Mcp-Session-Id'] = session
        req = urllib.request.Request(origin+path, data=None if body is None else json.dumps(body).encode(), headers=headers)
        try:
            response = opener.open(req, timeout=remaining)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f'{path} {body.get("method", "") if body else "GET"}: HTTP {error.code}') from None
        with response:
            if response.status == 204:
                return {}, response.headers
            raw = response.read(2*1024*1024+1)
            if len(raw) > 2*1024*1024:
                raise RuntimeError('probe response exceeds 2 MiB')
            if response.headers.get_content_type() != 'application/json':
                raise RuntimeError('Node probe requires its JSON response mode')
            return json.loads(raw), response.headers

    def tool(name, arguments):
        response, _ = request('/mcp', {'jsonrpc':'2.0','id':2,'method':'tools/call','params':{'name':name,'arguments':arguments}})
        result = response.get('result')
        if not isinstance(result, dict) or result.get('isError'):
            raise RuntimeError('MCP call did not return a successful tool result')
        def receipt(value):
            value = dict(value)
            metadata = result.get('_meta')
            owner = metadata.get('owner_target') if isinstance(metadata, dict) else None
            if owner is not None:
                if owner != args.target:
                    raise RuntimeError('receipt owner differs from the requested executor')
                value['owner_target'] = owner
            return value
        if isinstance(result.get('structuredContent'), dict):
            return receipt(result['structuredContent'])
        for item in result.get('content', []):
            if item.get('type') == 'text':
                value = json.loads(item['text'])
                if isinstance(value, dict):
                    return receipt(value)
        raise RuntimeError('MCP result contains no JSON receipt')

    version, _ = request('/version')
    if args.expect_commit and version.get('git_commit') != args.expect_commit:
        raise RuntimeError('candidate commit differs from --expect-commit')
    initialized, headers = request('/mcp', {'jsonrpc':'2.0','id':1,'method':'initialize',
        'params':{'protocolVersion':protocol,'capabilities':{},'clientInfo':{'name':'gptadmin-node-probe','version':'1'}}})
    if not isinstance(initialized.get('result'), dict):
        raise RuntimeError('MCP initialization failed')
    protocol = initialized['result'].get('protocolVersion', protocol)
    session = headers.get('Mcp-Session-Id')
    request('/mcp', {'jsonrpc':'2.0','method':'notifications/initialized'})
    marker = 'node-probe-' + secrets.token_hex(12)
    # A harmless real execution, on one explicit owner, submitted exactly once.
    result = tool('execute', {'target':args.target,'tool':'shell_exec',
        'args':{'cmd':'printf '+marker,'timeout':10},'background':True,'idempotency_key':marker})
    job = result.get('job_id') or result.get('task_id')
    if not job:
        raise RuntimeError('execution returned no job receipt')
    job_args = {'job_id':job}
    if result.get('owner_target') is not None:
        if result['owner_target'] != args.target:
            raise RuntimeError('receipt owner differs from the requested executor')
        job_args['owner_target'] = args.target
    while result.get('status') != 'completed':
        if result.get('status') in ('failed', 'cancelled', 'canceled'):
            raise RuntimeError('probe job did not complete successfully')
        time.sleep(min(.2, max(0, deadline-time.monotonic())))
        result = tool('job', job_args)
    if result.get('error') or result.get('returncode', 0) != 0 or result.get('stdout') != marker:
        raise RuntimeError('completed receipt does not contain the successful command stdout')
    print(json.dumps({'ok':True,'origin':origin,'connect_to':args.connect_to,'target':args.target,
        'git_commit':version.get('git_commit'),'job_id':job,'run_as_user':result.get('run_as_user'),
        'stdout':marker,'elapsed_seconds':round(args.timeout-(deadline-time.monotonic()),3)}))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Never emit response bodies, headers, environment values or credentials.
        detail = str(error) if type(error) is RuntimeError else type(error).__name__
        print(json.dumps({'ok':False,'error':detail}), file=sys.stderr)
        sys.exit(1)
    finally:
        if hasattr(signal, 'setitimer'):
            signal.setitimer(signal.ITIMER_REAL, 0)
