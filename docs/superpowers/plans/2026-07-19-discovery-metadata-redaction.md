# Discovery Metadata Redaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent public MCP discovery from exposing credentials embedded in registered-agent metadata.

**Architecture:** Keep the compact discovery payload unchanged. For `detail=full`, copy agent metadata through a recursive redactor before serializing it, replacing sensitive values while preserving safe operational metadata such as transport and public endpoint details.

**Tech Stack:** Go, `go-hub/internal/hub`, Go unit tests, live MCP smoke test.

## Global Constraints

- Default discovery remains compact and stable.
- Detailed discovery retains safe metadata but never returns credential values.
- Do not change target selection or tool execution behavior.
- Use TDD: observe the red test before implementation.

---

### Task 1: Redact sensitive detailed-discovery metadata

**Files:**
- Modify: `go-hub/internal/hub/server_test.go`
- Modify: `go-hub/internal/hub/server.go`

**Interfaces:**
- Consumes: `Agent.Meta map[string]any` from registered MCP agents.
- Produces: `agentAsServer(Agent) map[string]any` with a safe `meta` value.

- [x] **Step 1: Write the failing test**

Register an agent with `meta.args` containing an Authorization header and assert that detailed discovery does not contain the credential string while retaining the agent ID and safe metadata.

- [x] **Step 2: Run test to verify it fails**

Run: `cd go-hub && go test ./internal/hub -run TestDetailedDiscoveryRedactsSensitiveAgentMetadata -count=1`

Expected: FAIL because `agentAsServer` returns `a.Meta` unchanged.

- [x] **Step 3: Write minimal implementation**

Add a metadata-copying redactor in `server.go` and invoke it from `agentAsServer`. Redact values whose map keys contain `token`, `secret`, `password`, `authorization`, `credential`, or `api_key` case-insensitively; recurse through maps and slices.

- [x] **Step 4: Run focused tests**

Run: `cd go-hub && go test ./internal/hub -run 'TestDetailedDiscoveryRedactsSensitiveAgentMetadata|TestListServersUsesHubKind' -count=1`

Expected: PASS.

- [ ] **Step 5: Run the Hub suite and live smoke**

Run: `cd go-hub && go test ./...`

Status: local suite passed. Live smoke remains pending deployment because the
currently deployed Hub still returns the unsafe discovery payload.

Then call the installed GPTADMIN plugin `discover`, verify no sensitive metadata values are returned, and call `schema(target=hub)` plus `execute(status)`.
