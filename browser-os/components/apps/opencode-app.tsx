'use client';

export function OpenCodeApp() {
  return (
    <section className="h-full overflow-auto bg-slate-950/60 p-5 text-slate-100">
      <h2 className="text-lg font-semibold">OpenCode</h2>
      <p className="mt-2 text-sm text-slate-400">Cloud sessions, skills, and MCP configuration stay in the cloud runtime.</p>
      <div className="mt-5 rounded border border-dashed border-white/20 p-4 text-sm text-slate-300"><p>Install MCP Bridge to connect browser capabilities to an agent.</p><a href="https://became.bezrabotnyi.com/mcp-bridge.user.js" target="_blank" rel="noreferrer" className="mt-3 inline-flex rounded bg-violet-500 px-3 py-2 font-medium text-white hover:bg-violet-400">Download MCP Bridge extension</a></div>
    </section>
  );
}
