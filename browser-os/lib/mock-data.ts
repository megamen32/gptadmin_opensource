// ============================================================
// GPTAdmin Browser OS — Mock Data
// ============================================================

import { Computer, FileEntry, ProcessInfo, AppDefinition } from './types';

export const MOCK_COMPUTERS: Computer[] = [
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

export const MOCK_FILE_TREE: Record<string, FileEntry[]> = {
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

export const MOCK_FILE_CONTENTS: Record<string, string> = {
  '/.zshrc': `# ZSH Configuration
export PATH="$HOME/bin:$PATH"
export EDITOR='vim'

# Aliases
alias ll='ls -la'
alias gs='git status'
alias gp='git push'

# Prompt
PROMPT='%n@%m %1~ %# '

# Plugins
plugins=(git docker kubectl)
`,
  '/Users/admin/notes.txt': `Meeting Notes - Dec 20
======================

1. Review browser-os prototype
2. Discuss go-hub API changes
3. Plan v0.2 release
4. Update documentation
`,
  '/Users/admin/Desktop/todo.md': `# TODO

- [x] Design window manager
- [x] Implement dock
- [ ] Add terminal emulator
- [ ] File browser navigation
- [ ] Computer pairing flow
`,
  '/Users/admin/Desktop/screenshot.png': '[Binary image file - 2.3 MB]',
  '/Users/guest/readme.txt': `Welcome to Cloud OS
====================
This is a demo computer managed by GPTAdmin.
`,
  '/Projects/gptadmin/README.md': `# GPTAdmin - Cloud OS

GPTAdmin is an open-source cloud operating system that lets you
manage remote computers through a browser-based desktop environment.

## Features

- **Browser OS**: macOS-like desktop in your browser
- **Remote File Management**: Browse and edit files on remote computers
- **Terminal Access**: Run commands on connected machines
- **Process Management**: Monitor and control processes

## Quick Start

\
\
pm install
npm run dev
\
\
`,
  '/Projects/gptadmin/package.json': `{
  "name": "gptadmin",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "next dev",
    "build": "next build"
  }
}
`,
  '/Projects/gptadmin/go-hub/main.go': `package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(\"ok\"))
	})
	fmt.Println("Hub listening on :9001")
	http.ListenAndServe(":9001", nil)
}
`,
  '/Projects/gptadmin/go-hub/go.mod': `module github.com/megamen32/gptadmin/go-hub

go 1.21
`,
};

export const MOCK_PROCESSES: ProcessInfo[] = [
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

export const APP_DEFINITIONS: AppDefinition[] = [
  {
    id: 'files',
    name: 'Files',
    icon: 'files',
    defaultWidth: 820,
    defaultHeight: 520,
    minWidth: 600,
    minHeight: 400,
  },
  {
    id: 'terminal',
    name: 'Terminal',
    icon: 'terminal',
    defaultWidth: 720,
    defaultHeight: 460,
    minWidth: 480,
    minHeight: 300,
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    icon: 'opencode',
    defaultWidth: 860,
    defaultHeight: 560,
    minWidth: 600,
    minHeight: 400,
  },
  { id: 'browser', name: 'Browser', icon: 'browser', defaultWidth: 860, defaultHeight: 560, minWidth: 600, minHeight: 400 },
  {
    id: 'computers',
    name: 'Computers',
    icon: 'computers',
    defaultWidth: 700,
    defaultHeight: 480,
    minWidth: 500,
    minHeight: 350,
  },
  {
    id: 'settings',
    name: 'Settings',
    icon: 'settings',
    defaultWidth: 640,
    defaultHeight: 460,
    minWidth: 480,
    minHeight: 350,
  },
];
