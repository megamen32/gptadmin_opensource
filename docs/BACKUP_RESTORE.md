# Backup and restore

GPTAdmin can create and verify a versioned configuration archive without
printing configuration contents:

```bash
gptadmin backup create /var/backups/gptadmin.tgz
gptadmin backup verify /var/backups/gptadmin.tgz
gptadmin backup restore /var/backups/gptadmin.tgz /var/lib/gptadmin-restore
```

The archive contains only regular files below the configured directory and a
canonical `gptadmin.backup/v1` manifest with relative paths, modes, sizes and
SHA-256 digests. Symlinks, hard links, absolute paths, traversal members,
unexpected files and digest drift fail closed. Restore requires a new target,
extracts into a restrictive temporary directory, verifies the extracted
manifest and renames it atomically.

This is a configuration-level drill. Service stop/start, filesystem ownership
and host-specific Tunnel credentials remain deployment steps and must be
verified by the platform acceptance runbook; the CLI does not chown restored
files or restart services implicitly. The automated clean-host gate runs the
three CLI commands in a fresh temporary target, checks bytes, modes and
current-user ownership, and asserts that command output never contains the
stored admin password.
