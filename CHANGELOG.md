# Changelog

All notable changes to this project. Versions follow `0.x → 1.x` during early development;
the first public release is 1.4.x.

## 2.6.4

Two things that had been reporting success while doing nothing: a save button that answered
"a rule with this name already exists" when there was no duplicate, and a scan cache that was
written and then never believed by the next process.

- **Saving an edited rule now reaches the handler.** The edit page never told the server
  which rule it was editing: `serve.go` read `PostForm("editing")`, the form had no such
  field, so every save was treated as a new rule and collided with the same-name check. The
  message said 已存在同名规则 and there was no duplicate to be found — the check was right,
  the input had simply never been sent. The hidden field is emitted only when the page really
  is editing an existing rule: sending it unconditionally would carry a name that does not
  exist in create mode and push the backend into its rename-rejection path, which is a
  different failure. It has to sit *inside* the `<form>` — outside it the browser does not
  submit it, and the page looks fixed without being fixed. The test renders the edit page,
  pulls the field back out and drives the same values through `saveRuleFromValues`, so the
  form side and the handler side are covered together; deleting the field turns it red.
- **The MFT result is kept, and a second process can reuse it.** MFT used to return without
  writing anything, so an elevated `scan` re-read the whole `$MFT` — about 1.1 million
  records — on every run. The snapshot is now written, and a snapshot with no sub-trees is
  deliberately *not* accepted as a walk cache: the walk path would then scan every top-level
  subtree, an order of magnitude slower than reading the MFT. Any dirty subtree invalidates
  the whole snapshot, because a volume-wide result has no per-subtree keys to reuse.
  - Across processes the in-process USN delta means nothing, so the cache carries the volume
    journal's coordinates — `JournalID`, `First`, `Next` — plain numbers, and therefore
    persistable. Reuse requires the journal id to match, and then the records between the two
    coordinates to contain nothing that counts as a change (below). A recreated journal, a USN
    that went backwards, an anchor pushed out of the journal window, coordinates that cannot be
    read, or a delta with a change in it all mean "unknown" and a rescan. The anchor is stored
    only when the coordinates before and after the scan agree *and* nothing between them counts
    as a change; otherwise the snapshot is kept, but only the process that produced it may use
    it.
  - **What counts as a change, and what does not.** Exactly one reason bit is exempt: `CLOSE`
    (0x80000000). Windows writes a USN entry only when something actually changed — opening a
    file, reading it and querying attributes produce no entry at all — so a record carrying
    nothing but `CLOSE` says the file was closed and nothing else happened to it; reason bits
    accumulate by OR, so a write cannot hide behind it. Every other bit counts: basic info
    change (mtime feeds `DayWrites` and `TopFiles`), security change (owner, so `Owners`
    drifts), hard-link change (`Shared` / `Unique` change meaning), reparse-point change (what
    a walk sees changes), and the rest — indexable, object id, integrity, compression,
    encryption, stream, transacted, desired storage class — which rarely move a byte but are
    also rare, so exempting them would buy almost nothing while still costing something. An
    unrecognised bit counts too: the test is `reason &^ CLOSE != 0`, and the default answer is
    "changed".
  - **When it cannot tell, it rescans.** That is the line the relaxation rests on. Coordinates
    equal means no record at all and no read needed; anything else is read record by record,
    and a read that fails, a record it cannot parse (length out of range, shorter than the
    60-byte header, not 8-byte aligned, a version other than 2, a name field outside the
    record, half a record at the tail), a read it needed but did not perform, more than 20,000
    records in the delta, or a cursor that did not advance are all "unknown" and all a rescan.
    The structural checks are untouched: incomplete or invalid anchor, untrustworthy current
    coordinates, journal id changed, USN going backwards, anchor pushed out of the window.
  - **What this still cannot catch**, in the order it matters. First and by far the largest:
    the journal is only as good as NTFS accounting. A write that bypasses NTFS — a raw-sector
    write, an edit made while the volume is mounted offline, some imaging tools — leaves no USN
    record, so what is really being checked is "the journal saw nothing", not "the disk did not
    change", and no comparison of journal records can fix that. Second: if the journal is
    purged during the few milliseconds spent reading `[anchor, now)`, the kernel can return
    fewer records without an error, and USN values are not guaranteed to be contiguous, so a
    missing middle cannot be detected from the records alone. It takes the anchor sitting at
    the very edge of the window and an overflow inside that read; closing it would need one
    more coordinate query afterwards, checking the journal id is unchanged and
    `First <= anchor.Next` — about ten lines and one extra syscall, not part of this change.
    Third: the gain is smaller than the name suggests. With only `CLOSE` exempt, a single real
    write during the scan window still forces a rescan, so "busy machine, second scan
    re-reads" remains true; what changed is that close-only noise no longer vetoes. Applying
    the deltas to the snapshot instead would be a different piece of work. Fourth: 20,000
    records is a hard cap, and hitting it means a rescan — hit rate traded for judgement cost,
    on purpose. Fifth: record timestamps are not checked; the decision rests on reason bits
    alone, and a timestamp would only add another input that cannot be trusted. Sixth, and in
    the safe direction: named-stream writes, compression / encryption / integrity changes and
    ACL changes each throw the whole snapshot away even when they never moved a number we
    report.
