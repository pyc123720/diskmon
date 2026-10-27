# Changelog

All notable changes to this project. Versions follow `0.x → 1.x` during early development;
the first public release is 1.4.x.

## 2.5.5

- "计划 974 MB，实际 2 MB" explained and reduced:
  * plans now drop folders that no longer exist (stale scan snapshot used to keep showing them);
  * the lazy-clean page samples each folder and shows how many files are in use
    (e.g. "100% (60/60)") with a warning to close running programs first;
  * the result page explains that skipped files are locked by running programs.
- Fixed two brittle tests (one hard-coded a date, one used fake paths).

## 2.5.4

- Added the quarantine page (/q), which used to 404 even though "去隔离区" linked to it:
  lists unfinished quarantined items, per-item restore, checkbox batch restore, restore-all, and
  index rebuild. Batch restore runs through the background progress framework.
- Scan ETA now uses a smoothed recent throughput (files/s over the last samples) instead of
  average-since-start, so the estimate stays honest when speed varies.

## 2.5.3

- Every file-moving action now uses the background progress framework: restore, undo lazy clean,
  undo media batch, and duplicate-copy cleanup. The page returns immediately and shows bytes moved,
  files, skipped items and any failures; only one job runs at a time.
- Verified: restoring a 281 MB / 2,712-file quarantine entry reports progress and puts every file back.

## 2.5.2

- Quarantine and preset-cleanup now run in the background with a live progress page: bytes moved,
  files, skipped locked files, current file, and an accounted result (moved vs planned, errors).
  The browser no longer sits frozen on "演练并隔离", and the global pill shows "移动文件 X GB".

## 2.5.1

- Loading feedback for waits: a thin animated progress bar appears on any internal navigation or form
  submit; after 0.6 s a "正在处理…" card is shown, so slow actions (preview/quarantine, restore,
  undo, duplicate cleanup) no longer look frozen.
- Path inspection is asynchronous: the page returns in ~6 ms with a spinner, the analysis loads
  separately (~2 s for a folder with thousands of files).

## 2.5.0

- A folder whose name looks like a cache (Cache / Temp / Logs / GPUCache / Code Cache) is now
  judged as "safe to delete, rebuilt automatically" even when its parent app is a confirm-tier app,
  so "Chrome Cache" no longer reads as "do not delete the whole folder".

- Path inspection is now a guided walkthrough instead of a number dump:
  1. a plain-language verdict (safe to delete / do not delete / do not delete as a whole) plus what
     the folder actually is and what happens if it is removed;
  2. what is inside: file types with a one-line explanation and advice per type (database, config,
     program files, logs, media ...), and the largest files;
  3. child folders with their own classification and per-folder "Inspect" / "Clean this" actions
     (cache-like names are flagged);
  4. how to verify it yourself (open the folder, sort by size, official in-app cleanup paths);
  5. next steps: open folder in Explorer (/reveal), preview & quarantine, or clean a single cache child.

## 2.4.5

- The lazy-clean card stays on the overview after a cleanup: it used to disappear once nothing
  qualified for cleaning, which made the feature look gone. It now shows the running progress,
  the last result (planned vs actual) with undo, or an explicit "nothing to clean" state.

## 2.4.4

- Lazy clean no longer silently moves less than advertised: if a whole-folder move fails because
  files are in use, it now moves the remaining files one by one, skips locked ones, and reports
  "计划 X → 实际 Y（跳过 N 个占用中的文件）" with a per-folder details table.
- Overlapping folders (parent + child) are de-duplicated in plans, so the estimate is not counted twice.
- Global action pill in the nav: scanning or cleaning progress is visible on every page, so leaving
  the progress page no longer feels like losing the operation.
- Lazy clean progress is now byte-based (moved / planned) with live file count and skipped count.
- Fix: incremental scan could reuse stale data for changes made right before the scan (dirty markers
  arrive only on the next USN poll). It now waits for a fresh poll before reusing subtrees, and only
  clears dirty markers recorded before the scan started.

## 2.4.2

- Scan progress page redesigned to feel responsive rather than stuck: live file counter with
  smooth number animation, gradient bar with shimmer, scrolling current-file line, a rolling list
  of recently scanned files, and live speed / elapsed / ETA.
- Progress and ETA are now based on the previous scan's file count; the old byte-based percentage
  jumped to ~43% in the first seconds and made the wait feel worse.

## 2.4.1

- The capacity card showed a hardcoded "系统盘 C:" even when the scan root is another drive; it now uses the actual drive letter.
- When a scan exists but no safe-tier folder qualifies, the overview said "no scan yet"; it now says the scan is complete and nothing safe needs cleaning.
- README screenshots (synthetic demo data) and CONTRIBUTING added.

