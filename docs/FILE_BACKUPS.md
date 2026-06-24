# GPTAdmin managed file backups

`file_backup` is the preferred shell-agent tool for backups before editing files.
It replaces ad-hoc `cp file file.bak.$date` files that stay scattered across the disk.

The tool is exposed on every `shell:*` virtual agent through the hub:

- `action=backup` copies a file or packs a directory into a managed backup object.
- `action=list` lists known backups in the managed backup root.
- `action=cleanup` removes expired backups, or backups older than `max_age_days`.
- `action=restore` restores a backup by `backup_id`.

Default storage on the target host is:

```text
~/.gptadmin/file-backups/
```

The default retention for new backups is `ttl_days=30`. Use `ttl_days=0` only for backups that must not expire automatically.

Each backup has a `meta.json` and an append-only `manifest.jsonl`. Cleanup scans only this managed backup root, not the whole filesystem.

Examples:

```json
{"action":"backup","path":"/home/admin/gptadmin/hub_proxy.py","ttl_days":30,"label":"before-admin-api-change"}
```

```json
{"action":"list","limit":20}
```

```json
{"action":"cleanup"}
```

```json
{"action":"restore","backup_id":"20260624_144633_host_abcd1234_label","overwrite":true}
```

For privileged files, pass `use_sudo=true`; the target host must allow non-interactive `sudo -n`.
