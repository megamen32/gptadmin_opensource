---
name: gptadmin-dns-adapter
description: Build or configure an IPv4 DNS adapter for a self-hosted GPTAdmin installation, and verify that its MCP client can reconnect after losing one public ingress. Use for external DNS providers, including SpaceWeb and Cloudflare DNS-only.
---

# Connect a self-hosted GPTAdmin to a DNS provider

Use the operator's own public HTTPS origin, DNS provider, and two independently reachable ingress paths. A named Cloudflare Tunnel with replicas already handles the loss of a connector. For that topology, skip the DNS publisher and test the surviving connector.

1. **Choose the adapter.** In a matching GPTAdmin checkout, read `docs/INGRESS_ADAPTERS.md`. Reuse `scripts/gptadmin_dns_sweb.py` or `scripts/gptadmin_dns_cloudflare.py` when appropriate. For another provider, use its official API and any executable language. The target Node must support `GPTADMIN_FRP_DNS_PROVIDER=exec` to load an external adapter.
2. **Implement the two operations.** Accept one JSON object on stdin, with no arguments. Return one JSON object on stdout and exit. For the configured hostname below the zone, use the following v1 contract. The addresses shown are documentation examples.

   | Request | Successful response |
   | --- | --- |
   | `{"action":"append_a","name":"mesh.example.com","value":"192.0.2.42"}` | `{"ok":true,"value":"192.0.2.42"}` |
   | `{"action":"read_a_pool","name":"mesh.example.com"}` | `{"ok":true,"values":["192.0.2.41","192.0.2.42"]}` |

   Return success for an existing address. Preserve unrelated records and concurrent additions. Prefer an individual-record creation API. If the API only replaces the whole set, use the provider's atomic conditional update or report that it cannot guarantee concurrent additions. After an uncertain write, read once to resolve it. If the address is still unconfirmed, return `{"ok":false,"error":"write_unresolved"}`. Exit zero for success. Reads must never change DNS.
3. **Show how to install it.** Give the operator the executable, its local credential-file format, and the exact service settings for each eligible writer: `GPTADMIN_FRP_DNS_PROVIDER=exec`, `GPTADMIN_FRP_DNS_HELPER` (absolute path), `GPTADMIN_FRP_DNS_ZONE`, `GPTADMIN_FRP_DNS_NAME` and, when needed, `GPTADMIN_FRP_DNS_CREDENTIALS_FILE`. Keep secrets on the operator's machine. Respect any existing SpaceWeb pool binding.
4. **Try it as a self-hoster.** Use a disposable hostname. Add the two owned IPs concurrently, repeat one addition, and read back the pool. Establish an authenticated baseline with the intended MCP client. Take just the first test ingress offline, open a fresh connection through the same HTTPS URL using normal DNS and verified TLS, and execute a fresh harmless command on the surviving node. Restore the test ingress and report what actually ran, including any untested provider or client. No election, DNS write or periodic probe is needed to use the surviving prepublished address.
