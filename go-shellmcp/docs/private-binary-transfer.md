# Private binary transfer

The file_transfer MCP MCP tool uses the existing authenticated GPTAdmin connection.
No public download links, SSH keys, new tokens or expanded filesystem rights
are created.

Limits: 256 MiB per file, 1 MiB decoded per chunk, four retained transfers per
Node and 15-minute absolute expiry. Executors advertising
`file_transfer_version=3` support this large-file boundary. Older executors
retain the 16 MiB and 32 KiB contract.

Upload: upload_begin(path,size,sha256), then contiguous upload_chunk calls
(transfer_id,path,offset,data_base64, optional chunk sha256). upload_status
returns received, so a lost response is recoverable. Exact chunk retries are
idempotent. upload_commit verifies size and SHA256 and atomically creates a
NEW 0600 file without overwriting any destination. A privileged Node preserves
the destination directory owner, so the ordinary OS user can read the upload. The same commit can be
retried while its handle is retained. upload_abort releases the handle only.

Replace (file_transfer_version 2): upload_replace(path,size,sha256,expected_sha256)
opens a replace-mode upload against an EXISTING regular file within the
advertised file-size boundary.
The begin fails unless the current destination digest equals expected_sha256.
Chunks and status behave exactly like an upload. upload_commit re-reads and
re-hashes the destination and aborts with "destination changed since
upload_replace began" when it no longer matches expected_sha256; only then does
it publish through a fsynced private temporary file and an atomic rename over
the destination. This gives edit workflows a compare-and-swap primitive without
a sidecar file plus mv.

Download: download_begin(path) creates a bounded snapshot and returns size,
sha256 and expires_at. download_chunk accepts transfer_id,path,offset,max_bytes
(up to the advertised chunk boundary), returning data_base64, chunk_sha256,
next_offset and eof.
Verify each chunk and the reconstructed full hash, then download_abort.

Metadata (file_transfer_version 2): stat(path[,hash]) is a stateless read that
returns name, size, mode, mtime, is_dir and is_regular within the same roots;
hash=true additionally returns the SHA-256 of a regular file within the
advertised file-size boundary.
list(path[,pattern]) enumerates one directory (sorted, at most 1000 entries,
truncated=true when cut) and accepts a glob matched against entry names.
Neither consumes a transfer slot. Read-only profiles may use stat and list.

Resume covers transport disconnections, not a Node restart. Transfer buffers
are not persisted. Commit uses a random exclusive private temporary file,
fsync and an atomic no-replace hard link (upload) or rename (upload_replace) in
the destination directory, which must support them. Normal paths remove
temporary files. A crash during commit can leave a private
.gptadmin-transfer-*.tmp file.

Every call rechecks canonical allowed roots, symlinks, exact path, direction
and signed profile identity/version. Read-only profiles cannot upload or
replace. Write-only roots do not grant downloads or metadata reads. Legacy
nodes neither advertise nor accept the tool until they declare
file_transfer_version=1 in their signed heartbeat; stat, list and
upload_replace additionally require file_transfer_version=2, and the Hub
rejects them early with an explicit error on older executors. Unscoped
node-owner calls use existing InspectRoots.

Transfer data is exact and unredacted. Keep base64 inside tool orchestration,
not chat, and obey secret/data-sharing policies. This is a bounded initial
version, not a large-build or persistent cross-restart transfer service.

Verification: focused Node tests, Hub ACL/legacy tests and a real authenticated
MCP exec -> Hub queue -> Node -> filesystem -> download integration test.

## Fleet transfer

The Hub-owned `fleet_transfer` tool composes the existing download and upload
contracts for two explicit paired file targets. It never opens a direct LAN
listener and never changes SSH keys, firewall or VPN state.

Enable exact source/destination pairs in the Hub environment:

```json
GPTADMIN_FLEET_TRANSFER_ALLOWLIST='[{"source":"file:node-a","destination":"file:node-b"}]'
```

Use the existing authenticated MCP code path:

```js
return await mcp.call("hub", "fleet_transfer", {
  source: "file:node-a",
  destination: "file:node-b",
  source_path: "/srv/source.bin",
  destination_path: "/srv/destination.bin"
});
```

The pair must be present exactly in the Hub allowlist and each file target
must resolve through the existing enrolled-node identity and peer route. The
destination is created atomically and must not already exist. The completed
receipt binds both target IDs and paths to the byte count and SHA-256, and
sets `verified=true` only after the destination commit receipt matches the
source snapshot.

Fleet transfer requires `file_transfer_version=3` on both peers. It uses the
same bounded 256 MiB and 1 MiB chunk limits, so a signed desktop archive can
move without a public URL or a direct node-to-node listener.

Release safety: the lifecycle updater accepts --expected-revision with the full
installed Git SHA. It checks before preflight and again under a cooperating
current-pointer lock, and refuses a stale revision. Rollback checks the actual
candidate revision before changing code/configuration. This supplements the
required shared release lease; it does not replace it. For sanitized builds,
keep the public installed SHA and its private Source-Commit provenance distinct.
