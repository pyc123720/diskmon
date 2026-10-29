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
	// Usn 是写下这份快照时该卷 USN 日志的坐标，跨进程判「有没有变化」就靠它。
	// 零值表示这次扫描期间有过写入、锚点不可信——那份快照只能在同进程内复用。
	Usn usnAnchor `json:"usn"`
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

// cacheFreshAny 判断缓存「本身」还能不能用：同一个根、不是强制全扫、时间都没过期。
// 和「谁在问」无关的判据都在这儿；是否跨进程复用由调用方另外拿证据决定。
func cacheFreshAny(cache *scanCache, root string, forceFull bool, now time.Time) bool {
	return !forceFull && cache != nil &&
		strings.EqualFold(cache.Root, root) &&
		now.Sub(time.Unix(cache.TS, 0)) < 6*time.Hour &&
		now.Sub(time.Unix(cache.FullTS, 0)) < 24*time.Hour
}

// cacheFresh 在 cacheFreshAny 之上再加一条：同一个进程会话，且本进程的 USN 跟踪是活的。
//
// 遍历缓存按子树存，"这棵子树有没有变"靠的是进程内的脏集合。换一个进程，
// 脏集合生下来就是空的——那不是"没变"，那是"不知道"。所以遍历缓存只认本进程。
func cacheFresh(cache *scanCache, root string, forceFull bool, now time.Time) bool {
	return cacheFreshAny(cache, root, forceFull, now) &&
		cache.Session == procSession && usnActive.Load()
}

// walkCacheUsable 判断遍历缓存能不能参与本次增量。
//
// 必须要求 Children 非空：MFT 写下的缓存只有整卷快照、没有子树，
// 若把它当成"可以复用"放进来，就会走过 MFT 分支（useCache=true 时它被跳过），
// 然后逐个顶层子树全扫一遍——比直接读 MFT 慢一个数量级。
func walkCacheUsable(cache *scanCache, root string, forceFull bool, now time.Time) bool {
	return cacheFresh(cache, root, forceFull, now) && len(cache.Children) > 0
}

// mftCacheUsableSameProc：同进程复用。证据是本进程的脏集合——USN 在跟踪，
// 且一处脏都没有。没在跟踪 USN 时脏集合永远是空的，但这个空不能当"没变"用。
func mftCacheUsableSameProc(cache *scanCache, dirty map[string]bool, allDirty bool) *Scan {
	if !usnActive.Load() {
		return nil
	}
	if allDirty || len(dirty) > 0 {
		return nil
	}
	return cache.Mft
}

// mftCacheUsableCrossProc：跨进程复用。证据是卷上 USN 日志的锚点（见 usnCacheStillClean）。
// 本进程没有任何 USN 基准，脏集合是空的也不代表没变，所以只有锚点能说"零变更"时才放行。
func mftCacheUsableCrossProc(cache *scanCache, anchorClean bool) *Scan {
	if !anchorClean {
		// 对不上就是"期间有写入"或者"根本读不到"，两种都只能重扫。
		return nil
	}
	return cache.Mft
}

// usnCacheStillClean 拿卷上的 USN 日志，验证「缓存写下之后到现在没有改动」。
// 这是跨进程复用的唯一证据来源；任何一步读不到都算不确定，返回 false + 原因。
//
// t7 之后这里有两层：坐标层面的结构性判据（usnAnchorVerdict，t5 的口径一条没松），
// 以及「多出来的记录里有没有改动」（usnAnchorClean）。第二层只在第一层放行后才跑，
// 因为第一层不过的时候，读那些记录也是白读。
func usnCacheStillClean(cache *scanCache, root string) (bool, string) {
	letter, ok := volumeLetter(root)
	if !ok {
		return false, "根不是卷根，取不到 USN 日志"
	}
	cur, err := usnAnchorReader(letter)
	if err != nil {
		// 没有管理员权限 / 日志没开 / 调用失败：都等于"不知道"，不许猜。
		return false, err.Error()
	}
	var d deltaResult
	if okV, _ := usnAnchorVerdict(cache.Usn, cur); okV && cur.Next > cache.Usn.Next {
		n, mod, derr := usnDeltaReader(letter, cache.Usn.Next, cur.Next, cache.Usn.JournalID)
		d = deltaResult{examined: true, records: n, modifying: mod, err: derr}
		// t14：读完再查一次坐标。这一次**必须重新 syscall**——复用上面那次 cur 就什么都看不出来，
		// 而「读的过程中日志被 purge」正是这次要堵的盲区。只有真的读了记录才走这里；
		// cur.Next == cache.Usn.Next 的快路径压根不进这个分支，不会多付一次调用。
		post, perr := usnAnchorReader(letter)
		if okRe, why := recheckReadWindow(cache.Usn, post, perr); !okRe {
			return false, why
		}
	}
	return usnAnchorClean(cache.Usn, cur, d)
}

