// ============================================================
// GPTAdmin Browser OS — Processes API Route
// Operations: processes.list, processes.exec, processes.kill
// ============================================================

import { NextRequest, NextResponse } from 'next/server';

const HUB_URL = 'http://localhost:9001/api/v1/cloud-os';

const MOCK_PROCESSES = [
  { pid: 1, name: 'launchd', user: 'root', cpu: 0.1, mem: 0.3, status: 'running' },
  { pid: 234, name: 'WindowServer', user: 'root', cpu: 2.4, mem: 1.8, status: 'running' },
  { pid: 456, name: 'code', user: 'admin', cpu: 8.2, mem: 4.5, status: 'running' },
  { pid: 789, name: 'Terminal', user: 'admin', cpu: 0.3, mem: 0.5, status: 'running' },
  { pid: 1023, name: 'Docker Desktop', user: 'admin', cpu: 1.2, mem: 6.2, status: 'running' },
  { pid: 1456, name: 'nginx', user: 'root', cpu: 0.1, mem: 0.4, status: 'running' },
  { pid: 1789, name: 'node', user: 'admin', cpu: 5.1, mem: 2.8, status: 'running' },
  { pid: 2048, name: 'go-hub', user: 'admin', cpu: 0.8, mem: 1.2, status: 'running' },
  { pid: 2345, name: 'sleep', user: 'admin', cpu: 0.0, mem: 0.1, status: 'sleeping' },
];

async function proxyToHub(url: string, body?: unknown) {
  try {
    const res = await fetch(url, {
      method: body ? 'POST' : 'GET',
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(3000),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return await res.json();
  } catch {
    return null;
  }
}

export async function GET(request: NextRequest) {
  const { searchParams } = request.nextUrl;
  const operation = searchParams.get('operation');
  const computer = searchParams.get('computer') || 'macbook-home';

  if (operation === 'processes.list') {
    const hubResult = await proxyToHub(`${HUB_URL}/processes/list`, {
      computer, session: 'demo', operation: 'processes.list',
    });
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: MOCK_PROCESSES, mock: true });
  }

  if (operation === 'processes.exec') {
    const command = searchParams.get('command') || '';
    const argsStr = searchParams.get('args') || '[]';
    let args: string[] = [];
    try { args = JSON.parse(argsStr); } catch { /* ignore */ }

    const hubResult = await proxyToHub(`${HUB_URL}/processes/exec`, {
      computer, session: 'demo', operation: 'processes.exec', command, args,
    });
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  return NextResponse.json({ data: MOCK_PROCESSES, mock: true });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const { operation } = body;

  if (operation === 'processes.kill') {
    const hubResult = await proxyToHub(`${HUB_URL}/processes/kill`, body);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: { success: true }, mock: true });
  }

  if (operation === 'processes.exec') {
    const hubResult = await proxyToHub(`${HUB_URL}/shell/exec`, { target: body.computer, command: body.command, cwd: body.cwd, timeout: body.timeout });
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  return NextResponse.json({ data: MOCK_PROCESSES, mock: true });
}
