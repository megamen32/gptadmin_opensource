# ChatGPT Action setup

GPTAdmin is designed to be imported as a ChatGPT Action or any other tool-calling assistant that supports OpenAPI.

## 1. Deploy the hub

Run the hub on a machine reachable from ChatGPT. For local-only experiments, expose it through a tunnel. For production use, put it behind HTTPS.

```bash
curl -sSL https://became.bezrabotnyi.com/install.sh | sudo bash
```

The installer prints:

- your Hub URL;
- your `CTL_TOKEN` for assistant access;
- service status and useful commands.

## 2. Import OpenAPI

In the GPT editor:

1. create or edit a GPT;
2. open Actions;
3. import the OpenAPI schema;
4. use your hub URL as the schema server URL;
5. choose API key authentication;
6. choose Bearer token;
7. paste your `CTL_TOKEN`.

You can use the repository schema at `public/openapi.json` as a template, but a deployed hub should ideally serve its own copy with the correct `servers.url`.

## 3. Recommended assistant instructions

```text
You can use GPTAdmin to administer the user's own servers.
First call listServers when the target server is unclear.
Prefer diagnostics before mutation.
Use exact server names returned by listServers.
Before destructive actions, explain the command and ask for confirmation.
After changing anything, verify the result.
Summarize executed commands, files touched, services restarted, and remaining risks.
Never print secrets unless the user explicitly asks and the endpoint is intended for that purpose.
```

## 4. Typical flow

1. User: “Check why nginx is broken on the 100th server.”
2. Assistant calls `listServers`.
3. Assistant selects the matching server.
4. Assistant checks `systemctl status nginx` and relevant logs.
5. Assistant validates configs.
6. Assistant proposes a fix.
7. User confirms.
8. Assistant applies the fix, restarts nginx, verifies status, and reports.

## 5. Safety checklist

Before connecting a public assistant:

- rotate all development tokens;
- use HTTPS;
- avoid exposing rootd agents directly;
- start with a non-critical machine;
- enable logging;
- add command allowlists if multiple users have access;
- keep destructive operations behind explicit confirmation.
