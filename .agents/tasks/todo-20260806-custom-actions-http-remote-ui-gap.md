# Unselected defect: Custom Actions and selected MCP HTTP Remote UI gap

- Symptom: live `became.bezrabotnyi.com` redirects to `became.bezrabotnyi.com`, where Custom GPT Action/OpenAPI and MCP routes return 404; public MCP page only shows generic placeholder URLs and no selected-MCP Bearer HTTP Remote config.
- Smallest evidence: VPN2 probes on 2026-08-06; internal `memos-shared` MCP `memory_health` passed; source `website/src/components/site/pages/mcp-server-page.tsx` uses `your-hub.example.com` and `server/openmemory` placeholders.
- Blocker: implementing live public routing plus selected-MCP token/config UI is a separate product/deployment scope requiring explicit plan and release acceptance.