- **The cached snapshot was sharing its maps and slices with whoever held it.** Reading it
  back used `cp := *sc`, a shallow copy: `Cats`, `Deep`, `TopFiles` and friends pointed at
  the same backing arrays, so a caller sorting or accumulating the result was editing the
  cache in place, and the next reuse handed out polluted numbers. Writing had the same hole
  from the other side — the cache stored `sc` itself, while the caller still holds that
  value. Both sides now store and return a clone.

## 2.6.3

The app window stopped being a browser tab wearing a shortcut's clothes: closing it no
longer took the server down, it remembers where it was, and it stopped growing a pixel per
launch. All of this release is that one sentence, taken apart.

- **`open` starts the server when nothing is listening.** The desktop shortcut runs
  `diskmon-go open`, and `open` only pointed a browser at `http://127.0.0.1:<port>` without
  asking whether anything answered there — so after a reboot, or after the process was
  killed, a click produced "can't reach this page" and no explanation. The user's word for it
  was "又进不去了", the "又" meaning it had happened before. It now probes the port, spawns
  `serve` detached if nothing answers, waits up to 5 s, and then opens the window. If the
  server still will not come up it opens the static report instead and says so, so a click
  always produces something readable rather than a dead tab.
- **Closing the window leaves the tray running.** `open` launches `tray` rather than a bare
  `serve`, which is what "close to tray" is supposed to mean; one left click on the icon
  brings the window back. The title is 磁盘监测 instead of "diskmon", because the taskbar
  name comes from the page title and this was the only place it could change.
- **Browser shortcuts are off inside the app window** — F5, Ctrl+R, Ctrl+U, F12,
  Ctrl+Shift+I/J/C and the context menu. Scoped by `?app=1`, which `open` appends: a normal
  browser tab still reloads, because reloading is how you debug the page.
- **The window remembers its position and size.** Edge's `--app` window remembers neither on
  its own, so the page reports geometry to `/api/geom` and it is replayed as
  `--window-position` / `--window-size`. The endpoint answers 200 or 400 with a reason now
  instead of a bare 204 — the 204 had been covering a real failure: the file was written
  next to Edge's `ui-profile`, a directory that belongs to the browser and answered "Access
  is denied".
- **Dragging the window is recorded, not just resizing.** Browsers have no `move` event, so a
  position change was invisible to the page; there is now a 2 s poll that compares position
  and size and reports only when they changed, plus a last report on `pagehide`.
- **Both processes read the same geometry file.** The launcher and the server can be
  different executables with different data directories, and each had kept its own
  `ui-window.json` — the geometry one process wrote was never the file the other read back,
  which looks exactly like nothing being saved at all. Prefer the user-level location, fall
  back to the data dir when it is not writable, and let the reader try both candidates
  instead of giving up on the first miss.
