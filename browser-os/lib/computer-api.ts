// ============================================================
// GPTAdmin Browser OS — Computer API Client
// ============================================================

import { Computer, FileEntry, ProcessInfo, ComputerOperation } from './types';
import { MOCK_COMPUTERS, MOCK_FILE_TREE, MOCK_FILE_CONTENTS, MOCK_PROCESSES } from './mock-data';

const HUB_URL = 'http://localhost:9001/api/v1/cloud-os';

interface ApiResponse<T> {
  data?: T;
  error?: string;
  mock: boolean;
}

async function fetchFromHub<T>(endpoint: string, body?: unknown): Promise<ApiResponse<T>> {
  try {
    const res = await fetch(`${HUB_URL}${endpoint}`, {
      method: body ? 'POST' : 'GET',
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(3000),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    return { data: data as T, mock: false };
  } catch {
    return { data: undefined, error: 'Hub unreachable — using mock data', mock: true };
  }
}

// ---- Computers ----

export async function listComputers(): Promise<ApiResponse<Computer[]>> {
  try {
    const res = await fetch('api/computer?operation=computer.list', {
      signal: AbortSignal.timeout(3000),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const payload = await res.json() as { data?: Computer[]; mock?: boolean };
    if (Array.isArray(payload.data)) return { data: payload.data, mock: payload.mock === true };
  } catch {
    // Keep the desktop responsive while the Hub is restarting.
  }
  return { data: MOCK_COMPUTERS, mock: true };
}

export async function getComputerStatus(id: string): Promise<ApiResponse<Computer>> {
  const res = await fetchFromHub<Computer>(`/computers/${id}`);
  if (res.data) return res;
  const comp = MOCK_COMPUTERS.find(c => c.id === id);
  return { data: comp || MOCK_COMPUTERS[0], mock: true };
}

export async function disconnectComputer(id: string): Promise<ApiResponse<{ success: boolean }>> {
  const res = await fetchFromHub<{ success: boolean }>(`/computers/${id}/disconnect`, {});
  if (res.data) return res;
  return { data: { success: true }, mock: true };
}

// ---- Files ----

export async function listFiles(op: ComputerOperation): Promise<ApiResponse<FileEntry[]>> {
  const res = await fetchFromHub<FileEntry[]>('/files/list', op);
  if (res.data) return res;
  const path = op.path || '/';
  return { data: MOCK_FILE_TREE[path] || [], mock: true };
}

export async function readFile(op: ComputerOperation): Promise<ApiResponse<{ content: string }>> {
  const res = await fetchFromHub<{ content: string }>('/files/read', op);
  if (res.data) return res;
  const path = op.path || '';
  const content = MOCK_FILE_CONTENTS[path] || `[No mock content for ${path}]`;
  return { data: { content }, mock: true };
}

export async function writeFile(op: ComputerOperation): Promise<ApiResponse<{ success: boolean }>> {
  const res = await fetchFromHub<{ success: boolean }>('/files/write', op);
  if (res.data) return res;
  return { data: { success: true }, mock: true };
}

// ---- Processes ----

export async function listProcesses(op: ComputerOperation): Promise<ApiResponse<ProcessInfo[]>> {
  const res = await fetchFromHub<ProcessInfo[]>('/processes/list', op);
  if (res.data) return res;
  return { data: MOCK_PROCESSES, mock: true };
}

export async function execCommand(op: ComputerOperation): Promise<ApiResponse<{ stdout: string; stderr: string; exitCode: number }>> {
  const res = await fetchFromHub<{ stdout: string; stderr: string; exitCode: number }>('/processes/exec', op);
  if (res.data) return res;
  return { data: undefined, mock: true };
}

export async function killProcess(op: ComputerOperation): Promise<ApiResponse<{ success: boolean }>> {
  const res = await fetchFromHub<{ success: boolean }>('/processes/kill', op);
  if (res.data) return res;
  return { data: { success: true }, mock: true };
}

// ---- Network ----

export async function connectComputer(op: ComputerOperation): Promise<ApiResponse<{ success: boolean }>> {
  const res = await fetchFromHub<{ success: boolean }>('/network/connect', op);
  if (res.data) return res;
  return { data: { success: true }, mock: true };
}
