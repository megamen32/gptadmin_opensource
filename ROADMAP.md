# Roadmap

Priority order: top first.

## P0 — Restore public GPTAdmin OAuth and Apps SDK widget

Status: recovery deployed; fresh ChatGPT authorization acceptance pending

- [x] P0.1 Update the public primary and HAOS standby to tested build 134; prove public OAuth metadata and authorization routing without 502.
- [ ] P0.2 Reauthorize the GPTAdmin ChatGPT connection and confirm authenticated `resources/read` renders `ui://widget/admin-v3.html`.

## M1 — [user-visible outcome]

Status: planned

- [ ] M1.1 [stable deliverable]

## Proposed

<!-- Add new requests here before work starts. Name priority tradeoff. -->

- [x] Make the S21 Android 4G proxy the canonical egress path: installed a boot-persistent, cellular-bound Android bridge with server-100-only LAN ingress; verified 4G egress plus boot recovery. This may delay P0.2 GPTADMIN ChatGPT reauthorization acceptance.
