// ============================================================
// GPTAdmin Browser OS — Files API Route
// Operations: files.list, files.read, files.write
// ============================================================

import { NextRequest, NextResponse } from 'next/server';

const HUB_URL = 'http://localhost:9001/api/v1/cloud-os';

const MOCK_FILE_TREE: Record<string, unknown[]> = {
  '/': [
    { name: 'Users', type: 'directory', path: '/Users', modified: '2024-12-20T08:00:00Z' },
    { name: 'Projects', type: 'directory', path: '/Projects', modified: '2024-12-19T14:30:00Z' },
    { name: 'Downloads', type: 'directory', path: '/Downloads', modified: '2024-12-20T10:15:00Z' },
    { name: 'Applications', type: 'directory', path: '/Applications', modified: '2024-11-01T00:00:00Z' },
    { name: '.zshrc', type: 'file', size: 1240, path: '/.zshrc', modified: '2024-12-18T09:00:00Z' },
  ],
  '/Users': [
    { name: 'admin', type: 'directory', path: '/Users/admin', modified: '2024-12-20T08:00:00Z' },
    { name: 'guest', type: 'directory', path: '/Users/guest', modified: '2024-10-01T12:00:00Z' },
  ],
  '/Users/admin': [
    { name: 'Desktop', type: 'directory', path: '/Users/admin/Desktop', modified: '2024-12-20T09:30:00Z' },
    { name: 'Documents', type: 'directory', path: '/Users/admin/Documents', modified: '2024-12-19T16:00:00Z' },
    { name: '.ssh', type: 'directory', path: '/Users/admin/.ssh', modified: '2024-12-10T08:00:00Z' },
    { name: 'notes.txt', type: 'file', size: 456, path: '/Users/admin/notes.txt', modified: '2024-12-20T09:30:00Z' },
  ],
  '/Users/admin/Desktop': [
    { name: 'screenshot.png', type: 'file', size: 2456789, path: '/Users/admin/Desktop/screenshot.png', modified: '2024-12-20T09:30:00Z' },
    { name: 'todo.md', type: 'file', size: 1234, path: '/Users/admin/Desktop/todo.md', modified: '2024-12-19T14:00:00Z' },
  ],
  '/Users/admin/Documents': [
    { name: 'report.pdf', type: 'file', size: 3456789, path: '/Users/admin/Documents/report.pdf', modified: '2024-12-15T10:00:00Z' },
    { name: 'budget.xlsx', type: 'file', size: 567890, path: '/Users/admin/Documents/budget.xlsx', modified: '2024-12-10T08:00:00Z' },
    { name: 'notes', type: 'directory', path: '/Users/admin/Documents/notes', modified: '2024-12-18T11:00:00Z' },
  ],
  '/Users/admin/Documents/notes': [
    { name: 'meeting-2024-12.md', type: 'file', size: 890, path: '/Users/admin/Documents/notes/meeting-2024-12.md', modified: '2024-12-18T11:00:00Z' },
    { name: 'ideas.txt', type: 'file', size: 2345, path: '/Users/admin/Documents/notes/ideas.txt', modified: '2024-12-17T15:30:00Z' },
  ],
  '/Users/admin/.ssh': [
    { name: 'id_ed25519', type: 'file', size: 324, path: '/Users/admin/.ssh/id_ed25519', modified: '2024-12-10T08:00:00Z' },
    { name: 'id_ed25519.pub', type: 'file', size: 74, path: '/Users/admin/.ssh/id_ed25519.pub', modified: '2024-12-10T08:00:00Z' },
    { name: 'known_hosts', type: 'file', size: 4521, path: '/Users/admin/.ssh/known_hosts', modified: '2024-12-20T07:00:00Z' },
  ],
  '/Users/guest': [
    { name: 'readme.txt', type: 'file', size: 256, path: '/Users/guest/readme.txt', modified: '2024-10-01T12:00:00Z' },
  ],
  '/Projects': [
    { name: 'gptadmin', type: 'directory', path: '/Projects/gptadmin', modified: '2024-12-20T11:00:00Z' },
    { name: 'my-blog', type: 'directory', path: '/Projects/my-blog', modified: '2024-12-15T09:00:00Z' },
    { name: 'api-server', type: 'directory', path: '/Projects/api-server', modified: '2024-12-12T16:00:00Z' },
  ],
  '/Projects/gptadmin': [
    { name: 'browser-os', type: 'directory', path: '/Projects/gptadmin/browser-os', modified: '2024-12-20T11:00:00Z' },
    { name: 'go-hub', type: 'directory', path: '/Projects/gptadmin/go-hub', modified: '2024-12-19T10:00:00Z' },
    { name: 'README.md', type: 'file', size: 5678, path: '/Projects/gptadmin/README.md', modified: '2024-12-20T11:00:00Z' },
    { name: 'package.json', type: 'file', size: 1234, path: '/Projects/gptadmin/package.json', modified: '2024-12-19T10:00:00Z' },
  ],
  '/Projects/gptadmin/browser-os': [
    { name: 'app', type: 'directory', path: '/Projects/gptadmin/browser-os/app', modified: '2024-12-20T11:00:00Z' },
    { name: 'components', type: 'directory', path: '/Projects/gptadmin/browser-os/components', modified: '2024-12-20T11:00:00Z' },
    { name: 'lib', type: 'directory', path: '/Projects/gptadmin/browser-os/lib', modified: '2024-12-20T11:00:00Z' },
    { name: 'package.json', type: 'file', size: 456, path: '/Projects/gptadmin/browser-os/package.json', modified: '2024-12-20T11:00:00Z' },
  ],
  '/Projects/gptadmin/go-hub': [
    { name: 'main.go', type: 'file', size: 3456, path: '/Projects/gptadmin/go-hub/main.go', modified: '2024-12-19T10:00:00Z' },
    { name: 'go.mod', type: 'file', size: 234, path: '/Projects/gptadmin/go-hub/go.mod', modified: '2024-12-19T10:00:00Z' },
  ],
  '/Projects/my-blog': [
    { name: 'index.md', type: 'file', size: 7890, path: '/Projects/my-blog/index.md', modified: '2024-12-15T09:00:00Z' },
    { name: 'static', type: 'directory', path: '/Projects/my-blog/static', modified: '2024-12-14T12:00:00Z' },
  ],
  '/Projects/api-server': [
    { name: 'server.py', type: 'file', size: 4567, path: '/Projects/api-server/server.py', modified: '2024-12-12T16:00:00Z' },
    { name: 'requirements.txt', type: 'file', size: 189, path: '/Projects/api-server/requirements.txt', modified: '2024-12-12T16:00:00Z' },
  ],
  '/Downloads': [
    { name: 'archive.tar.gz', type: 'file', size: 45678900, path: '/Downloads/archive.tar.gz', modified: '2024-12-20T10:15:00Z' },
    { name: 'photo.jpg', type: 'file', size: 3456789, path: '/Downloads/photo.jpg', modified: '2024-12-19T15:00:00Z' },
    { name: 'setup.dmg', type: 'file', size: 89012345, path: '/Downloads/setup.dmg', modified: '2024-12-18T09:30:00Z' },
  ],
  '/Applications': [
    { name: 'VSCode.app', type: 'directory', path: '/Applications/VSCode.app', modified: '2024-12-01T00:00:00Z' },
    { name: 'Docker.app', type: 'directory', path: '/Applications/Docker.app', modified: '2024-11-15T00:00:00Z' },
    { name: 'Terminal.app', type: 'directory', path: '/Applications/Terminal.app', modified: '2024-11-01T00:00:00Z' },
  ],
};

