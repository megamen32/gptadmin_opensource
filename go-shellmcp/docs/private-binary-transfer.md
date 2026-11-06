# Private binary transfer

The file_transfer MCP tool uses the existing authenticated GPTAdmin connection.
No public download links, SSH keys, new tokens or expanded filesystem rights
are created.

Limits: 16 MiB per file, 32 KiB decoded per chunk, four retained transfers per
Node and 15-minute absolute expiry. Chunks fit the 65,536-character exec input
and 65,536-byte output budgets. Use max_output_bytes=65536 for full chunks.

Upload: upload_begin(path,size,sha256), then contiguous upload_chunk calls
(transfer_id,path,offset,data_base64, optional chunk sha256). upload_status
returns received, so a lost response is recoverable. Exact chunk retries are
idempotent. upload_commit verifies size and SHA256 and atomically creates a
NEW 0600 file without overwriting any destination. A privileged Node preserves
the destination directory owner, so the ordinary OS user can read the upload. The same commit can be
retried while its handle is retained. upload_abort releases the handle only.

Download: download_begin(path) creates a bounded snapshot and returns size,
sha256 and expires_at. download_chunk accepts transfer_id,path,offset,max_bytes
(up to 32768), returning data_base64, chunk_sha256, next_offset and eof.
Verify each chunk and the reconstructed full hash, then download_abort.

Resume covers transport disconnections, not a Node restart. Transfer buffers
are not persisted. Commit uses a random exclusive private temporary file,
fsync and an atomic no-replace hard link in the destination directory, which
must support hard links. Normal paths remove temporary files. A crash during
commit can leave a private .gptadmin-transfer-*.tmp file.

Every call rechecks canonical allowed roots, symlinks, exact path, direction
and signed profile identity/version. Read-only profiles cannot upload.
Write-only roots do not grant downloads. Legacy nodes neither advertise nor
accept the tool until they declare file_transfer_version=1 in their signed
heartbeat. Unscoped node-owner calls use existing InspectRoots.

Transfer data is exact and unredacted. Keep base64 inside tool orchestration,
not chat, and obey secret/data-sharing policies. This is a bounded initial
version, not a large-build or persistent cross-restart transfer service.

Verification: focused Node tests, Hub ACL/legacy tests and a real authenticated
MCP exec -> Hub queue -> Node -> filesystem -> download integration test.

Release safety: the lifecycle updater accepts --expected-revision with the full
installed Git SHA. It checks before preflight and again under a cooperating
current-pointer lock, and refuses a stale revision. Rollback checks the actual
candidate revision before changing code/configuration. This supplements the
required shared release lease; it does not replace it. For sanitized builds,
keep the public installed SHA and its private Source-Commit provenance distinct.
