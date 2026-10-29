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
	Children map[string]*Scan `json:"children,omitempty"`
	// Mft 是 MFT 引擎一次读完整卷的结果。它和 Children 互斥：读的是整卷，
	// 拆不出「一个顶层子树一个 Scan」，所以只能整份存、整份用。
	// 谁写谁就把另一份清掉——两套口径混在一张表里，会让"复用哪一份"变得说不清。
	Mft *Scan `json:"mft,omitempty"`
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
	// 只有 MFT 快照、没有子树，也是一份合法缓存。以前这里只认 Children，
	// 于是 MFT 写进来的那份永远读不回来——写了等于没写。
	if json.Unmarshal(b, &c) != nil || c.Root == "" || (len(c.Children) == 0 && c.Mft == nil) {
		return nil
	}
	return &c
}

func saveScanCache(cfg Config, c *scanCache) {
	if b, err := json.Marshal(c); err == nil {
		_ = writeFileAtomic(cachePath(cfg), b)
	}
}

// cacheFresh 是两种缓存共用的「还能不能用」判据：
// 同一个进程会话、同一个根、不是强制全扫、USN 在跟踪、时间都没过期。
//
// 会话令牌这一条不是保守，是必需：usnEventsSince 用的是进程内的 USN 序号差，
// 换一个进程就没有基准可比。跨进程复用 = "没法知道期间改了什么"，
// 而这份数字是要给人拿去删东西的，宁可贵一点重扫，也不能报一份陈旧账。
func cacheFresh(cache *scanCache, root string, forceFull bool, now time.Time) bool {
	return !forceFull && cache != nil &&
		strings.EqualFold(cache.Root, root) && cache.Session == procSession &&
		usnActive.Load() &&
		now.Sub(time.Unix(cache.TS, 0)) < 6*time.Hour &&
		now.Sub(time.Unix(cache.FullTS, 0)) < 24*time.Hour
}

// walkCacheUsable 判断遍历缓存能不能参与本次增量。
//
// 必须要求 Children 非空：MFT 写下的缓存只有整卷快照、没有子树，
// 若把它当成"可以复用"放进来，就会走过 MFT 分支（useCache=true 时它被跳过），
// 然后逐个顶层子树全扫一遍——比直接读 MFT 慢一个数量级。
func walkCacheUsable(cache *scanCache, root string, forceFull bool, now time.Time) bool {
	return cacheFresh(cache, root, forceFull, now) && len(cache.Children) > 0
}

// cachedMftScan 返回上次 MFT 整卷扫描的结果（可以直接当本次结果用），nil 表示这次得重读。
// 判据和遍历缓存同一套，另外多两条：
//   - 只有开着 MFT 引擎才认这份缓存（`--walk` 必须真的走遍历）；
//   - 整卷快照拆不出「哪一棵子树变了」，所以只要有一处脏标记，整份作废。
//
// 反过来，脏集合为空时复用它是安全的——那正是遍历缓存逐子树复用的同一个前提。
func cachedMftScan(cache *scanCache, root string, forceFull bool, dirty map[string]bool, allDirty bool, now time.Time) *Scan {
	if !useMftEngine || cache == nil || cache.Mft == nil {
		return nil
	}
	if !cacheFresh(cache, root, forceFull, now) {
		return nil
	}
	if allDirty || len(dirty) > 0 {
		return nil
	}
	return cache.Mft
}

// saveMftScanCache 把一次成功的 MFT 结果写进增量缓存，行为对齐遍历路径。
//
// 以前 MFT 成功时直接 return，什么都不留——于是管理员每次 scan 都得把整个 $MFT
// 重读一遍（约 110 万条记录）。写下来之后，同一个进程里下一次扫描只要 USN 说
// "没有任何写入"，就能直接拿这份结果（见 cachedMftScan）。
//
// 只有真成功才写：ok=false、回落遍历、被取消的路径都不碰它——半成品缓存比没有缓存更坏，
// 因为它会被当成完整结果用在下一轮。
func saveMftScanCache(cfg Config, sc *Scan) {
	if sc == nil {
		return
	}
	now := time.Now().Unix()
	// Root 用 cfg.Root 而不是 sc.Root：读的一侧拿 cfg.Root 比对，两边写法必须一致
	// （MFT 引擎内部会把根补成带反斜杠的形式）。
	saveScanCache(cfg, &scanCache{
		Root: cfg.Root, TS: now, FullTS: now,
		Session: procSession, Mft: sc,
	})
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
	now := time.Now()
	useCache := walkCacheUsable(cache, cfg.Root, forceFull, now)
	// 上次 MFT 整卷扫描的结果还在不在？静态条件（会话、根、时效）够不够，
	// 得等脏集合出来才算数——所以这里只用来决定"要不要等一次 USN 轮询"。
	hasMftCache := useMftEngine && cache != nil && cache.Mft != nil &&
		cacheFresh(cache, cfg.Root, forceFull, now)
	if useCache || hasMftCache {
		// 扫描前必须等一次"扫描开始之后"的 USN 轮询：
		// 否则刚改动的文件还没进脏集合，会被误判为没变。
		waitUSNPoll(3*time.Second, 8*time.Second)
	}
	scanStart := time.Now()
	dirty, all := dirtySnapshot()
	if all {
		useCache = false
	}

	// 整卷一次都没动过，就直接交上次的 MFT 结果——"第二次起变快"真正生效的地方，
	// 这一次连 $MFT 都不读。脏集合里有任何一条子树，整份快照作废（拆不出子树）。
	if sc := cachedMftScan(cache, cfg.Root, forceFull, dirty, all, now); sc != nil {
		cp := *sc
		cp.TS = scanStart.Unix()
		cp.Duration = time.Since(scanStart).Seconds()
		return &cp, "cached"
	}

	// MFT 引擎在这里才尝试，位置是有讲究的：
	// 它要替代的是「昂贵的那一次全量走盘」，而不是增量更新。
	// 早先它写在函数最开头，结果常驻路径（watch/tray/serve）每次刷新都绕过 USN 缓存、
	// 重读一遍 MFT 表——每 5 秒读 3 秒，比增量更新还慢。那把加速用成了减速。
	if !useCache && useMftEngine {
		if sc, ok := scanVolumeViaMft(cfg, cfg.Root); ok {
			// 成功才落缓存，和遍历路径的行为对齐：下一次同进程扫描只要 USN 说没写入，
			// 就能直接复用这份结果，不必再读 110 万条记录。
			// 被取消的那一次数据是残的——残的写进去比没有更坏，它会被当成完整结果。
			if !scanCancelled() {
				saveMftScanCache(cfg, sc)
				clearDirtyBefore(scanStart)
			}
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