- **The window no longer grows one pixel per launch.** Edge applies `--window-size` and then
  reports `outerWidth` with a one-pixel frame difference, so storing the reported value and
  feeding it back made the window a little wider and taller every time it opened — ten opens
  a day is 300 px a month. A size within 4 px of the stored one now counts as unchanged and
  keeps the stored value; a real resize is far more than 4 px, so it still lands.
- **The category page's app button pointed at a route that does not exist.** The `confirm`
  tier linked to `/apps`, which no handler serves — a 404 behind a button sitting exactly
  where the next step should be, shipped with the category filter in 2.6.2 and fixed one
  release later. It now goes to `/clean#appgroups`, and the cleanup centre's per-app card
  carries the `appgroups` anchor to land on. That page also dropped `target="_blank"`:
  inside an `--app` window every click was opening another top-level window, and "this feels
  like browsing" came from exactly that.

## 2.6.2

Category breakdown became navigable, in three steps.

- **Report: category rows expand in place.** Clicking one shows the largest paths inside that
  category, what its tier means, and the one entry point that is actually correct for it. The
  report is a static snapshot and cannot delete anything, so the panel gives evidence, judgement
  and a link into the server rather than pretending to be a one-click delete.
- **`/clean?cat=<category>` scopes the cleanup center to one category.** Same content, live, with
  the right action per tier: `clean` → lazy clean, `confirm` → per-app view, `adjust` → "do not
  delete by hand" plus the tool that actually owns it (DISM, powercfg, the uninstaller), `keep` →
  no button at all. Unknown or empty `cat` falls back to the full page instead of rendering blank.
- **Category chips on both pages.** Before this the filtered view was reachable only by following a
  link out of the report: you could go back to everything, but not sideways to a neighbour, and the
  cleanup center never mentioned a per-category view existed. Sorted by size, current one
  highlighted, `全部` shown only while filtering.
- **The overview now explains why `已用` and the scan total differ.** Two numbers sitting side by
  side get subtracted by the reader, and a ~9 GB gap with no explanation reads as broken arithmetic
  — in a cleanup tool that is exactly the wrong impression. It states the gap, attributes it
  (NTFS metafiles + directories the scan cannot enumerate), and says outright that it is expected.

Two wording bugs found by reading rendered output, not by tests: `sc.Deep` holds path *prefixes*, and
a depth-4 prefix of a deep file path **is** the file — labelling that list "最大的目录" filed
`msedge.dll` under a heading claiming it was a directory. And a category with no large path was
described as "大量零散文件凑出来的", which is false for `pagefile.sys`: one 11 GB file whose depth
prefix never reaches the threshold. Both now say what the data actually supports.

## 2.6.1

- **`scan` now uses the MFT engine by default**, with `scan --walk` as the escape hatch.
  **The daemon paths (`watch` / `tray` / `serve`) deliberately keep it off**, and this was caught the
  hard way. The MFT branch returns early on success — it does *not* write the incremental cache. Put
  it in `ScanRootCached` for everyone and a 5-second watcher loop becomes "read the whole MFT every 5
  seconds", i.e. every tick does a full scan. That is a slowdown dressed as a speedup. The
  optimisation belongs to the one-shot command someone is waiting on; the daemons keep their USN
  cache.
  - Verified baseline (walk path, measured 3× today): **52.4–52.8 s** for `C:`, reporting
    241,874 dirs / 158.25–158.27 GB. Unprivileged behaviour is otherwise unchanged.
  - The MFT figure through `scan` itself is **not yet measured** — that path only runs elevated.
    The ~5 s previously seen came from a diagnostic mode that does extra work. Treat the speedup as
    "large, unquantified until someone runs an elevated `scan`" rather than quoting a ratio.

Everything here came out of one question: *why does the MFT engine report 247,800 directories when
the directory walk reports 258,118?* Chasing it produced five fixes and one retracted premise.