// trustworthyAnchor 判断「这次扫描窗口里没有任何改动」，成立才返回可落盘的锚点。
//
// 不成立就返回零值：那份快照照样能用（它是一次真实的 MFT 读数），
// 但跨进程没有任何人能证明它到现在还有效，所以下次只能重扫。
// 读不到坐标、读不到记录、窗口里冒出一条改动类记录——全部算「不成立」，不许猜。
//
// t7 的放宽只体现在这里：以前要求窗口内**零条记录**，现在要求**零条改动类记录**。
// 纯粹的 CLOSE 不改变任何数字，所以不再一票否决。
func trustworthyAnchor(letter string, before usnAnchor, beforeErr error) (usnAnchor, string) {
	if beforeErr != nil {
		return usnAnchor{}, "扫描前取不到日志坐标：" + beforeErr.Error()
	}
	after, err := usnAnchorReader(letter)
	if err != nil {
		return usnAnchor{}, "扫描结束后取不到日志坐标：" + err.Error()
	}
	var d deltaResult
	readRecords := false
	if okV, _ := usnAnchorVerdict(before, after); okV && after.Next > before.Next {
		n, mod, derr := usnDeltaReader(letter, before.Next, after.Next, before.JournalID)
		d = deltaResult{examined: true, records: n, modifying: mod, err: derr}
		readRecords = true
	}
	if readRecords {
		// t14：读了记录就要复查一次坐标（原因见 recheckReadWindow）。
		// 没读记录时（after.Next == before.Next）不进这里，零改动路径不多付 syscall。
		post, perr := usnAnchorReader(letter)
		if okRe, whyRe := recheckReadWindow(before, post, perr); !okRe {
			return usnAnchor{}, "读完记录后复查不通过：" + whyRe
		}
	}
	if clean, whyC := usnAnchorClean(before, after, d); !clean {
		// 扫描期间有改动：这份快照可能是「中间某个瞬间」的样子。
		// 拿它当零变更的基准，那几秒里的改动会被永久漏掉。
		return usnAnchor{}, "扫描窗口内不干净：" + whyC
	}
	return after, ""
}

// cachedMftScan 返回上次 MFT 整卷扫描的结果（可以直接当本次结果用），nil 表示这次得重读。
//
// 两条路，证据来源不同：
//   - 同一个进程：本进程的脏集合就是证据（mftCacheUsableSameProc）；
//   - 跨进程：脏集合是空的但不代表没变，只能拿 USN 锚点去比（mftCacheUsableCrossProc）。
//
// 两条路共同的前置：MFT 引擎开着（--walk 必须真的走遍历）、根一致、时间没过期。
// 另外整卷快照拆不出「哪一棵子树变了」，所以任何一处脏标记都让整份作废。
func cachedMftScan(cache *scanCache, root string, forceFull bool, dirty map[string]bool, allDirty bool, now time.Time) *Scan {
	if !useMftEngine || cache == nil || cache.Mft == nil || !cacheFreshAny(cache, root, forceFull, now) {
		return nil
	}
	if allDirty || len(dirty) > 0 {
		return nil
	}
	if cache.Session == procSession {
		return mftCacheUsableSameProc(cache, dirty, allDirty)
	}
	anchorClean, _ := usnCacheStillClean(cache, root)
	return mftCacheUsableCrossProc(cache, anchorClean)
}

// saveMftScanCache 把一次成功的 MFT 结果写进增量缓存，行为对齐遍历路径。
//
// 以前 MFT 成功时直接 return，什么都不留——于是管理员每次 scan 都得把整个 $MFT
// 重读一遍（约 110 万条记录）。写下来之后，下一次扫描只要"没变过"，就能直接交这份结果。
//
// anchor 是这次扫描**可信**的 USN 锚点：只有「扫描窗口内没有任何写入」时才非零
// （见 ScanRootCached）。零值表示这份快照只在本进程内可复用——跨进程没有证据，
// 下次必然重扫。这是有意的：宁可重扫，也不拿一个解释不了的锚点去赌。
//
// 只有真成功才写：ok=false、回落遍历、被取消的路径都不碰它——半成品缓存比没有缓存更坏，
// 因为它会被当成完整结果用在下一轮。
func saveMftScanCache(cfg Config, sc *Scan, anchor usnAnchor) {
	if sc == nil {
		return
	}
	now := time.Now().Unix()
	// Root 用 cfg.Root 而不是 sc.Root：读的一侧拿 cfg.Root 比对，两边写法必须一致
	// （MFT 引擎内部会把根补成带反斜杠的形式）。
	//
	// 存的是 cloneScan 的结果，不是 sc 本身：sc 还要返回给调用方，
	// 它上面任何原地修改（排序、累加、补字段）都不该漏进缓存。
	saveScanCache(cfg, &scanCache{
		Root: cfg.Root, TS: now, FullTS: now,
		Session: procSession, Mft: cloneScan(sc), Usn: anchor,
	})
}

