// ============================================================
// GPTAdmin Browser OS — Type Definitions
// ============================================================

export type ComputerCapability = 'files' | 'terminal' | 'processes' | 'network' | 'status';

export type ComputerStatus = 'online' | 'offline' | 'connecting';

export type FileEntryType = 'file' | 'directory';

export interface Computer {
  id: string;
  name: string;
  os: string;
  capabilities: ComputerCapability[];
  status: ComputerStatus;
  lastSeen: string;
}

export interface ComputerOperation {
  computer: string;
  session: string;
  operation: string;
  path?: string;
  command?: string;
  args?: string[];
  content?: string;
}

export interface FileEntry {
  name: string;
  type: FileEntryType;
  size?: number;
  modified?: string;
  path: string;
  children?: FileEntry[];
}

export interface ProcessInfo {
  pid: number;
  name: string;
  user: string;
  cpu: number;
  mem: number;
  status: string;
}

export interface TerminalLine {
  id: string;
  type: 'input' | 'output' | 'error' | 'system';
  content: string;
  timestamp: number;
}

export interface WindowState {
  id: string;
  appId: AppId;
  title: string;
  x: number;
  y: number;
  width: number;
  height: number;
  minWidth: number;
  minHeight: number;
  zIndex: number;
  isFocused: boolean;
  isMinimized: boolean;
  isMaximized: boolean;
  prevBounds?: { x: number; y: number; width: number; height: number };
}

export type AppId = 'files' | 'terminal' | 'browser' | 'opencode' | 'computers' | 'settings';

export interface AppDefinition {
  id: AppId;
  name: string;
  icon: string;
  defaultWidth: number;
  defaultHeight: number;
  minWidth: number;
  minHeight: number;
}

export interface NetworkStatus {
  connected: boolean;
  latency?: number;
  hubUrl: string;
}
