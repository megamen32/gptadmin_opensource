'use client';

import { useEffect, useState } from 'react';
import { listComputers } from '@/lib/computer-api';
import type { Computer } from '@/lib/types';

export function ComputersApp() {
  const [computers, setComputers] = useState<Computer[]>([]);
  const [usingMock, setUsingMock] = useState(false);

  useEffect(() => {
    void listComputers().then(result => {
      setComputers((Array.isArray(result.data) ? result.data : []).filter(item => item.capabilities.includes('files') && item.capabilities.includes('terminal')));
      setUsingMock(result.mock);
    });
  }, []);

  return (
    <section className="h-full overflow-auto bg-slate-950/60 p-5 text-slate-100">
      <div className="flex items-center justify-between gap-3"><h2 className="text-lg font-semibold">Computers</h2><span className="text-xs text-slate-400">{usingMock ? 'demo' : 'hub'}</span></div>
      <p className="mt-1 text-sm text-slate-400">Live computers connected through the Cloud OS Hub.</p>
      <div className="mt-4 space-y-2">
        {computers.map(computer => <article key={computer.id} className="rounded border border-white/10 bg-white/5 p-3"><div className="font-medium">{computer.name}</div><div className="mt-1 text-xs text-slate-400">{computer.os} · {computer.capabilities.join(', ')} · {computer.status}</div></article>)}
      </div>
    </section>
  );
}
