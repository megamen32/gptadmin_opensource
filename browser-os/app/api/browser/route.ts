import { NextRequest, NextResponse } from 'next/server';
const HUB = 'http://localhost:9001/api/v1/cloud-os/browser';
export async function GET() { const res = await fetch(`${HUB}/connectors`, { signal: AbortSignal.timeout(5000) }); return NextResponse.json(await res.json(), { status: res.status }); }
export async function POST(request: NextRequest) { const body = await request.json(); const res = await fetch(`${HUB}/tabs`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body), signal:AbortSignal.timeout(30000) }); return NextResponse.json(await res.json(), { status: res.status }); }
