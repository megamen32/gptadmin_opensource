// ============================================================
// GPTAdmin Browser OS — Desktop Component
// ============================================================

'use client';

import React from 'react';
import { MenuBar } from './menubar';
import { Dock } from './dock';
import { WindowManager } from './window-manager';
import { useWindowStore } from '@/lib/window-store';
import { AppId } from '@/lib/types';
import { FilesIcon, TerminalIcon, OpenCodeIcon, ComputersIcon } from './icons/app-icons';

interface DesktopIcon {
  id: AppId;
  label: string;
  icon: React.ReactNode;
}

const DESKTOP_ICONS: DesktopIcon[] = [
  { id: 'browser', label: 'Browser', icon: <span className="text-4xl">🌐</span> },
  { id: 'files', label: 'Files', icon: <FilesIcon size={52} /> },
  { id: 'terminal', label: 'Terminal', icon: <TerminalIcon size={52} /> },
  { id: 'opencode', label: 'OpenCode', icon: <OpenCodeIcon size={52} /> },
  { id: 'computers', label: 'Computers', icon: <ComputersIcon size={52} /> },
];

export function Desktop() {
  const openWindow = useWindowStore(s => s.openWindow);

  const handleDesktopIconDoubleClick = (appId: AppId) => {
    openWindow(appId);
  };

  return (
    <div className="h-screen w-screen flex flex-col overflow-hidden relative select-none"
      style={{
        fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif",
      }}
    >
      {/* Menu Bar */}
      <MenuBar />

      {/* Desktop Area */}
      <div className="flex-1 relative overflow-hidden"
        style={{
          background: 'linear-gradient(135deg, #1a0533 0%, #0d1b3e 30%, #16213e 50%, #1a1a3e 70%, #0f3460 100%)',
        }}
      >
        {/* Subtle noise texture overlay */}
        <div className="absolute inset-0 opacity-[0.03]"
          style={{
            backgroundImage: `url("data:image/svg+xml,%3Csvg viewBox='0 0 256 256' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='noiseFilter'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.65' numOctaves='3' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23noiseFilter)'/%3E%3C/svg%3E")`,
          }}
        />

        {/* Decorative orbs */}
        <div className="absolute top-1/4 left-1/4 w-96 h-96 rounded-full opacity-10"
          style={{
            background: 'radial-gradient(circle, #4361ee 0%, transparent 70%)',
            filter: 'blur(60px)',
          }}
        />
        <div className="absolute bottom-1/3 right-1/4 w-80 h-80 rounded-full opacity-8"
          style={{
            background: 'radial-gradient(circle, #7c4dff 0%, transparent 70%)',
            filter: 'blur(50px)',
            opacity: 0.08,
          }}
        />

        {/* Desktop Icons */}
        <div className="absolute top-4 right-4 flex flex-col gap-2 z-10">
          {DESKTOP_ICONS.map(icon => (
            <button
              key={icon.id}
              className="flex flex-col items-center gap-1 w-20 p-2 rounded-lg hover:bg-white/10 active:bg-white/15 transition-all duration-150 group"
              onDoubleClick={() => handleDesktopIconDoubleClick(icon.id)}
              aria-label={icon.label}
            >
              <div className="drop-shadow-lg transition-transform duration-150 group-hover:scale-105">
                {icon.icon}
              </div>
              <span className="text-white/90 text-[11px] font-medium drop-shadow-[0_1px_2px_rgba(0,0,0,0.8)]">
                {icon.label}
              </span>
            </button>
          ))}
        </div>

        {/* Window Manager */}
        <WindowManager />

        {/* Dock */}
        <Dock />
      </div>
    </div>
  );
}
