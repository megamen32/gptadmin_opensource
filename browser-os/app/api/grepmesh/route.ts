import { NextRequest, NextResponse } from 'next/server';

export async function POST(request: NextRequest) {
  const { query } = await request.json().catch(() => ({}));
  if (typeof query !== 'string' || !query.trim()) return NextResponse.json({ error: 'Query is required' }, { status: 400 });
  try {
    const response = await fetch('http://127.0.0.1:9419/mcp', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2026-07-28', 'Mcp-Method': 'tools/call', 'Mcp-Name': 'search_text' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'search_text', arguments: { query, hosts: '*', max_matches: 30 } } }), signal: AbortSignal.timeout(8000),
    });
    const body = await response.json();
    const text = body?.result?.content?.[0]?.text;
    if (!response.ok || typeof text !== 'string') throw new Error(body?.error?.message || `GrepMesh HTTP ${response.status}`);
    return NextResponse.json({ data: JSON.parse(text) });
  } catch (error) { return NextResponse.json({ error: error instanceof Error ? error.message : 'GrepMesh unavailable' }, { status: 502 }); }
}
