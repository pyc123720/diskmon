package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 增量扫描（干净子树复用） ----------
//
// 只复用"上次扫描后没有任何 USN 事件"的顶层子树；其余照旧全扫。
// 任何可疑情况（新进程、重命名、缓存过期、强制全扫）一律退回全扫。

type scanCache struct {
	Root     string           `json:"root"`
	TS       int64            `json:"ts"`      // 上次扫描时间
	FullTS   int64            `json:"full_ts"` // 上次"全量扫描"时间（对账基准）
	Session  string           `json:"session"` // 进程会话令牌：跨进程缓存一律不认
	Children map[string]*Scan `json:"children"`
}

var procSession = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

var (
	usnLastPoll atomic.Int64 // 最近一次 USN 轮询完成时间（UnixMilli）

	dirtyMu       sync.Mutex
	dirtyChildren = map[string]time.Time{}
	dirtyAll      bool
	scanForceFull atomic.Bool
)

func cachePath(cfg Config) string { return filepath.Join(cfg.DataDir, "scan_cache.json") }

func loadScanCache(cfg Config) *scanCache {
	b, err := os.ReadFile(cachePath(cfg))
	if err != nil {
		return nil
	}
	var c scanCache
	if json.Unmarshal(b, &c) != nil || c.Root == "" || len(c.Children) == 0 {
		return nil
	}
	return &c
}

func saveScanCache(cfg Config, c *scanCache) {
	if b, err := json.Marshal(c); err == nil {
		_ = writeFileAtomic(cachePath(cfg), b)
	}
}