- **Retracted: "directory count must match the walk."** That was the shipping criterion for `--mft`
  and it was wrong. A new `walk-count` subcommand measured the walk on this machine at two
  privilege levels: **241,879 directories as a normal user, 258,118 elevated — a 16,239 difference
  for identical code on an identical volume.** The count is not a fact about the disk; it is a
  function of who is asking, because `$Recycle.Bin`, `System32\config` and `System Volume
  Information` are unreadable without elevation. MFT reads filesystem records and never consults an
  ACL, so it lands **between** the two: 247,800. Byte totals are the stable, independently
  checkable quantity instead — walk 158.27 GB vs MFT 157.10 GB, 0.74% apart, which is the size of
  the known hard-link under-count.
- **`walkStats` reported nothing about its own blind spots.** `filepath.WalkDir` hands back an error
  for a directory it cannot read, and the callback returned `SkipDir` *before* `dirs++` — so an
  unreadable directory was neither counted nor reported, and its whole subtree vanished silently.
  Now split into `walkStatsFull` (returns a `walkResult` struct carrying `unreadable` and
  `skippedReparse`) with `walkStats` kept as a thin compatibility wrapper, so no existing test
  changed. Measured here: 560 unreadable directories, 1 junction.
- **New `walk-count [drive]` subcommand.** Runs the walk only, needs no elevation, and prints the
  count together with the two reasons that count is a **lower bound**. This exists so the next
  person can ask "which side is wrong?" instead of assuming it is MFT.
- **`scan --mft` and `mft-dump` disagreed about the directory count** — 222,980 vs 247,800, with the
  24,820-record difference unmentioned. `scan` now prints the caveat inline rather than silently
  reporting a smaller number than its own diagnostic mode.
- **`$BadClus` was inflating the metadata figure to 204.76 GB.** It is a sparse file whose logical
  size equals the *whole volume*, so summing it produced "205 GB of your 200 GB disk is NTFS
  metadata" — a claim that would actively mislead anyone deciding what to delete. It now counts as
  one file and contributes no bytes.
- **The `$`-prefix metadata rule existed in two places.** `isMetadataRecord` used a name whitelist;
  `buildPaths` still used `strings.HasPrefix(name, "$")`, which swallowed `$Recycle.Bin`'s entire
  subtree. Fixed at the second site too, keeping only the whitelist. Found by reading the code, not
  the data — the data had already been explained away once.
- **Extension records now contribute `$DATA` sizes.** `parseExtData` recovers the real size from
  records whose base reference points elsewhere; measured recovery was 7,613 files / 20.94 GB, and
  MFT's total went 137.25 → 157.10 GB. Spot-checked against `hiberfil.sys`, which had read as 0 KB
  and now reads 6.08 GB.

## 2.6.0

- **Scan results now carry two size figures instead of one.** `Total` stays what it always was —
  the sum over every path, the same thing Explorer shows. `Shared` is the part we could prove is a
  hard link, and `Unique` (= `Total - Shared`) is the figure that may be compared against the
  volume's used space. This is the fix for `C:\Windows\WinSxS` reporting 18.46 GB: Microsoft's own
  WinSxS documentation gives the same shape of error ("4.98 GB reported vs 507 MB of actual cost"),
  and WizTree / WinDirStat / `du -u` / czkawka / Dism++ all de-duplicate while we did not.
  - Detection uses volume serial + file index via `GetFileInformationByHandle`.
  - To keep scanning fast, only files ≥ 8 MB are probed (`hardlinkProbeMin`), so `Shared` is a
    **lower bound** — smaller hard links are not caught. Every surface that shows the number says
    so; presenting an unbounded "physical size" would just be a new inaccuracy replacing the old one.
  - The identity `Unique + Shared == Total` is enforced by construction and covered by a test.
