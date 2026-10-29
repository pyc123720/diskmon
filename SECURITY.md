# Security & data-safety policy

## What this tool does to your disk

`diskmon` is a monitoring and cleanup tool. Please read this before enabling anything beyond
read-only monitoring.

### Read-only by default

Scanning, attribution, forecasting and the dashboard never modify scanned files.
The USN journal and process I/O counters are read-only kernel APIs.

### Cleanup always goes through quarantine

Manual cleanups (lazy clean, tiers, per-app, per-folder, duplicates, media) **move** files to:

```
<data-dir>\quarantine\<id>\<name>
```

- Restorable for `quarantine_days` (default 7) from the dashboard or `diskmon-go.exe restore <id>`
- Size-capped (`quarantine_max_gb`, default 20 GB) — over the cap, cleanup is refused
- Indexed in `quarantine\index.jsonl` with a per-entry `.diskmon-meta.json` sidecar;
  `diskmon-go.exe quarantine-rebuild` can rebuild a corrupted index

### Quarantine is a logical delete, not a secure erase

Moving a file into quarantine removes it from its original path, but **it does not erase the
data**. The bytes remain on the volume — inside the quarantine folder, in free space, in previous
versions and in volume snapshots. Overwriting is not a reliable erasure either: Gutmann (USENIX
Security '96) showed it fails on modern magnetic media, and Wei et al. (FAST '11) showed the same
for SSDs, where wear levelling means logical overwrite ≠ physical overwrite.

So: if you need the contents gone, treat this tool as "removed from view", not "destroyed".
For real erasure use full-disk encryption from the start, or the drive vendor's secure-erase.

### What the numbers mean

- `Total` is the sum over every path — the same figure Explorer reports, and it **double-counts
  hard links**. `C:\Windows\WinSxS` is the classic case: Microsoft's own documentation gives
  4.98 GB reported vs 507 MB of actual cost.
- `Shared` is the part we could prove is a hard link: files ≥8 MB are probed by volume serial +
  file index. It is a **lower bound** — smaller hard links are not caught.
- `Unique = Total - Shared`. Use this one when comparing against the volume's used space.

### The one destructive path: L2 auto-clean rules

Rules can permanently delete files that are older than `keep_days` in a directory classified as
`clean` (temp / dev cache / update cache / logs). Rules are:

1. Created as **dry-run** by default (the web form never enables real deletion)
2. Required to pass three confirmations before the first real run
3. Restricted by a hard deny-list (`AppData\Local\Packages`, `Microsoft\Windows`, `WindowsApps`, …)
4. Never applied to user documents or desktop folders (whitelist)

If you do not need automatic deletion, leave rules disabled. The quarantine path is enough for
normal use.

### Whitelist and deny-list

- Whitelist (default): `.ssh`, `.git`, `\documents\`, `\desktop\`, `\source\repos`, `.env`, `\pictures\`
- Deny-list (built in): app packages, system component data, `WindowsApps`, `ProgramData\Microsoft\Windows`

These can be edited in `/settings`, but removing them increases the risk of data loss.

## Known limitations with security impact

- Process-level attribution is approximate (I/O counters), not per-file (no ETW).
- Files opened with shared access may be considered "not locked" and thus eligible for cleanup.
- Junctions/symlinks inside candidate folders are not specially handled.
- Builds are self-signed; verify the SHA-256 checksum from the release page.

## Reporting a vulnerability

Open a GitHub issue with the `security` label, or contact the maintainer via the GitHub profile
if the report should stay private. Please include reproduction steps and the version
(`diskmon-go.exe health`).

## Disclaimer

The software is provided "as is", without warranty of any kind. You are responsible for the data
you choose to clean. When in doubt: run a dry-run, read the rehearsal page, and keep backups of
important files. See [LICENSE](LICENSE).
