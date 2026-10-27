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
	children, err := listRootChildren(cfg.Root)
	if err != nil || len(children) < 2 {
		return scanVolume(cfg, cfg.Root), "full"
	}
	cache := loadScanCache(cfg)
	dirty, all := dirtySnapshot()
	useCache := !forceFull && cache != nil && strings.EqualFold(cache.Root, cfg.Root) &&
		cache.Session == procSession && usnActive.Load() && !all &&
		time.Since(time.Unix(cache.TS, 0)) < 6*time.Hour &&
		time.Since(time.Unix(cache.FullTS, 0)) < 24*time.Hour

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

	var mu sync.Mutex
	sem := make(chan struct{}, scanWorkers)
	var wg sync.WaitGroup
	scanned := 0
	for _, ch := range jobs {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if scanCancelled() {
				return
			}
			s := scanVolume(cfg, p)
			mu.Lock()
			parts = append(parts, s)
			newChildren[strings.ToLower(p)] = s
			scanned++
			mu.Unlock()
		}(ch)
	}
	wg.Wait()

	merged := mergeScans(cfg.Root, compactScans(parts))
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
		clearDirty()
	}
	return merged, merged.Source
}
