package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// ---------- 隔离区 ----------

type QEntry struct {
	ID       string `json:"id"`
	Original string `json:"original"`
	Stored   string `json:"stored"`
	Bytes    uint64 `json:"bytes"`
	Files    int    `json:"files"`
	TS       int64  `json:"ts"`
	Reason   string `json:"reason"`
	Restored bool   `json:"restored"`
}

const metaFileName = ".diskmon-meta.json"

type Rehearsal struct {
	Path     string
	Files    int
	Bytes    uint64
	Locked   []string
	Skipped  int
	TopFiles []FileEntry
	Refuse   string
	Cat      string
	Tier     string
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

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// 执行隔离：移动而不是删除；同卷 rename，跨卷 copy+delete
func (q *Quarantine) apply(cfg Config, path, reason string) (QEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	r := rehearse(cfg, path)
	if r.Refuse != "" {
		return QEntry{}, fmt.Errorf("%s", r.Refuse)
	}
	if cfg.QuarantineMaxGB > 0 && q.totalBytes()+r.Bytes > uint64(cfg.QuarantineMaxGB)<<30 {
		return QEntry{}, fmt.Errorf("隔离区已达上限 %d GB（当前 %s），请先还原或清理超期项",
			cfg.QuarantineMaxGB, human(q.totalBytes()))
	}
	id := newID()
	dest := filepath.Join(q.root, id, filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return QEntry{}, err
	}
	if err := movePath(path, dest); err != nil {
		return QEntry{}, err
	}
	e := QEntry{ID: id, Original: path, Stored: dest, Bytes: r.Bytes, Files: r.Files,
		TS: time.Now().Unix(), Reason: reason}
	// 元数据随条目落盘：即使 index.jsonl 损坏，也能靠它重建
	if b, err := json.Marshal(e); err == nil {
		_ = writeFileAtomic(filepath.Join(q.root, id, metaFileName), b)
	}
	q.append(e)
	return e, nil
}

func movePath(src, dst string) error {
	errRename := os.Rename(src, dst)
	if errRename == nil {
		return nil
	}
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
			return e, fmt.Errorf("原路径已存在，拒绝覆盖：%s", e.Original)
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
