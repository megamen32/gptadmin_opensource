// ============================================================
// GPTAdmin Browser OS — Menu Bar
// ============================================================

'use client';

import React, { useState, useEffect } from 'react';
import { CloudOSIcon } from './icons/app-icons';
import { useWindowStore } from '@/lib/window-store';

export function MenuBar() {
  const [time, setTime] = useState('');
  const [date, setDate] = useState('');
  const [menu, setMenu] = useState<string | null>(null);
  const focusedWindow = useWindowStore(s => s.windows.find(w => w.isFocused));
  const openWindow = useWindowStore(s => s.openWindow);
  const closeWindow = useWindowStore(s => s.closeWindow);
  const minimizeWindow = useWindowStore(s => s.minimizeWindow);
  const maximizeWindow = useWindowStore(s => s.maximizeWindow);
  const action = (fn: () => void) => { fn(); setMenu(null); };

  useEffect(() => {
    const update = () => {
      const now = new Date();
      setTime(now.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: true }));
      setDate(now.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' }));
    };
    update();
    const interval = setInterval(update, 1000);
    return () => clearInterval(interval);
  }, []);

  return (
    <div className="h-7 bg-black/40 backdrop-blur-2xl flex items-center px-3 text-white/90 text-[13px] font-normal z-[9999] relative border-b border-white/5">
      {/* Apple Logo / Cloud OS */}
      <button aria-label="Cloud OS menu" onClick={() => setMenu(menu === 'cloud' ? null : 'cloud')} className="flex items-center gap-1.5 px-2 py-0.5 rounded hover:bg-white/10 transition-colors mr-3">
        <CloudOSIcon size={14} />
        <span className="font-semibold">Cloud OS</span>
      </button>

      {/* Active App Name */}
      <button className="px-2 py-0.5 rounded hover:bg-white/10 transition-colors font-semibold">
        {focusedWindow?.title || 'Finder'}
      </button>

      {['File', 'Edit', 'View', 'Window', 'Help'].map(label => <button key={label} onClick={() => setMenu(menu === label ? null : label)} className="px-2 py-0.5 rounded hover:bg-white/10 transition-colors">{label}</button>)}

      {menu && <div className="absolute left-2 top-7 min-w-44 rounded-md border border-white/15 bg-slate-900 p-1 shadow-xl">
        {menu === 'cloud' && <MenuItem label="About Cloud OS" onClick={() => action(() => alert('Cloud OS: connected computer workspace'))} />}
        {menu === 'File' && <><MenuItem label="Open Files" onClick={() => action(() => openWindow('files'))} /><MenuItem label="Open Terminal" onClick={() => action(() => openWindow('terminal'))} /></>}
        {menu === 'Edit' && <MenuItem label="Copy selected text" onClick={() => action(() => document.execCommand('copy'))} />}
        {menu === 'View' && <MenuItem label="Toggle fullscreen" onClick={() => action(() => document.fullscreenElement ? void document.exitFullscreen() : void document.documentElement.requestFullscreen())} />}
        {menu === 'Window' && <><MenuItem label="Minimize focused window" onClick={() => action(() => focusedWindow && minimizeWindow(focusedWindow.id))} /><MenuItem label="Zoom focused window" onClick={() => action(() => focusedWindow && maximizeWindow(focusedWindow.id))} /><MenuItem label="Close focused window" onClick={() => action(() => focusedWindow && closeWindow(focusedWindow.id))} /></>}
        {menu === 'Help' && <MenuItem label="CloudOS help" onClick={() => action(() => openWindow('computers'))} />}
      </div>}

      {/* Spacer */}
      <div className="flex-1" />

      {/* Status Indicators */}
      <div className="flex items-center gap-3 text-[12px]">
        <StatusIndicator label="Cloud" status="connected" />
        <StatusIndicator label="Local Computer" status="connected" />
        <StatusIndicator label="Network" status="connected" />
      </div>

      {/* Separator */}
      <div className="w-px h-3 bg-white/15 mx-2" />

      {/* Date & Time */}
      <div className="flex items-center gap-1.5">
        <span className="text-white/60">{date}</span>
        <span>{time}</span>
      </div>
    </div>
  );
}

function MenuItem({ label, onClick }: { label: string; onClick: () => void }) {
  return <button onClick={onClick} className="block w-full rounded px-3 py-2 text-left text-sm hover:bg-white/10">{label}</button>;
}

function StatusIndicator({ label, status }: { label: string; status: 'connected' | 'disconnected' | 'warning' }) {
  const colors = {
    connected: 'bg-[#06d6a0] shadow-[0_0_4px_rgba(6,214,160,0.5)]',
    disconnected: 'bg-white/30',
    warning: 'bg-[#ffd166] shadow-[0_0_4px_rgba(255,209,102,0.5)]',
  };

  return (
    <div className="flex items-center gap-1.5" title={`${label}: ${status}`}>
      <div className={`w-1.5 h-1.5 rounded-full ${colors[status]}`} />
      <span className="text-white/50 text-[11px] hidden lg:inline">{label}</span>
    </div>
  );
}
