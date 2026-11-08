# Baseline review before roadmap implementation

Observed 2026-11-08 through GPTAdmin. Source and installed runtime were recorded
separately.

- Clean source main/origin: `7d43d3bafe1409dcb5d8797a229b71bc80b106a7`.
- Installed primary Node: build `1113`, clean source revision
  `26378055953db40ce92d3d0636e2717aa06d7ca4`.
- The source drain fix `226e5067b4ef9ec57a9c8b4db72da34e7b441160`
  was not yet present in that installed binary.

## Source checks

| Check | Result |
| --- | --- |
| Hub `go test -json -count=1 -timeout=8m ./...` | PASS; 1,004 top-level tests, 1,310 including subtests |
| ShellMCP equivalent full suite | PASS; 330 top-level tests, 435 including subtests |
| QJS module | PASS; 7 top-level tests, 15 including subtests |
| Admin UI Vitest | PASS; 129 tests in 17 files |
| Admin UI TypeScript and Vite build | PASS |
| Durable real-process integration | PASS; real ShellMCP, completion survives restart, cancellation and TTL |
| Real executor queued work after drain/restart | PASS |

Opt-in production proxy tests and three root-dependent ShellMCP cases were
not included. These results do not claim those environments were exercised.

## Installed runtime and ordinary client checks

- Discovery, full schema retrieval and a real shell uptime call succeeded.
- A disposable tmpfs file was created, read, edited and removed; content hashes
  and final absence were independently checked.
- A durable shell task completed and its saved result was read.
- A separate task created by this review was cancelled; its process exited.
- Existing startup and server resource receipts succeeded.
- Authenticated admin UI loaded the overview, server filter and activity list;
  no browser page or console errors were observed in that journey.
- Existing attention/error/queued counters were recorded as observations, not
  treated as evidence that unrelated servers were broken by this build.

## Reproduced correctness gaps

The prior inventory implementation exposed targets excluded by the current
access profile. Source RED/GREEN evidence is in `acl-*.txt`. Independently,
an isolated installed Node 1110 reproduced the same behavior with a newly
created local fixture client: its direct schema request was denied while the
server inventory still listed the denied target. That client is retained across
the candidate update so the same credential can verify the fix.

Child `tools/list` previously stopped at the first page. Source RED/GREEN
evidence is in `pagination-*.txt`.

Additional completeness, retention and snapshot-boundary regressions were
discovered while implementing optional schema resources and have separate
RED/GREEN records in this directory.

The phone resource picker and actual conversation attachment were not observed.
Native client catalog/read acceptance is a separate delivery check.
