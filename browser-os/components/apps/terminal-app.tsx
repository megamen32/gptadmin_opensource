'use client';

import { FormEvent, useEffect, useState } from 'react';
import { listComputers } from '@/lib/computer-api';
import type { Computer } from '@/lib/types';

type Tab = { id: number; computerId: string; lines: string[] };

export function TerminalApp() {
  const [computers, setComputers] = useState<Computer[]>([]);
  const [tabs, setTabs] = useState<Tab[]>([{ id: 1, computerId: '', lines: ['CloudOS Terminal'] }]);
  const [activeId, setActiveId] = useState(1);
  const [command, setCommand] = useState('hostname');
  const active = tabs.find(tab => tab.id === activeId) ?? tabs[0];
  const computer = computers.find(item => item.id === active.computerId);
  useEffect(() => { void listComputers().then(result => setComputers((result.data ?? []).filter(item => item.status === 'online' && item.capabilities.includes('terminal')))); }, []);
  function changeComputer(computerId: string) { setTabs(old => old.map(tab => tab.id === activeId ? { ...tab, computerId, lines: [...tab.lines, `Connected: ${computers.find(item => item.id === computerId)?.name ?? 'unknown'}`] } : tab)); }
  function addTab() { const id = Date.now(); setTabs(old => [...old, { id, computerId: '', lines: ['CloudOS Terminal'] }]); setActiveId(id); }
  function closeTab(id: number) { if (tabs.length === 1) return; const index = tabs.findIndex(tab => tab.id === id); const next = tabs.filter(tab => tab.id !== id); setTabs(next); if (id === activeId) setActiveId(next[Math.max(0, index - 1)].id); }
  async function run(event: FormEvent) { event.preventDefault(); if (!active.computerId || !command) return; setTabs(old => old.map(tab => tab.id === activeId ? { ...tab, lines: [...tab.lines, `${computer?.name ?? 'cloudos'} $ ${command}`] } : tab)); const response = await fetch('/api/computer/processes', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({operation:'processes.exec', computer: active.computerId, session:'cloudos-ui', command}) }); const payload = await response.json(), result = payload.data, output = result?.response?.structuredContent?.result?.stdout || result?.response?.structuredContent?.result?.stderr || result?.detail || payload.error || 'Command failed.'; setTabs(old => old.map(tab => tab.id === activeId ? { ...tab, lines: [...tab.lines, output] } : tab)); }
  return <section className="flex h-full flex-col overflow-hidden bg-[#0b0e12] font-mono text-sm text-slate-100"><div className="flex items-center border-b border-white/10 bg-[#151a22] px-2 pt-2">{tabs.map((tab, index) => <button key={tab.id} onClick={() => setActiveId(tab.id)} className={`mr-1 flex min-w-32 items-center gap-2 rounded-t px-3 py-2 text-left text-xs ${tab.id === activeId ? 'bg-[#0b0e12] text-white' : 'bg-white/5 text-slate-400'}`}><span className="truncate">{computers.find(item => item.id === tab.computerId)?.name ?? `Terminal ${index + 1}`}</span>{tabs.length > 1 && <span onClick={event => { event.stopPropagation(); closeTab(tab.id); }} className="text-slate-500 hover:text-white">×</span>}</button>)}<button aria-label="New terminal tab" onClick={addTab} className="px-3 pb-2 text-lg text-slate-400 hover:text-white">+</button></div><div className="flex items-center justify-between border-b border-white/10 px-4 py-2 text-xs"><span className="text-emerald-300">{computer ? `user@${computer.name}:~` : 'choose a computer for this tab'}</span><select aria-label="Terminal computer" value={active.computerId} onChange={event => changeComputer(event.target.value)} className="rounded bg-[#151a22] px-2 py-1 text-xs text-white"><option value="">Connect…</option>{computers.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></div><pre className="flex-1 overflow-auto p-4 whitespace-pre-wrap leading-6 text-slate-200">{active.lines.join('\n')}</pre><form onSubmit={run} className="flex border-t border-white/10 px-4 py-3"><span className="mr-2 text-emerald-300">{computer ? '$' : '·'}</span><input aria-label="Command" value={command} onChange={event => setCommand(event.target.value)} disabled={!active.computerId} className="flex-1 bg-transparent outline-none disabled:opacity-50" autoComplete="off"/><button disabled={!active.computerId} className="ml-3 rounded bg-violet-500 px-3 py-1 font-sans text-xs text-white disabled:opacity-40">Run</button></form></section>;
}
