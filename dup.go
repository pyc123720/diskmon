package main

import (
	"crypto/sha256"
	"syscall"
	"unsafe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- 重复文件 ----------

type DupGroup struct {
	Size   uint64   `json:"size"`
	Hash   string   `json:"hash"`
	Paths  []string `json:"paths"`
	Wasted uint64   `json:"wasted"`
}

type DupResult struct {
	TS          int64      `json:"ts"`
	Root        string     `json:"root"`
	MinSize     uint64     `json:"min_size"`
	Files       int        `json:"files"`
	Hashed      int        `json:"hashed"`
	Groups      []DupGroup `json:"groups"`
	TotalWasted uint64     `json:"total_wasted"`
	Duration    float64    `json:"duration"`
	Errors      int        `json:"errors"`
	Truncated   bool       `json:"truncated"`
}

func dupsPath(cfg Config) string { return filepath.Join(cfg.DataDir, "dups.json") }

func loadDups(cfg Config) *DupResult {
	b, err := os.ReadFile(dupsPath(cfg))
	if err != nil {
		return nil
	}
	var r DupResult
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func saveDups(cfg Config, r *DupResult) {
	if b, err := json.Marshal(r); err == nil {
		_ = writeFileAtomic(dupsPath(cfg), b)
	}
}

// 安装目录里的"重复文件"往往是程序必需的（比如 Quark 的 app.asar + app_asar.dll），
// 隔离会直接搞坏软件，所以默认跳过
func isInstallPath(p string) bool {
	low := strings.ToLower(p) + `\`
	for _, d := range []string{`\program files\`, `\program files (x86)\`, `\windows\`, `\appdata\local\programs\`} {
		if strings.Contains(low, d) {
			return true
		}
	}
	return false
}

// 硬链接的"多份"是同一份数据，不能当重复文件
func fileLinkCount(path string) int {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	h, _, _ := pCreateFileW.Call(uintptr(unsafe.Pointer(p)), 0x80, 0x7, 0, 3, 0, 0)
	if h == invalidHandle || h == 0 {
		return 0
	}
	defer pCloseHandle.Call(h)
	var info fileStandardInfo
	ok, _, _ := pGetFileInformationByHandleEx.Call(h, 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		return 0
	}
	return int(info.NumberOfLinks)
}

func quickHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, buf)
	h := sha256.Sum256(buf[:n])
	return hex.EncodeToString(h[:8])
}

func fullHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// 两阶段：按大小分组 → 快速哈希 → 完整哈希；带时间预算
func scanDups(cfg Config, root string, minMB int, budget time.Duration) *DupResult {
	t0 := time.Now()
	minSize := uint64(max(minMB, 1)) << 20
	res := &DupResult{TS: t0.Unix(), Root: root, MinSize: minSize}

	bySize := map[uint64][]string{}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return res
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			res.Errors++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if time.Since(t0) > budget {
			res.Truncated = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.EqualFold(p, cfg.DataDir) {
				return filepath.SkipDir
			}
			if info, e := d.Info(); e == nil && isReparse(info) {
				return filepath.SkipDir
			}
			return nil
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() {
			return nil
		}
		size := uint64(info.Size())
		if size < minSize {
			return nil
		}
		low := strings.ToLower(p)
		if strings.Contains(low, `\winsxs\`) || strings.Contains(low, `\windowsapps\`) || strings.Contains(low, `\windows\system32\`) {
			return nil // 系统目录：多为硬链接，不参与去重
		}
		if fileLinkCount(p) > 1 {
			return nil // 硬链接指向同一份数据
		}
		res.Files++
		bySize[size] = append(bySize[size], p)
		return nil
	})

	type key struct {
		size uint64
		hash string
	}
	final := map[key][]string{}
	for size, paths := range bySize {
		if len(paths) < 2 {
			continue
		}
		if len(paths) > 64 { // 同尺寸过多，跳过（可能是日志轮转/缓存碎片）
			continue
		}
		byQuick := map[string][]string{}
		for _, p := range paths {
			if time.Since(t0) > budget {
				res.Truncated = true
				break
			}
			q := quickHash(p)
			if q == "" {
				res.Errors++
				continue
			}
			res.Hashed++
			byQuick[q] = append(byQuick[q], p)
		}
		for _, cand := range byQuick {
			if len(cand) < 2 {
				continue
			}
			byFull := map[string][]string{}
			for _, p := range cand {
				if time.Since(t0) > budget {
					res.Truncated = true
					break
				}
				h := fullHash(p)
				if h == "" {
					res.Errors++
					continue
				}
				res.Hashed++
				byFull[h] = append(byFull[h], p)
			}
			for h, ps := range byFull {
				if len(ps) < 2 {
					continue
				}
				final[key{size, h}] = ps
			}
		}
	}

	for k, ps := range final {
		sort.Strings(ps)
		g := DupGroup{Size: k.size, Hash: k.hash, Paths: ps, Wasted: k.size * uint64(len(ps)-1)}
		res.Groups = append(res.Groups, g)
		res.TotalWasted += g.Wasted
	}
	sort.Slice(res.Groups, func(i, j int) bool { return res.Groups[i].Wasted > res.Groups[j].Wasted })
	if len(res.Groups) > 200 {
		res.Groups = res.Groups[:200]
	}
	res.Duration = time.Since(t0).Seconds()
	return res
}

func runDupScan(cfg Config, st *Store, root string, minMB int) *DupResult {
	fmt.Printf("重复文件扫描 %s（≥%d MB，最多 300 秒）…\n", root, minMB)
	r := scanDups(cfg, root, minMB, 300*time.Second)
	saveDups(cfg, r)
	st.AddAlert("dup-scan", fmt.Sprintf("重复文件扫描完成：%d 组，可释放 %s，耗时 %.0fs",
		len(r.Groups), human(r.TotalWasted), r.Duration))
	fmt.Printf("完成：%d 组重复，可释放 %s，哈希 %d 个文件，耗时 %.0fs%s\n",
		len(r.Groups), human(r.TotalWasted), r.Hashed, r.Duration,
		map[bool]string{true: "（超出时间预算，结果不完整）", false: ""}[r.Truncated])
	for i, g := range r.Groups {
		if i >= 10 {
			break
		}
		fmt.Printf("  %8s × %d 份，浪费 %s\n", human(g.Size), len(g.Paths), human(g.Wasted))
		for j, p := range g.Paths {
			if j >= 2 {
				fmt.Printf(enFormat("      … 另外 %d 份\n", "      … plus %d more copies\n"), len(g.Paths)-2)
				break
			}
			fmt.Printf("      %s\n", p)
		}
	}
	return r
}

// 隔离一组的「除第一份外的所有副本」
func dupApplyGroup(cfg Config, st *Store, q *Quarantine, hash string, keepFirst bool) (int, uint64, []string) {
	r := loadDups(cfg)
	if r == nil {
		return 0, 0, []string{"没有重复文件扫描结果"}
	}
	var target *DupGroup
	for i := range r.Groups {
		if r.Groups[i].Hash == hash {
			target = &r.Groups[i]
			break
		}
	}
	if target == nil {
		return 0, 0, []string{"找不到该组"}
	}
	var n int
	var freed uint64
	var fails []string
	paths := target.Paths
	start := 0
	if keepFirst {
		start = 1
	}
	skipped := 0
	for _, p := range paths[start:] {
		if isInstallPath(p) {
			skipped++
			fails = append(fails, "跳过安装目录："+p)
			continue
		}
		e, err := q.apply(cfg, p, "dup")
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		n++
		freed += e.Bytes
	}
	if n > 0 {
		st.AddAlert("dup-quarantine", fmt.Sprintf("重复文件：隔离 %d 份副本，释放 %s", n, human(freed)))
	}
	return n, freed, fails
}