// cloneScan 深拷贝一份 Scan：map 和 slice 都换新的底层数组。
//
// 为什么非拷不可：缓存里那份会被反复读出来当「这次的结果」交出去。
// 浅拷贝（cp := *sc）让 Cats / Deep / TopFiles 这些引用类型和缓存共享底层数组——
// 谁在返回值上排个序、累加一下，缓存就被就地改了，下一次复用的就是被污染的数字。
func cloneScan(sc *Scan) *Scan {
	if sc == nil {
		return nil
	}
	cp := *sc
	cp.Cats = cloneCounts(sc.Cats)
	cp.CatsShared = cloneCounts(sc.CatsShared)
	cp.Owners = cloneCounts(sc.Owners)
	cp.Protected = cloneCounts(sc.Protected)
	cp.DayWrites = cloneCounts(sc.DayWrites)
	cp.Dirs3 = append([]DirEntry(nil), sc.Dirs3...)
	cp.Deep = append([]DirEntry(nil), sc.Deep...)
	cp.TopFiles = append([]FileEntry(nil), sc.TopFiles...)
	return &cp
}

func cloneCounts(m map[string]uint64) map[string]uint64 {
	if m == nil {
		return nil
	}
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---------- B2 Phase 1：把 USN 增量应用到快照上 ----------

// deltaApplyLimit 是一次增量能处理的记录上限。沿用读日志那个数：
// 再多的话，「读日志 + 逐条判定」的代价已经超过重扫一遍。
const deltaApplyLimit = 20000

// applyUsnDeltas 把一批记录应用到基线快照上，返回「现在」的一份。
//
// 第二个返回值非空 = **必须回落全扫**，并且说明为什么。调用方拿到非空值时
// 不许用返回的快照——宁可慢，也不许给一个偏小的数字：少算的字节意味着
// 用户以为自己还有空间。
//
// 三条纪律：
//  1. 任一记录判不准 → 整批回落，不做「先应用能应用的那部分」。半份增量比没有更坏。
//  2. 应用完必须过不变量校验（Unique+Shared==Total、sum(Cats)==Total）；不满足也回落——
//     那说明基线本身已经坏了，不是增量能修的。
//  3. 不改基线：进来的是 base，出去的是 clone。
//
// 注意：**这里不做名字→路径的还原，也不做 os.Stat**。记录里的 Path/OldSize/NewSize
// 都必须由调用方填好。原因见 usnDeltaRec 的注释：快照目前没有逐文件表，生产路径上
// OldSize 无处可取，所以写/删这两类在真实扫描里仍会回落，只有「新增」能落地。
// 补齐那块索引是 Phase 2 的事，不在本单夹带。
func applyUsnDeltas(base *Scan, recs []usnDeltaRec, root, dataDir string) (*Scan, string) {
	if base == nil {
		return nil, "没有基线快照，无法应用增量"
	}
	if len(recs) > deltaApplyLimit {
		return nil, fmt.Sprintf("记录太多（%d > %d），读日志+判定的代价超过重扫", len(recs), deltaApplyLimit)
	}
	// 先查基线自己：一份自相矛盾的快照上做增量，只会把错放大。
	// 注意 Unique 我们后面会按 Total-Shared 重算，所以基线的 Unique 不一致也照样拒绝——
	// 那说明这份缓存不是本版本写的，宁可重扫。
	if why := checkScanInvariants(base); why != "" {
		return nil, "基线快照本身不满足不变量，不敢在它上面加增量：" + why
	}
	cur := cloneScan(base)
	if cur.Cats == nil {
		cur.Cats = map[string]uint64{}
	}
	dataLow := strings.ToLower(strings.TrimSuffix(dataDir, `\`))

	for _, r := range recs {
		kind, why := classifyDeltaRec(r, root, dataLow)
		switch kind {
		case deltaSkip:
			// CLOSE 这类不改变任何数字：跳过，不是失败。
			continue
		case deltaFallback:
			return nil, why
		case deltaCreate:
			cur.Total += r.NewSize
			cur.Files++
			cur.Cats[catForPath(r.Path)] += r.NewSize
		case deltaDelete:
			// 快照里总量比这条文件还小：基线本身就说不通，别往下减。
			if r.OldSize > cur.Total {
				return nil, fmt.Sprintf("删除 %s 会让总量变负（基线 %d < 该文件 %d），基线不可信",
					r.Path, cur.Total, r.OldSize)
			}
			cat := catForPath(r.Path)
			if r.OldSize > cur.Cats[cat] {
				return nil, fmt.Sprintf("删除 %s 会让分类 %s 变负，基线不可信", r.Path, cat)
			}
			cur.Total -= r.OldSize
			cur.Cats[cat] -= r.OldSize
			cur.Files--
		case deltaWrite:
			cat := catForPath(r.Path)
			if r.NewSize >= r.OldSize {
				grow := r.NewSize - r.OldSize
				cur.Total += grow
				cur.Cats[cat] += grow
			} else {
				shrink := r.OldSize - r.NewSize
				if shrink > cur.Total || shrink > cur.Cats[cat] {
					return nil, fmt.Sprintf("缩小 %s 会让总量/分类变负，基线不可信", r.Path)
				}
				cur.Total -= shrink
				cur.Cats[cat] -= shrink
			}
		}
	}
	cur.Unique = uniqueBytes(cur.Total, cur.Shared)
	if why := checkScanInvariants(cur); why != "" {
		return nil, why
	}
	return cur, ""
}

// checkScanInvariants 是增量路径的闸门：把「这份快照还像不像一份真扫描结果」查一遍。
// 两条都沿用既有口径：Unique+Shared==Total（硬链接三口径自洽）、sum(Cats)==Total（分类不漏账）。
func checkScanInvariants(sc *Scan) string {
	if sc.Unique+sc.Shared != sc.Total {
		return fmt.Sprintf("不变量不满足：Unique(%d) + Shared(%d) != Total(%d)", sc.Unique, sc.Shared, sc.Total)
	}
	var sum uint64
	for _, v := range sc.Cats {
		sum += v
	}
	if sum != sc.Total {
		return fmt.Sprintf("不变量不满足：分类合计(%d) != Total(%d)", sum, sc.Total)
	}
	if sc.Files < 0 {
		return "不变量不满足：文件数为负"
	}
	return ""
}

// catForPath 给一个文件路径判分类，规则和扫描时保持一致：
// 先按所在目录分类，再补上几个必须固定分类的系统大文件。
func catForPath(path string) string {
	dir := parentDir(path)
	base := path
	if i := strings.LastIndexByte(path, '\\'); i >= 0 && i+1 < len(path) {
		base = path[i+1:]
	}
	switch strings.ToLower(base) {
	case "hiberfil.sys", "swapfile.sys":
		return "system_hiber"
	case "pagefile.sys":
		return "system_pagefile"
	case "memory.dmp":
		return "logs_dumps"
	}
	return classify(strings.ToLower(dir) + `\`)
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
		// 必须深拷贝：缓存里那份是"原件"，会被反复读出来当本次结果交出去。
		// 浅拷贝会让 Cats / Deep / TopFiles 和它共享底层数组，调用方排个序就把缓存改了。
		cp := cloneScan(sc)
		cp.TS = scanStart.Unix()
		cp.Duration = time.Since(scanStart).Seconds()
		return cp, "cached"
	}

	// MFT 引擎在这里才尝试，位置是有讲究的：
	// 它要替代的是「昂贵的那一次全量走盘」，而不是增量更新。
	// 早先它写在函数最开头，结果常驻路径（watch/tray/serve）每次刷新都绕过 USN 缓存、
	// 重读一遍 MFT 表——每 5 秒读 3 秒，比增量更新还慢。那把加速用成了减速。
	if !useCache && useMftEngine {
		letter, isVolume := volumeLetter(cfg.Root)
		// 读之前先记一次日志坐标；读完之后再记一次。两次一致才说明这几秒里
		// 没有任何写入——只有那样的快照才配当「零变更」的基准，交给下一个进程复用。
		var before usnAnchor
		beforeErr := fmt.Errorf("根不是卷根，取不到 USN 日志")
		if isVolume {
			before, beforeErr = usnAnchorReader(letter)
		}
		if sc, ok := scanVolumeViaMft(cfg, cfg.Root); ok {
			// 成功才落缓存，和遍历路径的行为对齐：下一次扫描只要"没变过"，
			// 就能直接复用这份结果，不必再读 110 万条记录。
			// 被取消的那一次数据是残的——残的写进去比没有更坏，它会被当成完整结果。
			if !scanCancelled() {
				anc, why := trustworthyAnchor(letter, before, beforeErr)
				if why != "" {
					// 锚点没落盘，下一次扫描就还得全读 MFT。这条日志是唯一
					// 能解释"为什么快不起来"的地方——判据拒绝了你，得说清是哪一条。
					logf("mft: 这次不落盘锚点，下次仍会全读 MFT —— %s", why)
				}
				saveMftScanCache(cfg, sc, anc)
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
