# Architecture

GPTAdmin is a small hub-and-agent system for AI-assisted server administration.

## Components

### hub_proxy

`hub_proxy` is the central API exposed to ChatGPT, scripts, or internal tools. It authenticates the control-plane token, stores recent agent heartbeats, and proxies requests to the selected agent.

Responsibilities:

- authenticate `CTL_TOKEN`;
- list registered servers;
- route `/srv/...` requests to a selected server;
- pass the correct `ROOTD_TOKEN` to the agent;
- provide a single OpenAPI surface for assistant tools.

### rootd

`rootd` is the agent running on a target machine. It receives authenticated requests from the hub and executes local operations.

Responsibilities:

- authenticate `ROOTD_TOKEN`;
- expose command execution and system inspection endpoints;
- execute commands with timeout and working-directory support;
- return structured stdout/stderr/exit-code results;
- optionally use an SSH backend instead of local execution.

### rootd_pure

`rootd_pure` is a simplified agent that uses only the Python standard library. It is useful for minimal Unix-like systems, rescue environments, or cases where installing FastAPI dependencies is inconvenient.

## Request flow

```text
assistant
  -> hub_proxy with CTL_TOKEN
  -> hub selects server by query parameter
  -> hub forwards request to rootd with ROOTD_TOKEN
  -> rootd executes operation
  -> result returns through hub
  -> assistant summarizes and verifies
```

## Server registration

Agents periodically send heartbeats to the hub. A heartbeat contains:

- server name;
- agent base URL;
- agent token;
- timestamp and metadata.

The current implementation keeps registrations in memory. For larger or long-running deployments, a persistent registry is a natural next step.

## Trust boundaries

GPTAdmin has two main trust boundaries:

1. **assistant/client -> hub** guarded by `CTL_TOKEN`;
2. **hub -> rootd** guarded by `ROOTD_TOKEN`.

The hub should be the only component exposed to the public internet. Agents should usually stay on private networks or behind host firewalls.

## Design philosophy

GPTAdmin intentionally avoids heavy orchestration. The goal is not to replace Ansible, Kubernetes, Prometheus, or SSH. The goal is to provide a compact action bridge that an AI assistant can use to inspect, execute, verify, and report.
