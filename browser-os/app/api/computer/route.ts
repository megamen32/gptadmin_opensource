// ============================================================
// GPTAdmin Browser OS — Computer API Route
// Operations: computer.list, computer.status, computer.disconnect
// ============================================================

import { NextRequest, NextResponse } from 'next/server';

const HUB_URL = 'http://localhost:9001/api/v1/cloud-os';

const MOCK_COMPUTERS = [
  {
    id: 'macbook-home',
    name: 'MacBook Home',
    os: 'macOS 14.2 Sonoma',
    capabilities: ['files', 'processes', 'network'],
    status: 'online',
    lastSeen: new Date().toISOString(),
  },
  {
    id: 'linux-server',
    name: 'Linux Server',
    os: 'Ubuntu 22.04 LTS',
    capabilities: ['files', 'processes'],
    status: 'offline',
    lastSeen: '2024-12-15T10:30:00Z',
  },
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
  const computerId = searchParams.get('computer');

  if (operation === 'computer.list') {
    const hubResult = await proxyToHub(`${HUB_URL}/shell/computers`);
    if (hubResult) {
      return NextResponse.json({ data: Array.isArray(hubResult.computers) ? hubResult.computers : [], mock: false });
    }
    return NextResponse.json({ data: MOCK_COMPUTERS, mock: true });
  }

  if (operation === 'computer.status' && computerId) {
    const hubResult = await proxyToHub(`${HUB_URL}/computers/${computerId}`);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    const comp = MOCK_COMPUTERS.find(c => c.id === computerId) || MOCK_COMPUTERS[0];
    return NextResponse.json({ data: comp, mock: true });
  }

  if (operation === 'computer.disconnect' && computerId) {
    const hubResult = await proxyToHub(`${HUB_URL}/computers/${computerId}/disconnect`, {});
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: { success: true }, mock: true });
  }

  // Default: list
  return NextResponse.json({ data: MOCK_COMPUTERS, mock: true });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const { operation, computer } = body;

  if (operation === 'computer.disconnect' && computer) {
    const hubResult = await proxyToHub(`${HUB_URL}/computers/${computer}/disconnect`, body);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: { success: true }, mock: true });
  }

  return NextResponse.json({ data: MOCK_COMPUTERS, mock: true });
}
