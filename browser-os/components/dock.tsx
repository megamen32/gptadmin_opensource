// ============================================================
// GPTAdmin Browser OS — Dock
// ============================================================

'use client';

import React, { useState } from 'react';
import { useWindowStore } from '@/lib/window-store';
import { appIconMap } from './icons/app-icons';
import { APP_DEFINITIONS } from '@/lib/mock-data';
import { AppId } from '@/lib/types';

const DOCK_APPS: { id: AppId; name: string; icon: string }[] = APP_DEFINITIONS.map(a => ({
  id: a.id,
  name: a.name,
  icon: a.icon,
}));

export function Dock() {
  const { openWindow, unminimizeWindow, windows, focusWindow } = useWindowStore();
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);

  const handleDockClick = (appId: AppId) => {
    const existingWindow = windows.find(w => w.appId === appId);
    if (existingWindow) {
      if (existingWindow.isMinimized) {
        unminimizeWindow(existingWindow.id);
      } else {
        focusWindow(existingWindow.id);
      }
    } else {
      openWindow(appId);
    }
  };

  const isAppRunning = (appId: AppId) => windows.some(w => w.appId === appId);
  const isAppFocused = (appId: AppId) => windows.some(w => w.appId === appId && w.isFocused && !w.isMinimized);

  return (
    <div className="absolute bottom-2 left-1/2 -translate-x-1/2 z-[9998]">
      {/* Tooltip */}
      {hoveredIndex !== null && (
        <div className="absolute -top-9 left-1/2 -translate-x-1/2 bg-black/70 backdrop-blur-md text-white text-xs px-3 py-1 rounded-md whitespace-nowrap pointer-events-none border border-white/10">
          {DOCK_APPS[hoveredIndex].name}
        </div>
      )}

      <div
        className="flex items-end gap-1.5 px-3 py-2 bg-white/10 backdrop-blur-2xl rounded-2xl border border-white/15"
        style={{
          boxShadow: '0 4px 30px rgba(0,0,0,0.3), inset 0 0 0 1px rgba(255,255,255,0.05)',
        }}
      >
        {DOCK_APPS.map((app, index) => {
          const IconComponent = appIconMap[app.icon];
          const isHovered = hoveredIndex === index;
          const scale = isHovered ? 1.35 : 1;
          const running = isAppRunning(app.id);
          const focused = isAppFocused(app.id);

          return (
            <button
              key={app.id}
              className="relative flex flex-col items-center transition-transform duration-200 ease-out"
              style={{
                transform: `scale(${scale}) translateY(${isHovered ? '-8px' : '0px'})`,
                transformOrigin: 'bottom center',
              }}
              onMouseEnter={() => setHoveredIndex(index)}
              onMouseLeave={() => setHoveredIndex(null)}
              onClick={() => handleDockClick(app.id)}
              aria-label={app.name}
            >
              <div className={`w-12 h-12 rounded-[12px] flex items-center justify-center transition-shadow duration-200 ${
                focused ? 'ring-2 ring-white/30' : ''
              }`}
                style={{
                  boxShadow: isHovered ? '0 4px 12px rgba(0,0,0,0.3)' : 'none',
                }}
              >
                {IconComponent && <IconComponent size={48} />}
              </div>
              {/* Running indicator */}
              {running && (
                <div className={`absolute -bottom-1 w-1 h-1 rounded-full ${focused ? 'bg-white' : 'bg-white/50'}`} />
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}
