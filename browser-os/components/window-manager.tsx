// ============================================================
// GPTAdmin Browser OS — Window Manager
// ============================================================

'use client';

import React from 'react';
import { useWindowStore } from '@/lib/window-store';
import { Window } from './window';
import { FilesApp } from './apps/files-app';
import { TerminalApp } from './apps/terminal-app';
import { OpenCodeApp } from './apps/opencode-app';
import { BrowserApp } from './apps/browser-app';
import { ComputersApp } from './apps/computers-app';
import { AppId } from '@/lib/types';

function renderAppContent(appId: AppId) {
  switch (appId) {
    case 'files':
      return <FilesApp />;
    case 'terminal':
      return <TerminalApp />;
    case 'opencode':
      return <OpenCodeApp />;
    case 'browser':
      return <BrowserApp />;
    case 'computers':
      return <ComputersApp />;
    case 'settings':
      return (
        <div className="flex items-center justify-center h-full text-white/50">
          <div className="text-center">
            <div className="text-4xl mb-3">⚙️</div>
            <p className="text-sm">Settings — Coming Soon</p>
            <p className="text-xs text-white/30 mt-1">Configure Cloud OS preferences</p>
          </div>
        </div>
      );
    default:
      return null;
  }
}

export function WindowManager() {
  const windows = useWindowStore(state => state.windows);

  return (
    <div className="absolute inset-0 pointer-events-none" style={{ top: 28, bottom: 72 }}>
      {windows.map(win => (
        <div key={win.id} className="pointer-events-auto">
          <Window windowState={win}>
            {renderAppContent(win.appId)}
          </Window>
        </div>
      ))}
    </div>
  );
}
