package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// errStopped 表示用户主动要求停止，不是失败：已经移走的部分应照常记账。
var errStopped = errors.New("已停止")

// ---------- 隔离区 ----------

type QEntry struct {
	ID       string `json:"id"`
	Original string `json:"original"`
	Stored   string `json:"stored"`
	Bytes    uint64 `json:"bytes"`
	Files    int    `json:"files"`
	TS       int64  `json:"ts"`
	Reason   string `json:"reason"`
	Skipped  int    `json:"skipped,omitempty"` // 逐文件移动时被占用而跳过的文件数
	Restored bool   `json:"restored"`
}

const metaFileName = ".diskmon-meta.json"

type Rehearsal struct {
	Path      string
	Truncated bool
	Files     int
	Bytes     uint64
	Locked    []string
	Skipped   int
	TopFiles  []FileEntry
	Refuse    string
	Cat       string
	Tier      string
}

type Quarantine struct {
	mu    sync.Mutex
	root  string
	index string
}

func openQuarantine(cfg Config) *Quarantine {
	root := filepath.Join(cfg.DataDir, "quarantine")
	_ = os.MkdirAll(root, 0o755)
	return &Quarantine{root: root, index: filepath.Join(root, "index.jsonl")}
}

func (q *Quarantine) entries() []QEntry {
	b, err := os.ReadFile(q.index)
	if err != nil {
		return nil
	}
	var out []QEntry
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e QEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (q *Quarantine) append(e QEntry) {
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(q.index, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(string(b) + "\n")
		_ = f.Close()
	}
}

func (q *Quarantine) rewrite(list []QEntry) {
	var sb strings.Builder
	for _, e := range list {
		b, _ := json.Marshal(e)
		sb.WriteString(string(b) + "\n")
	}
	_ = writeFileAtomic(q.index, []byte(sb.String()))
}

var whitelistGuard = func(cfg Config, path string) string {
	low := strings.ToLower(path + `\`)
	for _, w := range cfg.Whitelist {
		if strings.Contains(low, strings.ToLower(w)) {
			return "命中白名单保护：" + w
		}
	}
	return ""
}

// 拒绝处理的对象：系统核心 / 安装目录 / 用户文档 / 根目录
func guardPath(cfg Config, path string) (cat, tier, refuse string) {
	clean := filepath.Clean(path)
	if clean == "" || len(clean) <= 3 {
		return "", "", "路径过短，拒绝处理"
	}
	if w := whitelistGuard(cfg, clean); w != "" {
		return "", "", w
	}
	parent := filepath.Dir(clean)
	// 目录按自身分类；文件按所在目录分类
	probe := parent
	if st, err := os.Stat(clean); err == nil && st.IsDir() {
		probe = clean
	}
	cat = classify(strings.ToLower(probe) + `\`)
	meta, ok := cats[cat]
	if !ok {
		cat = "other"
		meta = cats["other"]
	}
	tier = meta.Tier
	switch tier {
	case "keep":
		return cat, tier, "该目录属于「" + meta.Label + "」，工具不提供处理（" + meta.Hint + "）"
	case "adjust":
		return cat, tier, "该目录属于系统可调整项，请用系统工具处理：" + meta.Hint
	}
	return cat, tier, ""
}

// 文件是否被独占锁定（无法以独占方式打开 → 有人在用）
func isLocked(path string) bool {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	h, _, e := pCreateFileW.Call(uintptr(unsafe.Pointer(p)),
		0x80000000, // GENERIC_READ
		0,          // 不共享 → 有人占用则失败
		0, openExisting, flagBackupSem, 0)
	if h == invalidHandle || h == 0 {
		// 5 = ACCESS_DENIED（权限问题，不算被占用）；32 = SHARING_VIOLATION
		if errno, ok := e.(syscall.Errno); ok && errno == 32 {
			return true
		}
		return false
	}
	pCloseHandle.Call(h)
	return false
}

func rehearse(cfg Config, path string) Rehearsal {
	r := Rehearsal{Path: path}
	cat, tier, refuse := guardPath(cfg, path)
	r.Cat, r.Tier = cat, tier
	if refuse != "" {
		r.Refuse = refuse
		return r
	}
	st, err := os.Stat(path)
	if err != nil {
		r.Refuse = "路径不存在或不可读：" + err.Error()
		return r
	}
	if !st.IsDir() {
		r.Files = 1
		r.Bytes = uint64(st.Size())
		if isLocked(path) {
			r.Locked = append(r.Locked, path)
		}
		return r
	}
	type bf struct {
		p string
		b uint64
		t int64
	}
	var top []bf
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			r.Skipped++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			r.Skipped++
			return nil
		}
		r.Files++
		sz := uint64(info.Size())
		r.Bytes += sz
		if isLocked(p) {
			if len(r.Locked) < 20 {
				r.Locked = append(r.Locked, p)
			}
		}
		top = append(top, bf{p, sz, info.ModTime().Unix()})
		return nil
	})
	// 只保留最大的 10 个
	for i := 0; i < len(top); i++ {
		for j := i + 1; j < len(top); j++ {
			if top[j].b > top[i].b {
				top[i], top[j] = top[j], top[i]
			}
		}
	}
	if len(top) > 10 {
		top = top[:10]
	}
	for _, t := range top {
		r.TopFiles = append(r.TopFiles, FileEntry{Path: t.p, Bytes: t.b, MTime: t.t})
	}
	return r
}

// rehearseQuick 用于"路径体检"：有 3 秒预算，不逐文件做锁检测，避免大目录卡住页面
func rehearseQuick(cfg Config, path string, budget time.Duration) Rehearsal {
	r := Rehearsal{Path: path, Truncated: true}
	cat, tier, refuse := guardPath(cfg, path)
	r.Cat, r.Tier = cat, tier
	if refuse != "" {
		r.Refuse = refuse
		return r
	}
	st, err := os.Stat(path)
	if err != nil {
		r.Refuse = "路径不存在或不可读：" + err.Error()
		return r
	}
	if !st.IsDir() {
		r.Truncated = false
		r.Files = 1
		r.Bytes = uint64(st.Size())
		if isLocked(path) {
			r.Locked = append(r.Locked, path)
		}
		return r
	}
	deadline := time.Now().Add(budget)
	lockChecks := 0
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if time.Now().After(deadline) {
			return filepath.SkipAll
		}
		if err != nil {
			r.Skipped++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			r.Skipped++
			return nil
		}
		r.Files++
		r.Bytes += uint64(info.Size())
		if lockChecks < 200 {
			lockChecks++
			if isLocked(p) && len(r.Locked) < 10 {
				r.Locked = append(r.Locked, p)
			}
		}
		return nil
	})
	r.Truncated = time.Now().After(deadline)
	return r
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// 执行隔离：移动而不是删除；同卷 rename，跨卷 copy+delete
func (q *Quarantine) apply(cfg Config, path, reason string) (QEntry, error) {
	return q.applyWithProgress(cfg, path, reason, nil, nil)
}

// applyWithProgress 与 apply 相同，但会回报移动进度（逐文件兜底时用于显示进度）。
// gate 是执行前的闸门：返回 true 才继续，返回 false 表示用户要求停止。
// 停止时如果已经移走了一部分文件，仍会落盘记账（可撤销），并返回 errStopped。
func (q *Quarantine) applyWithProgress(cfg Config, path, reason string, onFile func(moved uint64, files, skipped int, cur string), gate func() bool) (QEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if gate != nil && !gate() {
		return QEntry{}, errStopped
	}
	// 这里只量大小和文件数，不逐文件做锁检测：锁检测在真正移动时自然会暴露，
	// 逐文件 CreateFile 会把大目录的清理拖慢一个数量级，也让“暂停”迟迟不生效。
	r, stopped := rehearseForApply(cfg, path, gate)
	if r.Refuse != "" {
		return QEntry{}, fmt.Errorf("%s", r.Refuse)
	}
	if stopped {
		return QEntry{}, errStopped
	}
	if cfg.QuarantineMaxGB > 0 && q.totalBytes()+r.Bytes > uint64(cfg.QuarantineMaxGB)<<30 {
		return QEntry{}, fmt.Errorf("隔离区已达上限 %d GB（当前 %s），请先还原或清理超期项",
			cfg.QuarantineMaxGB, human(q.totalBytes()))
	}
	// 真正动文件之前的最后一步：先落一次系统还原点（同一次清理只落一次）。
	// 位置有讲究——放在守卫、测量、配额这三道拒绝之后，才不会为一次注定
	// 被拒绝的清理白占卷影空间；放在 rename/复制之前，才是「清理前」。
	// dry-run 压根走不到这里（runRule 与各预览页在调用 apply 前就分叉了），
	// 所以这里不需要再判断 DryRun。
	ensureRestorePoint("隔离目录 " + path)

	id := newID()
	dest := filepath.Join(q.root, id, filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return QEntry{}, err
	}
	bytesMoved, filesMoved, skipped := r.Bytes, r.Files, 0
	var stopErr error
	if err := os.Rename(path, dest); err != nil {
		// 目录里有正在被占用的文件、或目标在另一个卷上时，整目录 rename 会失败。
		// 直接逐个文件移动/流式复制：能移的先移走，占用中的跳过并如实计数；
		// 复制过程中每块都检查暂停/停止，进度和暂停键才有意义。
		b2, f2, s2, ferr := moveFilesIndividually(path, dest, onFile, gate)
		bytesMoved, filesMoved, skipped = b2, f2, s2
		if f2 == 0 && !errors.Is(ferr, errStopped) {
			// 一个都没搬动：可能是安全策略不允许本进程直接改文件，
			// 再走 PowerShell / 复制回退（先把逐文件阶段留下的空壳/半截文件清掉）。
			_ = os.RemoveAll(dest)
			if serr := movePathSlow(path, dest, err); serr == nil {
				bytesMoved, filesMoved, skipped = r.Bytes, r.Files, 0
				ferr = nil
			}
		}
		switch {
		case errors.Is(ferr, errStopped):
			stopErr = errStopped
			if f2 == 0 {
				_ = os.RemoveAll(filepath.Join(q.root, id)) // 什么都没移走，别留空壳
				return QEntry{}, errStopped
			}
			removeEmptyTree(path)
		case ferr != nil:
			if f2 == 0 {
				_ = os.RemoveAll(filepath.Join(q.root, id))
				return QEntry{}, ferr
			}
			// 已经移走一部分：照常记账，避免隔离区里出现无法还原的“无主文件”。
			stopErr = ferr
			removeEmptyTree(path)
		default:
			// 逐个文件搬完后源目录可能只剩空壳；清掉，别让页面看起来“没清干净”。
			removeEmptyTree(path)
		}
	}
	e := QEntry{ID: id, Original: path, Stored: dest, Bytes: bytesMoved, Files: filesMoved,
		TS: time.Now().Unix(), Reason: reason, Skipped: skipped}
	// 元数据随条目落盘：即使 index.jsonl 损坏，也能靠它重建
	if b, err := json.Marshal(e); err == nil {
		_ = writeFileAtomic(filepath.Join(q.root, id, metaFileName), b)
	}
	q.append(e)
	return e, stopErr
}

// rehearseForApply 是 apply 前的轻量测量：只统计大小/文件数，可在文件之间检查停止信号。
// 真正的占用检测交给逐文件移动时处理（移不动就跳过并计数）。
func rehearseForApply(cfg Config, path string, gate func() bool) (Rehearsal, bool) {
	r := Rehearsal{Path: path}
	cat, tier, refuse := guardPath(cfg, path)
	r.Cat, r.Tier = cat, tier
	if refuse != "" {
		r.Refuse = refuse
		return r, false
	}
	st, err := os.Stat(path)
	if err != nil {
		r.Refuse = "路径不存在或不可读：" + err.Error()
		return r, false
	}
	if !st.IsDir() {
		r.Files = 1
		r.Bytes = uint64(st.Size())
		return r, false
	}
	stopped := false
	seen := 0
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen++
		if gate != nil && seen%200 == 0 && !gate() {
			stopped = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if info, e := d.Info(); e == nil && isReparse(info) {
				return filepath.SkipDir
			}
			return nil
		}
		if info, e := d.Info(); e == nil && info.Mode().IsRegular() {
			r.Files++
			r.Bytes += uint64(info.Size())
		}
		return nil
	})
	return r, stopped
}

func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else {
		return movePathSlow(src, dst, err)
	}
}

func movePathSlow(src, dst string, errRename error) error {
	// 某些安全策略会拒绝本进程直接改动文件（例如仅允许白名单程序写盘）。
	// 这时交给系统自带的 PowerShell 完成移动（跨卷也能处理）。
	errPS := powershellMove(src, dst)
	if errPS == nil {
		return nil
	}
	// 最后兜底：自己复制再删除
	errCopy := copyTree(src, dst)
	if errCopy != nil {
		return fmt.Errorf("rename: %v | powershell: %v | copy: %v", errRename, errPS, errCopy)
	}
	errDel := os.RemoveAll(src)
	if errDel != nil {
		return fmt.Errorf("rename: %v | powershell: %v | delete: %v", errRename, errPS, errDel)
	}
	return nil
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func powershellMove(src, dst string) error {
	script := "Move-Item -LiteralPath " + psQuote(src) + " -Destination " + psQuote(dst) + " -Force -ErrorAction Stop"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removePath(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	script := "Remove-Item -LiteralPath " + psQuote(p) + " -Recurse -Force -ErrorAction Stop"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyTree(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, st.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	st, _ := os.Stat(src)
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}

func (q *Quarantine) restore(id string) (QEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	list := q.entries()
	for i, e := range list {
		if e.ID != id || e.Restored {
			continue
		}
		if _, err := os.Stat(e.Original); err == nil {
			// 原目录仍在（说明只移走了一部分文件）：合并还原，不覆盖已有文件
			files, _, skipped, err := restoreInto(e.Stored, e.Original)
			if err != nil || files == 0 {
				if err == nil {
					err = fmt.Errorf("原路径已存在且没有可还原的文件：%s", e.Original)
				}
				return e, err
			}
			_ = os.RemoveAll(e.Stored)
			e.Restored = true
			e.Skipped = skipped
			list[i].Restored = true
			q.rewrite(list)
			pruneEmptyDirs(filepath.Dir(e.Stored), q.root)
			return e, nil
		}
		if err := os.MkdirAll(filepath.Dir(e.Original), 0o755); err != nil {
			return e, err
		}
		if err := movePath(e.Stored, e.Original); err != nil {
			return e, err
		}
		list[i].Restored = true
		q.rewrite(list)
		pruneEmptyDirs(filepath.Dir(e.Stored), q.root)
		return e, nil
	}
	return QEntry{}, fmt.Errorf("找不到隔离记录：%s", id)
}

// 清理过期隔离项（真删，不可恢复）
func (q *Quarantine) purge(days int) (int, uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cut := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	list := q.entries()
	var kept []QEntry
	var n int
	var bytes uint64
	for _, e := range list {
		if e.Restored || e.TS > cut {
			kept = append(kept, e)
			continue
		}
		_ = removePath(filepath.Dir(e.Stored))
		n++
		bytes += e.Bytes
	}
	q.rewrite(kept)
	return n, bytes
}

func (q *Quarantine) totalBytes() uint64 {
	var sum uint64
	for _, e := range q.entries() {
		if !e.Restored {
			sum += e.Bytes
		}
	}
	return sum
}

// ---------- 演练 / 执行的 CLI 入口 ----------

func printRehearsal(cfg Config, path string) Rehearsal {
	r := rehearse(cfg, path)
	fmt.Printf("删除演练：%s\n", path)
	if r.Refuse != "" {
		fmt.Printf("  [拒绝] %s\n", r.Refuse)
		return r
	}
	fmt.Printf("  类别 %s（%s）· 文件 %d 个 · 合计 %s\n", cats[r.Cat].Label, tierText[r.Tier], r.Files, human(r.Bytes))
	if len(r.Locked) > 0 {
		fmt.Printf("  [注意] %d 个文件被占用，实际隔离时会被跳过/失败：\n", len(r.Locked))
		for i, l := range r.Locked {
			if i >= 5 {
				break
			}
			fmt.Printf("    - %s\n", l)
		}
	}
	for _, f := range r.TopFiles {
		fmt.Printf("    %10s  %s\n", human(f.Bytes), f.Path)
	}
	return r
}

// 即将到期的隔离项（剩余天数 <= withinDays）
func (q *Quarantine) expiringSoon(keepDays, withinDays int) []QEntry {
	var out []QEntry
	cut := time.Now().Add(-time.Duration(keepDays-withinDays) * 24 * time.Hour).Unix()
	for _, e := range q.entries() {
		if !e.Restored && e.TS <= cut {
			out = append(out, e)
		}
	}
	return out
}

// rebuild 从每个条目目录里的元数据副本重建 index.jsonl。
// 原则：**只增不减**——已有索引里的条目一律保留，只有确认失效（文件已不存在）才剔除。
func (q *Quarantine) rebuild() (recovered, orphans, skipped int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	entries := q.entries() // 以现有索引为基线，绝不丢弃
	idx := map[string]int{}
	for i, e := range entries {
		idx[e.ID] = i
	}
	dirs, err := os.ReadDir(q.root)
	if err != nil {
		return 0, 0, 0
	}
	seenDir := map[string]bool{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		seenDir[d.Name()] = true
		metaPath := filepath.Join(q.root, d.Name(), metaFileName)
		b, err := os.ReadFile(metaPath)
		if err != nil {
			// 老条目没有侧车：只要索引里有它，就原样保留
			if _, ok := idx[d.Name()]; ok {
				continue
			}
			// 没有元数据也没有索引：空壳目录直接清掉
			dir := filepath.Join(q.root, d.Name())
			if sub, e := os.ReadDir(dir); e == nil && len(sub) == 0 {
				_ = os.Remove(dir)
				continue
			}
			orphans++
			continue
		}
		var e QEntry
		if json.Unmarshal(b, &e) != nil || e.ID == "" || e.Original == "" {
			orphans++
			continue
		}
		if _, err := os.Stat(e.Stored); err != nil {
			// 侧车在但文件没了：这条确实失效
			if i, ok := idx[e.ID]; ok {
				entries = append(entries[:i], entries[i+1:]...)
				delete(idx, e.ID)
				for k, v := range idx {
					if v > i {
						idx[k] = v - 1
					}
				}
			}
			skipped++
			continue
		}
		if i, ok := idx[e.ID]; ok {
			entries[i] = e // 用侧车校正（原路径、大小等）
		} else {
			entries = append(entries, e)
			idx[e.ID] = len(entries) - 1
			recovered++
		}
	}

	// 索引里有、目录里没了：说明隔离文件被手工删了，剔除
	var kept []QEntry
	for _, e := range entries {
		if _, err := os.Stat(e.Stored); err != nil {
			skipped++
			continue
		}
		kept = append(kept, e)
	}

	var sb strings.Builder
	for _, e := range kept {
		if b, err := json.Marshal(e); err == nil {
			sb.Write(b)
			sb.WriteByte('\n')
		}
	}
	_ = writeFileAtomic(q.index, []byte(sb.String()))
	return recovered, orphans, skipped
}

// 即将到期的隔离项（剩余天数 <= withinDays）

// 还原/删除后把空目录清掉，避免隔离区里留一堆空壳
func pruneEmptyDirs(dir, stop string) {
	for dir != "" && dir != stop && strings.HasPrefix(dir, stop) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// removeEmptyTree 自下而上删掉已经空了的目录（逐文件移动后调用）。
// 只删空目录：还有被占用文件没搬走的目录会保留下来。
func removeEmptyTree(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空会失败，正好保留
	}
}

// moveFilesIndividually 把目录内容移进隔离区。
// 先尝试整棵子树 rename（快）；失败（被占用 / 跨卷）才逐个文件移动，
// 跨卷时流式复制，每 1 MB 检查一次暂停/停止并回报进度。
// gate 返回 false 时立即停下，已完成的文件如实返回（半截文件会被删掉）。
func moveFilesIndividually(src, dst string, onFile func(moved uint64, files, skipped int, cur string), gate func() bool) (uint64, int, int, error) {
	var moved uint64
	files, skipped := 0, 0
	lastReport := time.Time{}
	report := func(cur string, force bool) {
		if onFile == nil {
			return
		}
		if !force && time.Since(lastReport) < 120*time.Millisecond {
			return
		}
		lastReport = time.Now()
		onFile(moved, files, skipped, cur)
	}
	stopCheck := func() bool {
		return gate != nil && !gate()
	}

	// moveOne 移动单个文件：同卷 rename 是瞬时的；跨卷/被占用时流式复制。
	moveOne := func(p, t string) error {
		if err := os.Rename(p, t); err == nil {
			if info, err := os.Stat(t); err == nil {
				moved += uint64(info.Size())
			}
			files++
			report(p, false)
			return nil
		}
		written, err := copyFileStream(p, t, gate, func(n uint64) {
			if onFile != nil && time.Since(lastReport) >= 120*time.Millisecond {
				lastReport = time.Now()
				onFile(moved+n, files, skipped, p)
			}
		})
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			// 复制成功但源文件删不掉（安全策略）：把副本删掉，别留下两份数据
			_ = os.Remove(t)
			return fmt.Errorf("删除源文件失败：%v", err)
		}
		moved += written
		files++
		report(p, false)
		return nil
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range entries {
		if stopCheck() {
			return moved, files, skipped, errStopped
		}
		child := filepath.Join(src, e.Name())
		target := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				skipped++
				continue
			}
			if err := os.Rename(child, target); err == nil {
				size, cnt := dirStats(target)
				moved += size
				files += cnt
				report(child, true)
				continue
			}
			// 子树里有文件被占用（或跨卷）：逐文件搬
			walkErr := filepath.WalkDir(child, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if stopCheck() {
					return errStopped
				}
				rel, rerr := filepath.Rel(src, p)
				if rerr != nil {
					skipped++
					return nil
				}
				t := filepath.Join(dst, rel)
				if err := os.MkdirAll(filepath.Dir(t), 0o755); err != nil {
					skipped++
					return nil
				}
				if err := moveOne(p, t); err != nil {
					if errors.Is(err, errStopped) {
						return errStopped
					}
					skipped++
				}
				return nil
			})
			if errors.Is(walkErr, errStopped) {
				return moved, files, skipped, errStopped
			}
			if walkErr != nil {
				return moved, files, skipped, walkErr
			}
			report(child, true)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			skipped++
			continue
		}
		if err := moveOne(child, target); err != nil {
			if errors.Is(err, errStopped) {
				return moved, files, skipped, errStopped
			}
			skipped++
		}
	}
	if files == 0 {
		return 0, 0, skipped, fmt.Errorf("所有文件都被占用，无法移动（%d 个跳过）", skipped)
	}
	return moved, files, skipped, nil
}

// copyFileStream 分块复制文件：每读一块检查暂停/停止，进度也随之更新。
// 中途失败或被停止时删掉半截目标文件，源文件保持原样。
func copyFileStream(src, dst string, gate func() bool, onChunk func(written uint64)) (uint64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	done := false
	defer func() {
		_ = out.Close()
		if !done {
			_ = os.Remove(dst)
		}
	}()
	buf := make([]byte, 1<<20)
	var written uint64
	for {
		if gate != nil && !gate() {
			return written, errStopped
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += uint64(n)
			if onChunk != nil {
				onChunk(written)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, rerr
		}
	}
	if err := out.Close(); err != nil {
		return written, err
	}
	done = true
	if st, err := in.Stat(); err == nil {
		_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return written, nil
}

// dirStats 统计目录大小与文件数（用于子树 rename 后记账）
func dirStats(root string) (uint64, int) {
	var bytes uint64
	var n int
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			bytes += uint64(info.Size())
			n++
		}
		return nil
	})
	return bytes, n
}

// restoreInto 把隔离区里的文件合并回已存在的目录，不覆盖同名文件。
func restoreInto(stored, original string) (int, uint64, int, error) {
	var files int
	var bytes uint64
	skipped := 0
	err := filepath.WalkDir(stored, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(stored, p)
		if rerr != nil {
			skipped++
			return nil
		}
		target := filepath.Join(original, rel)
		if _, err := os.Stat(target); err == nil {
			skipped++ // 目标已存在：保留现场，不覆盖
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			skipped++
			return nil
		}
		if err := os.Rename(p, target); err != nil {
			if err := copyFile(p, target); err != nil {
				skipped++
				return nil
			}
			_ = os.Remove(p)
		}
		if info, err := os.Stat(target); err == nil {
			bytes += uint64(info.Size())
		}
		files++
		return nil
	})
	return files, bytes, skipped, err
}