- **Categories are de-duplicated too, not just the grand total.** `CatsShared` records, per category,
  how much of its figure is a hard link counted twice; the report now reads
  "WinSxS 组件库（其中 2.73 GB 与系统目录共享）" and the tooltip gives the de-duplicated value
  (18.46 → 15.73 GB here). `Cats` stays path-summed so it still adds up to `Total`.
  The reclaimable-share card also switches to de-duplicated bytes — the same data cannot be
  reclaimed twice, and letting hard links inflate "can be freed" would over-promise exactly where
  users act on the number. Measured on this machine: shared across categories sums to 6.35 GB,
  matching the global `Shared` exactly.
  - Still path-summed: the per-directory lists (`dirs3` / `deep`). Those need a `Shared` field on
    `DirEntry` to be consistent, which is a separate change.
- **Files sitting in the scan root are now counted.** `ScanRootCached` only ever walked top-level
  *directories*, so `pagefile.sys`, `hiberfil.sys`, `swapfile.sys` and friends were invisible. On this
  machine that was 18.09 GB — the scan reported 137.87 GB against a volume using 165.47 GB. With the
  root files included the same scan reports **155.98 GB** (155.96 GB by an independent Python walk).
  They get their own categories (`system_pagefile` / `system_hiber`), so the "adjustable" tier stops
  being wrong about them.
- **Removed a misleading dead function.** An earlier attempt at work-stealing lived in
  `scanRootParallel`, which nothing called — the real path is `ScanRootCached`, whose per-top-level-child
  structure the incremental cache depends on. Deleting it is better than leaving something that looks
  like the scan path but isn't. Real work-stealing has to keep the child-level cache keys valid, so it
  is its own piece of work.
- **winapp2.ini-compatible parser (`winapp2.go`, `diskmon-go.exe winapp2 <file>`).** Community rule
  files can now be read and resolved: `%Var%` expansion, `RECURSE` / `REMOVESELF` flags, registry-only
  sections reported and skipped, `ExcludeKey` counted, unknown variables left unresolved instead of
  guessed, and paths that resolve to a drive root or a system root flagged as out of bounds.
  **It deliberately does not delete anything** — a community rule file has no business driving
  deletions without review, so this only produces a report.

- **Quarantine is now described as a logical delete.** SECURITY.md states plainly that moving a file
  into quarantine does not erase the data — bytes remain in free space, previous versions and
  snapshots — and cites Gutmann (USENIX Security '96) and Wei et al. (FAST '11) on why overwriting
  is not reliable erasure on modern media, SSD included.
- Tests: 6 new cases, including an end-to-end `scanVolume` test on a real NTFS hard link that asserts
  all three figures.

- **Scan work is stolen, not pre-assigned.** `ScanRootCached` used to hand each top-level subtree to
  one of three workers, so on this machine `Users` (58 GB) and `Windows` (40 GB) made the slowest
  worker the finish line. It now splits each subtree into its direct subdirectories and lets workers
  pull from a single queue, then merges the pieces back into **one Scan per top-level subtree** — the
  incremental cache is keyed by subtree, so that regroup is not optional, it is the whole contract.
  - Measured: 55.6 s → 53.1 s / 53.4 s. That is ~4%, which is inside run-to-run noise. I predicted
    15–30% and was wrong: this disk was already saturated at three readers, so removing tail
    imbalance bought almost nothing. The real lever is reading the MFT, not re-shuffling the walk.
  - **The bug this introduced, and the fix.** Splitting the scan also split the hardlink identity
    table, so `WinSxS` and `System32` landed in different workers and cross-directory duplicates
    stopped being found — `Shared` silently fell from 6.35 GB to 3.98 GB. The identity table is now
    scan-scoped and shared (`beginHardlinkScope`), `observe` only reports *whether* a file is the
    second name, and each shard books its own duplicate bytes into its own `Shared`/`CatsShared`.
    `Shared` is back to 6.36 GB with per-category totals summing to it exactly.
- **Test count: 66.**