const MOCK_FILE_CONTENTS: Record<string, string> = {
  '/.zshrc': '# ZSH Configuration\nexport PATH="$HOME/bin:$PATH"\nexport EDITOR=\'vim\'\n\nalias ll=\'ls -la\'\nalias gs=\'git status\'\n',
  '/Users/admin/notes.txt': 'Meeting Notes - Dec 20\n======================\n\n1. Review browser-os prototype\n2. Discuss go-hub API changes\n',
  '/Users/admin/Desktop/todo.md': '# TODO\n\n- [x] Design window manager\n- [x] Implement dock\n- [ ] Add terminal emulator\n',
  '/Users/admin/Desktop/screenshot.png': '[Binary image file]',
  '/Users/guest/readme.txt': 'Welcome to Cloud OS\n====================\nThis is a demo computer managed by GPTAdmin.\n',
  '/Projects/gptadmin/README.md': '# GPTAdmin - Cloud OS\n\nBrowser-based desktop environment for managing remote computers.\n',
  '/Projects/gptadmin/package.json': '{\n  "name": "gptadmin",\n  "version": "0.1.0"\n}',
  '/Projects/gptadmin/go-hub/main.go': 'package main\n\nimport "fmt"\n\nfunc main() {\n  fmt.Println("Hub listening on :9001")\n}',
};

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
  const path = searchParams.get('path') || '/';
  const computer = searchParams.get('computer') || 'macbook-home';

  if (operation === 'files.list') {
    const hubResult = await proxyToHub(`${HUB_URL}/shell/inspect`, { target: computer, path });
    if (hubResult) {
      const inspection = hubResult?.response?.structuredContent?.result?.structuredContent?.inspection;
      if (Array.isArray(inspection?.entries)) {
        return NextResponse.json({ data: { path, entries: inspection.entries.map((entry: { name: string; type: string; size?: number; modified_at?: string }) => ({ name: entry.name, type: entry.type, size: entry.size, modified: entry.modified_at, path: `${path.replace(/\/$/, '')}/${entry.name}` })) }, mock: false });
      }
      return NextResponse.json({ error: 'ShellMCP did not return a directory listing' }, { status: 502 });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  if (operation === 'files.read') {
    const hubResult = await proxyToHub(`${HUB_URL}/files/read`, {
      computer, session: 'demo', operation: 'files.read', path,
    });
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  return NextResponse.json({ error: 'Unknown operation' }, { status: 400 });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const { operation, path } = body;

  if (operation === 'files.write') {
    const hubResult = await proxyToHub(`${HUB_URL}/files/write`, body);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  if (operation === 'files.list') {
    const hubResult = await proxyToHub(`${HUB_URL}/files/list`, body);
    if (hubResult) {
      return NextResponse.json({ data: hubResult, mock: false });
    }
    return NextResponse.json({ error: 'Hub unavailable' }, { status: 502 });
  }

  return NextResponse.json({ error: 'Unknown operation' }, { status: 400 });
}
