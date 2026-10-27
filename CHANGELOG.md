# Changelog

All notable changes to this project. Versions follow `0.x → 1.x` during early development;
the first public release is 1.4.x.

## 1.6.1

- Path inspection (`/inspect`): paste any file/folder path to see size, file count,
  category, cleanup tier, lock status, and whether it is eligible for cleanup.
  Uses a 3-second budget and skips per-file lock checks on huge folders.
- `%TEMP%` / `%USERPROFILE%` style environment variables are expanded in every path input.

## 1.5.0

- Scan progress: live file/dir/GB counter, progress bar, cancellable (cancelled scans are not saved)
- Navigation collapsed: 媒体 / 重复 / 残留 are now tabs inside 清理 (9 top-level items → 6)
- Every table gets client-side search and click-to-sort (size-aware, handles GB/MB/KB)

## 1.4.1

- First-run guidance banner when no scan exists (3 steps: scan → suggestion → lazy clean)
- Scan page: correct duration text (1–2 minutes), shows the configured root

## 1.4.0

- Notification controls: mute 1 hour / until restart, from the tray or `/mute`
- System-write noise (SRU, event logs, registry hives) is recorded but no longer pops a toast
- `notify_min_delta_mb` (default 500 MB): smaller growth is recorded only

## 1.3.2

- USN attribution now follows the configured scan root (was hard-coded to `C:`)
- Drives are enumerated via `GetLogicalDrives`; the "monitored" badge follows the root
- Warning banner when scan data belongs to a different root than the configured one

## 1.3.1

- Settings page: scan root, intervals, thresholds, quarantine limits, whitelist, notification mode

## 1.3.0

- Web settings page (`/settings`) with validation and "reset to defaults"

## 1.2.2

- Quarantine index is written atomically; per-entry metadata sidecars; `quarantine-rebuild`
- Data-directory fallback is visible (banner + alert + `/api/health` fields)
- Alert throttling: same-pattern alerts merged within 5 minutes, plus a digest entry

## 1.2.x

- Media classification: images / video / documents / audio / archives / programs per folder
- Per-type cleanup of files older than N days, batch-quarantined with undo

## 1.1.x

- Lazy clean: one button, conservative tier, progress, one-click undo
- L2 rule editor: create/edit/delete rules, execution window, run-now / dry-run buttons

## 1.0.0

- Detail pages split into focus pages; sidebar navigation
- Standalone window mode (Edge/Chrome `--app`), installer, self-signed builds
- Health endpoint, watchdog, USN retry/degrade, config validation

## 0.x

- Initial prototype: USN attribution, categories, forecast, quarantine, tiers, process I/O
