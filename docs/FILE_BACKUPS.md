# GPTAdmin legacy managed file backups

`file_backup` is retained for compatibility with older clients. New agents should use the paired `file:<host>` target and `file_checkpoint` for durable restore points, plus `file_editor` for normal text edits. Do **not** create a backup/checkpoint before every edit.

`file_checkpoint` uses SHA-256 content-addressed storage with deduplication and explicit manifests. `restore` automatically creates a restore-safety checkpoint of the live state first, making rollback itself reversible. Checkpoints should mark meaningful boundaries such as a dangerous configuration change, migration, deploy, or large refactor.

The legacy `file_backup` actions remain `backup`, `list`, `cleanup`, and `restore`; its default store is `~/.gptadmin/file-backups/`. Avoid ad-hoc `file.bak.$date` files. On new system-mode installations privileged filesystem work belongs on `file:<host>` rather than through `sudo` shell editing.
