// Loaded via NODE_OPTIONS for noisy MCP servers: keep JSON-RPC stdout clean.
const origLog = console.log.bind(console);
const origInfo = console.info.bind(console);
console.log = (...args) => console.error(...args);
console.info = (...args) => console.error(...args);