## 2.4.0

First public release.

- Release assets: diskmon-go.exe (self-signed, Windows GUI, 8.5 MB) and a portable zip.
- Tray monitor with USN-journal attribution; reversible quarantine cleanup (7-day undo).
- Presets, scheduled lazy clean, parallel scan and incremental rescan (100 s -> 2.1 s on the reference machine).
- Web UI on localhost: 6 pages, sortable/searchable tables, CSV export, light/dark theme, English/Chinese.
- MCP entry point (diskmon-go.exe mcp) exposing read-only and reversible tools.

## 2.3.6

- Unknown paths returned 200 + overview; now a 404 page.
- ?lang=en was ignored when a language cookie existed; explicit query now wins.
- MCP diskmon_inspect on a missing path reported 0 bytes; it now errors.

## 2.3.2 - 2.3.5

- English UI completed: 256+ phrase entries, single-character keys removed, whole-sentence
  translations for plan/risk cards, settings, export, media, rules and trend pages.
- Dynamic "plus N more" rows localized via nFormat.

## 2.3.0


- English UI: per-request language (cookie / `?lang=en` / `Accept-Language`), nav toggle `EN / 中文`.
  A phrase dictionary translates the chrome (nav, overview, settings, cleanup, rules, categories, tiers,
  table headers) and key dynamic sentences have explicit English branches.
  Data (alert bodies, log lines) stays in its original language.

## 2.2.0

- MCP entry point: `diskmon-go.exe mcp` speaks MCP over stdio (JSON-RPC, newline-delimited).
  Tools: status, health, alerts, inspect, plans, quarantine_list, clean_lazy, restore.
  Permanent deletion (rule-run / purge) is intentionally not exposed; policy belongs to the client.

## 2.1.0

- Incremental scanning (clean-subtree reuse): top-level subtrees with no USN events since the
  last scan are reused; dirty subtrees are rescanned in parallel and merged.
  Reference run: full scan 100 s → incremental 2.1 s (12 of 13 subtrees reused).
- Safety rails: cache is bound to the process session, invalidated on USN gaps/renames,
  expires after 6 h, and a full scan is forced every 24 h for reconciliation.
  UI shows the data source and offers "force full scan" (`/scan?full=1`).

## 2.0.0

- Parallel scan: top-level subtrees are scanned by 3 workers and merged.
  Full C: scan on the reference machine went from ~107 s to ~60 s.
- Instant rescan: when the USN tailer has seen no writes since the last scan, `scan` returns
  the cached result immediately (0.1 s) instead of walking the disk again.
- Fixed folder paths missing a separator when scanning subtrees, and depth semantics
  (`dirs3` / `deep`) that had shifted for subtasks.

## 1.9.2

Security / robustness fixes found by an explicit test pass:

- Fixed open redirect in `/theme?back=` (now only same-site paths; `//evil.example` rejected)
- Fixed `/media/dir?dir=...` accepting arbitrary paths: only folders from the media scan are allowed
  (previously `/media/dir?dir=C:\` walked the whole drive and blocked the request for minutes)
- CSV export now neutralises formula injection (`=`, `+`, `-`, `@` prefixes get a leading `'`)
- Added tests: dangerous rule roots refused, CSV cells, redirect targets, rehearsal budget

## 1.9.1

- Scheduled lazy clean: run the conservative tier automatically every day or every Sunday at a chosen hour
  (`lazy_auto` / `lazy_auto_hour`, default off). Runs at most once per day, recorded as `lazy-auto`.
- Fix: the HTTP handler used the config snapshot taken at startup, so pages kept showing old thresholds
  after saving settings. Every request now refreshes the config.

## 1.8.0

- UI consolidation: 20 detail section pages collapsed into 6 groups (capacity / trend / cats / files / clean / system);
  old section URLs still work and map to their group.
- Overview: six duplicate tiles replaced by three ordered actions (scan → lazy clean → check trend);
  redundant "detail" entries removed (21 links → 15 on the overview).
- Plain-language navigation: 规则 → 自动清理, 体检 → 查路径, 详情 → 全部数据.
- Risk markers in the nav: green dot = read-only, amber dot = moves/deletes files.

## 1.7.0

- Light theme with a nav toggle (cookie-persisted), full palette override
- CSV export for dirs / files / apps / duplicates / media / alerts / quarantine (UTF-8 BOM for Excel)
- Responsive layout: grids collapse, tables scroll, sidebar turns into a horizontal strip under 820px

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