- **`mft-dump`: an NTFS MFT reader that reconciles against the walk (`mft.go`).** Opens `\\.\X:`,
  asks `FSCTL_GET_NTFS_VOLUME_DATA` for the geometry, then follows `$MFT`'s *own* data runs — the MFT
  fragments, so a contiguous read from `MftStartLcn` silently stops at the first fragment — applies
  update-sequence fixups, parses `$FILE_NAME` (parent + name, preferring Win32 over DOS) and `$DATA`
  (real size), and rebuilds paths upward from record 5.
  - **It is deliberately not wired into scanning.** The whole point is to prove the numbers agree
    with `WalkDir` *before* anything trusts it, and that comparison needs administrator rights.
  - **Not verified on this machine.** This session is not elevated, so `CreateFile("\\.\C:")` returns
    `Access is denied`; `mft-dump` prints the reason and exits without touching normal scanning. Both
    paths behave, but the reader itself has only ever run against synthetic records — **never against
    a real MFT**. Treat the speed claim as unmeasured until someone runs it elevated.
  - 15 unit tests cover what a volume is not needed for: `NTFS_VOLUME_DATA_BUFFER` parsing, signed
    data-run decoding (including a negative delta), record headers, update-sequence fixups,
    in-use / extension-record / bad-magic rejection, DOS-vs-Win32 name preference, truncated buffers,
    path reconstruction, and a cyclic parent reference that must not hang.
  - To verify: `diskmon-go.exe mft-dump C:` from an elevated prompt.
  - **First real run (elevated) — speed confirmed, numbers still apart.**
    Reading the MFT took **2.61–2.71 s** against **84–108 s** for the walk on the same volume:
    **32–40×**. That part is settled.
  - **Fixed and confirmed: NTFS metadata was in the total.** `$BadClus` is a sparse file whose
    logical size equals the whole volume, which is how a 200 GB disk "contained" 334.60 GB. Metadata
    records are now excluded and reported separately — the rerun shows **54,223 records / 203.01 GB**
    excluded, i.e. very close to one full volume, as expected.
  - **A theory of mine that the data killed.** I blamed the walk baseline's 178.57 GB on
    `C:\Documents and Settings` being a junction onto `C:\Users`, and added a reparse-point skip.
    With that skip in place the walk still reports **178.67 GB** — the number did not move, so
    junctions were never the cause. The skip stays (it is correct anyway), but the explanation was
    wrong and is recorded here as wrong.
  - **Where it stands now, and why I stopped.** Measured on the same machine, three ways:
    walk **178.77 GB** / 754,867 files, scanner **156.00 GB** / 685,686 files,
    MFT **138.48 GB** / 665,765 files. The MFT sits *below* the scanner, so it under-counts.
  - **One hypothesis tested and confirmed — and the target missed anyway.** Sizes used to be read only
    from the `$DATA` attribute, so a file whose data stream had overflowed into an extension record
    (attribute list) contributed 0. Sizes now come from `$FILE_NAME.RealSize`, which survives that
    overflow. The rerun recovered **14,764 files / 4.94 GB**, and the total moved 133.51 → 138.48 GB —
    exactly the predicted amount. The mechanism was real; it simply was not the whole gap. The agreed
    acceptance target was 149.64 GB (the scanner's de-duplicated figure). 138.48 GB misses it by
    11.16 GB, so work on this stops here by prior agreement rather than turning into another round.
  - **What the rerun does narrow.** Every record that parsed landed in exactly one bucket
    (`dirs 222,723 + files 665,765 + metadata 54,223 = 942,711` parsed), with `Unresolved = 0`. So
    nothing leaks between parsing and counting, and the remaining gap is a **per-record size**
    question, not a missing-files question. It does *not* prove that no in-use record failed
    `parseRecord` — the 161,673 rejected slots are not separated into free vs. failed, and that
    counter should be split before anyone trusts this further.
  - **One place the MFT beats the walk.** It sees `C:\System Volume Information` (3.09 GB of shadow
    copies here), which a directory walk cannot read at all. Part of the disagreement is the MFT being
    *more* complete, not less — so "match the walk exactly" was never the right acceptance target.
  - **Not wired into scanning.** Still true, and it stays true: a 44× faster scan that is 11 GB short
    would be a worse tool than the slow correct one. The subcommand remains read-only and opt-in.
  - **Two diagnostics added so the next run answers instead of guessing.** Neither changes any
    measurement made so far; both make the next one decisive:
    - `空闲槽位` and `解析失败` are now counted separately (`inUseRecord` reads only the record header).
      They used to be one `dropped` figure, which left "do any *used* records fail to parse?" — the
      single hypothesis that could explain a large under-count — permanently unanswerable.
    - `mft-dump` now buckets both sides by top-level directory and prints the 8 largest differences,
      so a single run localises the remaining gap to a subtree instead of another round of guessing.
  - **Corrected an acceptance target of my own.** "Match the directory walk exactly" was wrong from the
    start: the MFT can see `System Volume Information` (3.09 GB of shadow copies here) and a walk cannot.
    The next comparison should be against a defined figure — the scanner's de-duplicated total plus the
    walk's blind spots — not against the walk.
  - **The parse-failure hypothesis is dead, and it died cleanly.** Splitting the counter showed
    **57,612 extension records + 4 genuine oddities** out of 57,616. Attribute-list overflow records are
    normal NTFS structure — their size belongs to the base record, so skipping them loses nothing.
    Nothing is being dropped. (The earlier arithmetic, 11.16 GB / 57,616 ≈ 194 KB per record, was a
    coincidence. It looked like an answer and was not, which is exactly why it was not reported as one.)
  - **Where the remaining gap actually lives**, from the bucket comparison (MFT 138.80 GB against a
    178.88 GB walk, net −40.08 GB): `Windows` −24.01, `Users` −7.23, root files −6.08,
    `Program Files (x86)` −4.27, `Program Files` −1.41, `ProgramData` −0.48, and
    `System Volume Information` **+3.41** — the MFT being *more* complete there, since a walk cannot
    read shadow copies at all.
  - **The sharpest lead: `hiberfil.sys` is absent from the MFT entirely.** Root files total 12.02 GB
    against the walk's 18.09 GB, and the 6.08 GB difference is exactly hiberfil.sys's size — a file
    that would rank second in the tool's own largest-entries list. It is not a size error; the record
    never arrives. `Unresolved = 0` rules out the path-resolution bucket, so the next check is whether
    it is being misfiled as an extension record, or sits outside the range we read.
  - **Fixed: `$DATA` living in an extension record is now followed, not dropped.** This is the one
    that moved the needle. A file whose data stream overflowed into an extension record (`$ATTRIBUTE_LIST`)
    used to contribute 0 bytes unless `$FILE_NAME` happened to carry the size. Extension records were
    being thrown away outright. Now the scan keeps their `$DATA` size keyed by base record and backfills
    any file still sitting at 0. Measured on `C:\`: **7,610 files / 23.65 GB recovered in one run**, total
    **137.25 → 158.51 GB**. `C:\hiberfil.sys` — the 6.08 GB sample that exposed this — reports 6.08 GB.
  - The reconciliation gap collapsed with it: total **−40.08 → −15.51 GB**; `C:\Windows` −24.01 → −11.69,
    `C:\Users` −7.22 → −1.67, `Program Files (x86)` −4.27 → −4.18.
  - **What is left is probably not a bug.** The remaining `Windows` gap has a benign explanation: this
    tool's own hard-link figure (6.36 GB) only probes files ≥ 8 MB, so it is a *lower bound* — and WinSxS
    is mostly small files. An MFT record counts once; a path-summed walk counts per name. `Links` per
    record is now tallied (`硬链接数>1 的记录 … 按路径求和会多算 …`), which measures that difference
    directly instead of assuming it.
- **Test count: 85.**

## 2.5.8

- **Lazy clean can be paused, resumed and stopped.** While it is running, both the lazy page and
  the overview card show Pause / Continue / Stop. Pause takes effect before the next file; Stop
  waits for the file in progress, keeps everything already moved in the quarantine (still undoable),
  and reports how many folders were left untouched. A stopped run is labelled as stopped instead
  of looking like a finished one.
- **Plan vs actual is now honest.**
  * The "plan" is re-measured from the filesystem right before cleaning instead of reusing a scan
    snapshot that may be hours old (folders that exceed the 4 s measurement budget keep the
    snapshot value, and the page says so).
  * Results list planned / actual / difference per folder, and the summary explains the gap:
    more than planned means caches kept growing during the run; less means files were locked and
    skipped.
  * Quarantine now measures size without opening every file for a lock check first — much faster on
    large caches — and lock detection still happens when a file cannot actually be moved.
- Fixed the completion alert reporting the failure count as the skipped-file count.
- **Pause and stop now work on cross-volume quarantine too.** When the quarantine lives on another
  drive (the common case), files are streamed in 1 MB chunks with a pause/stop check before each
  chunk and live progress, instead of one opaque `PowerShell Move-Item` per folder that could not be
  interrupted. Source folders left empty by a per-file move are removed.
- **The running page distinguishes the three states instead of looking the same.** Paused shows a
  yellow bar, "已暂停" plus 继续清理 / 停止清理; stopping shows "正在停止…" and jumps to the result as
  soon as the run ends. Both actions take effect within roughly one file (unit-tested: a 5 MB copy
  stops mid-stream and the half-written destination is removed).
- **Single-instance handling fixed.** `CreateMutexW` returns a valid handle even when the named
  mutex already exists; only `ERROR_ALREADY_EXISTS` says another instance is running. The old check
  let a second copy start a second tray and fight over the port. Now an existing instance makes the
  new process open the dashboard and exit — so double-clicking the shortcut always "opens" it.
- Tests: pause/resume/stop gate, measured-plan behaviour, planned-vs-actual explanations.

## 2.5.7

- **Security fix: cross-site requests are blocked.** The dashboard only listens on 127.0.0.1, but
  any web page the user has open can still send requests to it. Requests carrying
  `Sec-Fetch-Site: cross-site`, or an `Origin` that is not our own, are now rejected with 403 —
  so a random page can no longer trigger cleanups. Command-line and MCP clients (no such headers)
  are unaffected.
- **Permanent deletion now requires POST.** `/q/purge` (drop expired quarantine items) and the
  L2 rule "delete for real" confirmation were plain GET links: a refresh, a prefetch or an external
  request could fire them. Both are now form buttons; GET gets 405.
- Trailing-slash URLs such as `/clean/` now redirect (301) instead of 404.
- Loading overlay in English mode says "Working…" instead of "正在Handle…".
- Tests: cross-site rejection, POST-only purge, trailing-slash redirect, origin matching.

## 2.5.6

- **"今日建议" rewrite.** The card used to be a fixed priority ladder, so it showed the same
  sentence every time you opened it. It now ranks by what is actually true today and states its
  evidence:
  * a basis line with scan freshness, today's net growth, 7-day median and days-to-full;
  * space pressure first (low free space, or less than 7 days left) — urgent;
  * stale data (>48 h) is called out instead of quietly serving old numbers;
  * a day whose growth is more than 2x the median (and over 500 MB) is surfaced with the multiplier;
  * what was already quarantined today is reported (with the reminder that the space only returns
    when quarantine is purged), so the same suggestion is not repeated;
  * at most three items, each with its own reason and action; a genuinely quiet day says so.
- **`diskmon-go.exe diag`**: writes a text bundle (version, elevation, config, last-scan summary,
  top categories/folders/files/owners, growth forecast, quarantine, recent alerts, log tail) so a
  bug report arrives complete.
- **Issue handling**: Issue Forms with required fields, automatic triage labels and first response,
  and auto-nudge/close of issues stuck on `needs-info`. New docs: `docs/FAQ.md` (the "this is not a
  bug" answers, including why freeing space needs the quarantine purge) and `docs/TRIAGE.md`
  (response cadence, priorities, what gets closed).

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