// topChild 返回路径所属的顶层子树（结尾带分隔符），如 C:\Users\
func topChild(root, p string) string {
	r := strings.TrimRight(root, `\`) + `\`
	if !strings.HasPrefix(strings.ToLower(p), strings.ToLower(r)) {
		return ""
	}
	rest := p[len(r):]
	if i := strings.IndexAny(rest, `\`); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return ""
	}
	return r + rest + `\`
}

// markDirty 记录某个顶层子树发生了变化；重命名保守处理为"全部失效"
func markDirty(root, path string, rename bool) {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	if rename {
		dirtyAll = true
		return
	}
	if c := topChild(root, path); c != "" {
		dirtyChildren[strings.ToLower(c)] = time.Now()
	}
}

func dirtySnapshot() (map[string]bool, bool) {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	out := map[string]bool{}
	for k := range dirtyChildren {
		out[k] = true
	}
	return out, dirtyAll
}

func clearDirty() {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	dirtyChildren = map[string]time.Time{}
	dirtyAll = false
}

func canReuseChild(cache *scanCache, child string, dirty map[string]bool, now time.Time) bool {
	if cache == nil || len(cache.Children) == 0 {
		return false
	}
	if now.Sub(time.Unix(cache.TS, 0)) > 24*time.Hour {
		return false
	}
	key := strings.ToLower(child)
	if dirty[key] {
		return false
	}
	s := cache.Children[key]
	return s != nil
}

func listRootChildren(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "$") {
			continue
		}
		if info, ierr := e.Info(); ierr == nil && isReparse(info) {
			continue
		}
		out = append(out, filepath.Join(root, e.Name())+string(os.PathSeparator))
	}
	sort.Strings(out)
	return out, nil
}

// ScanRootCached 逐子树扫描（能复用就复用），返回结果和来源
func ScanRootCached(cfg Config, forceFull bool) (*Scan, string) {
	// 整次扫描共用一张硬链接身份表——拆成分片并行之后，跨分片的重复
	// （例如 WinSxS 与 System32）否则会漏掉。本机实测漏掉 2.37 GB。
	restoreHardlinks := beginHardlinkScope()
	defer restoreHardlinks()

	children, err := listRootChildren(cfg.Root)
	if err != nil || len(children) < 2 {
		return scanVolume(cfg, cfg.Root), "full"
	}
	cache := loadScanCache(cfg)
	useCache := !forceFull && cache != nil && strings.EqualFold(cache.Root, cfg.Root) &&
		cache.Session == procSession && usnActive.Load() &&
		time.Since(time.Unix(cache.TS, 0)) < 6*time.Hour &&
		time.Since(time.Unix(cache.FullTS, 0)) < 24*time.Hour
	if useCache {
		// 扫描前必须等一次"扫描开始之后"的 USN 轮询：
		// 否则刚改动的文件还没进脏集合，会被误判为没变。
		waitUSNPoll(3*time.Second, 8*time.Second)
	}
	scanStart := time.Now()
	dirty, all := dirtySnapshot()
	if all {
		useCache = false
	}

	// MFT 引擎在这里才尝试，位置是有讲究的：
	// 它要替代的是「昂贵的那一次全量走盘」，而不是增量更新。
	// 早先它写在函数最开头，结果常驻路径（watch/tray/serve）每次刷新都绕过 USN 缓存、
	// 重读一遍 MFT 表——每 5 秒读 3 秒，比增量更新还慢。那把加速用成了减速。
	if !useCache && useMftEngine {
		if sc, ok := scanVolumeViaMft(cfg, cfg.Root); ok {
			return sc, "mft"
		}
		fmt.Println("  MFT 引擎不可用（需管理员权限 + NTFS），回落到目录遍历")
	}

	parts := make([]*Scan, 0, len(children))
	newChildren := map[string]*Scan{}
	var jobs []string
	reused := 0
	for _, ch := range children {
		key := strings.ToLower(ch)
		if useCache && canReuseChild(cache, ch, dirty, time.Now()) {
			parts = append(parts, cache.Children[key])
			newChildren[key] = cache.Children[key]
			reused++
			continue
		}
		jobs = append(jobs, ch)
	}

	// 把待扫的顶层子树拆成更细的任务，让 worker 抢活，而不是各领一大块。
	// 硬约束：结果必须按顶层子树归并回「一个子树一个 Scan」——缓存键就是子树路径，
	// 拆完不归并回去，增量复用会整体失效。
	var tasks []scanJob
	for _, ch := range jobs {
		tasks = append(tasks, buildJobs(ch)...)
	}

	byParent := make(map[string][]*Scan, len(jobs))
	scanned := 0
	if len(tasks) > 0 {
		var mu sync.Mutex
		queue := make(chan scanJob)
		var wg sync.WaitGroup
		for i := 0; i < scanWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range queue {
					if scanCancelled() {
						continue
					}
					s := scanVolume(cfg, job.path)
					s.Root = job.path
					mu.Lock()
					key := strings.ToLower(job.parent)
					byParent[key] = append(byParent[key], s)
					mu.Unlock()
				}
			}()
		}
		for _, t := range tasks {
			queue <- t
		}
		close(queue)
		wg.Wait()

		for _, ch := range jobs {
			key := strings.ToLower(ch)
			ps := byParent[key]
			if len(ps) == 0 {
				continue // 被取消或整块失败就不写进缓存
			}
			unified := mergeChildParts(ch, ps)
			parts = append(parts, unified)
			newChildren[key] = unified
			scanned++
		}
	}

	parts = compactScans(parts)
	// 根目录下的散文件（pagefile.sys / hiberfil.sys / bootmgr…）也必须进账。
	// 以前整条路径都漏了它们，本机那两个就占 18 GB，导致 Total 比卷已用少一大截。
	if rf := scanRootFiles(cfg, listRootFiles(cfg.Root)); rf != nil {
		parts = append(parts, rf)
	}
	merged := mergeScans(cfg.Root, parts)
	merged.Dirs++
	merged.ReusedChildren = reused
	merged.ScannedChildren = scanned
	if reused > 0 && scanned >= 0 {
		merged.Source = "incremental"
	} else {
		merged.Source = "full"
	}
	if !scanCancelled() {
		fullTS := time.Now().Unix()
		if reused > 0 && cache != nil && cache.FullTS > 0 {
			fullTS = cache.FullTS // 增量不刷新对账基准
		}
		if forceFull || reused == 0 {
			fullTS = time.Now().Unix()
		}
		saveScanCache(cfg, &scanCache{
			Root: cfg.Root, TS: time.Now().Unix(), FullTS: fullTS,
			Session: procSession, Children: newChildren,
		})
		clearDirtyBefore(scanStart)
	}
	return merged, merged.Source
}

// waitUSNPoll 等待一次晚于当前时刻的 USN 轮询完成（最多 max 秒）。
func waitUSNPoll(interval, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		last := time.UnixMilli(usnLastPoll.Load())
		if !last.IsZero() && time.Since(last) < interval {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// clearDirtyBefore 只清除扫描开始前的脏标记，扫描期间的新事件保留
func clearDirtyBefore(ts time.Time) {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	cut := ts.Unix()
	for k, v := range dirtyChildren {
		if v.Unix() <= cut {
			delete(dirtyChildren, k)
		}
	}
	dirtyAll = false
}
