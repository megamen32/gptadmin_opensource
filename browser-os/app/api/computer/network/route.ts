// ============================================================
// GPTAdmin Browser OS — Network API Route
// Operations: network.connect
// ============================================================

import { NextRequest, NextResponse } from 'next/server';

const HUB_URL = 'http://localhost:9001/api/v1/cloud-os';

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
  const computer = searchParams.get('computer') || '';

  if (operation === 'network.connect') {
    const hubResult = await proxyToHub(`${HUB_URL}/network/connect`, {
      computer,
      session: 'demo',
      operation: 'network.connect',
    });
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: { success: true, message: 'Pairing initiated (mock)' }, mock: true });
  }

  return NextResponse.json({ error: 'Unknown operation' }, { status: 400 });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const { operation, computer } = body;

  if (operation === 'network.connect') {
    const hubResult = await proxyToHub(`${HUB_URL}/network/connect`, body);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ data: { success: true, message: 'Pairing initiated (mock)' }, mock: true });
  }

  return NextResponse.json({ error: 'Unknown operation' }, { status: 400 });
}