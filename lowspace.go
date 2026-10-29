package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------- 低空间阈值 → 行为（C4） ----------
//
// 这一层是「阈值终于有事可做」的那一半，也是磁盘工具里最容易变成事故的地方。
// 所以先把边界钉死，再谈功能：
//
//	本文件允许的全部动作：notify（托盘/toast）+ logf + st.AddAlert + 至多一次只读扫描。
//	本文件不允许：任何删除、隔离、移动、改配置。
//
// 这句话不是靠自觉守的，是靠结构守的：
//   - 这里没有 os.Remove / os.RemoveAll / q.apply / runRule 的任何调用；
//   - 唯一的"重"动作是 lowSpaceScan，它只调 runScan——那是一条纯读取的扫描；
//   - 测试把 lowSpaceScan 换成计数器，就能证明「通知一次 + 扫描至多一次」以外的副作用不存在。
//
// 为什么不做自动清理：阈值穿越可能是插了块快照盘、某个程序临时写了 50 GB、
// 或者用户刚挂上备份盘。这些场景下自动删文件没有任何挽回余地，而人点一下只要两秒。
// 删除永远只在人在场时发生。

// lowSpaceState 是防抖状态。要落盘：进程重启后不能重复通知同一档。
type lowSpaceState struct {
	Root   string `json:"root"`
	Level  string `json:"level"` // 已经通知过的档位；"ok" 表示已复位，可以再次触发
	Free   uint64 `json:"free"`
	At     int64  `json:"at"`
	Notify int    `json:"notify"`  // 累计通知次数（排查用）
	ScanAt int64  `json:"scan_at"` // 最近一次因低空间自动触发的只读扫描
	Scanned bool  `json:"scanned"` // 本次"跌破"里是否已经尝试过自动扫描
}

// 防抖状态落在数据目录，跟 rules-stats.json 同级。
// 放在 state.json 里会和托盘/命令行进程互相覆盖，这里独立成文件也是同一个理由。
func lowSpaceStatePath(cfg Config) string {
	return filepath.Join(cfg.DataDir, "lowspace.json")
}

var lowSpaceCache struct {
	mu    sync.Mutex
	path  string
	state lowSpaceState
	valid bool
}

func loadLowSpaceState(cfg Config) lowSpaceState {
	p := lowSpaceStatePath(cfg)
	b, err := os.ReadFile(p)
	if err != nil {
		return lowSpaceState{Root: cfg.Root}
	}
	var s lowSpaceState
	if json.Unmarshal(b, &s) != nil {
		return lowSpaceState{Root: cfg.Root}
	}
	return s
}

func saveLowSpaceState(cfg Config, s lowSpaceState) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(lowSpaceStatePath(cfg), b)
}

func levelRank(l string) int {
	switch l {
	case "critical":
		return 3
	case "danger":
		return 2
	case "warn":
		return 1
	}
	return 0
}

// 恢复带：余量必须回升到「阈值 + 恢复带」以上才算复位。
// 没有这条，20.1 GB → 19.9 GB 这种抖动会来回刷屏；
// 有了它，一次跌破只通知一次，除非真的缓解了再跌破。
const (
	lowSpacePctBand = 2.0 // 百分比档：回升 2 个百分点
	lowSpaceMinBand = 0.5 // GB 档：至少回升 0.5 GB
	lowSpaceMaxBand = 5.0 // GB 档：最多看 5 GB（阈值很大时别设一个永远达不到的带）
)

func lowSpaceGBBand(cfg Config) float64 {
	b := cfg.WarnGB * 0.10
	if b < lowSpaceMinBand {
		b = lowSpaceMinBand
	}
	if b > lowSpaceMaxBand {
		b = lowSpaceMaxBand
	}
	return b
}

// recovered 判断余量是否已经明显回升（越过警告阈值 + 恢复带）。
func recovered(cfg Config, free, total uint64) bool {
	if gb(free) < cfg.WarnGB+lowSpaceGBBand(cfg) {
		return false
	}
	if total > 0 {
		if float64(free)/float64(total)*100 < cfg.WarnPct+lowSpacePctBand {
			return false
		}
	}
	return true
}

// decideLowSpace 是纯函数：只根据「上次状态 + 这次观测」决定要不要通知。
//  1. 档位变差（ok→warn→danger→critical）→ 通知；
//  2. 档位不变 → 不通知；
//  3. 档位变好但没越过恢复带 → 不通知、也不复位（所以紧接着再跌破不会重复通知）；
//  4. 越过恢复带 → 复位成 ok，此后再次跌破才会重新通知。
func decideLowSpace(prev lowSpaceState, cfg Config, free, total uint64) (lowSpaceState, bool) {
	next := prev
	next.Free = free
	next.At = time.Now().Unix()

	// 换了盘（改了扫描根目录）就从头算：新盘的余量跟旧盘的状态没有可比性。
	if prev.Root != cfg.Root {
		next = lowSpaceState{Root: cfg.Root, Free: free, At: time.Now().Unix()}
	}

	lvl := levelOf(Volume{Free: free, Total: total}, cfg)

	if lvl == "ok" && recovered(cfg, free, total) {
		next.Level = "ok"
		next.Scanned = false
		return next, false
	}
	if lvl != "ok" && levelRank(lvl) > levelRank(next.Level) {
		next.Level = lvl
		next.Scanned = false // 新的一档：允许一次只读扫描
		return next, true
	}
	return next, false
}

// lowSpaceMessage 要让人立刻能做决定：现在还剩多少、哪条线破了、去哪儿看。
func lowSpaceMessage(cfg Config, free, total uint64) string {
	lvl := levelOf(Volume{Free: free, Total: total}, cfg)
	pct := 0.0
	if total > 0 {
		pct = float64(free) / float64(total) * 100
	}
	var line string
	switch lvl {
	case "critical":
		line = fmt.Sprintf("阈值 %s / %.0f%%", trimZero(cfg.CriticalGB), cfg.CriticalPct)
	case "danger":
		line = fmt.Sprintf("阈值 %s / %.0f%%", trimZero(cfg.DangerGB), cfg.DangerPct)
	default:
		line = fmt.Sprintf("阈值 %s / %.0f%%", trimZero(cfg.WarnGB), cfg.WarnPct)
	}
	return fmt.Sprintf("%s 剩余 %s（%.1f%%）已进入「%s」档（%s）。去看哪些目录占空间：http://127.0.0.1:%d/clean",
		cfg.Root, human(free), pct, levelText(lvl), line, cfg.Port)
}

func trimZero(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%.0f GB", f)
	}
	return fmt.Sprintf("%.1f GB", f)
}

// lowSpaceScan 是自动扫描的唯一入口，做成可注入：测试用它证明「至多一次」。
// 返回 true 表示真的启动了一次扫描。
var lowSpaceScan = func(cfg Config, st *Store) bool {
	// 手动扫描在跑时 scanMu 被占着：这时绝不能再叠一条扫描（两倍 I/O）。
	// 这里没有直接复用 startScanAsync，因为它在"忙"时静默返回，我们无法区分
	// 「因为忙没扫」和「扫起来了」——而这两件事在日志里必须写清楚。
	if !scanMu.TryLock() {
		return false
	}
	go func() {
		defer scanMu.Unlock()
		runScan(cfg, st) // 只读：遍历目录、产出统计，不碰任何文件
	}()
	return true
}

// observeSpace 是采样循环每轮都会调的一步入口。
// 通知发不出去也要留痕：先落页面可见的告警与日志，再去弹通知。
func observeSpace(cfg Config, st *Store, v Volume) string {
	lvl := levelOf(v, cfg)
	if v.Total == 0 || cfg.Root == "" {
		return lvl // 读数不可信时不动作，也不要污染防抖状态
	}

	lowSpaceCache.mu.Lock()
	defer lowSpaceCache.mu.Unlock()

	p := lowSpaceStatePath(cfg)
	if !lowSpaceCache.valid || lowSpaceCache.path != p {
		lowSpaceCache.state = loadLowSpaceState(cfg)
		lowSpaceCache.path = p
		lowSpaceCache.valid = true
	}

	prev := lowSpaceCache.state
	next, fire := decideLowSpace(prev, cfg, v.Free, v.Total)

	if !fire {
		// 状态没变就不写盘：这是每 5 秒跑一次的路径。
		if next.Level != prev.Level || next.Scanned != prev.Scanned || next.Free != prev.Free {
			lowSpaceCache.state = next
			saveLowSpaceState(cfg, next)
		}
		return lvl
	}

	msg := lowSpaceMessage(cfg, v.Free, v.Total)
	next.Notify = prev.Notify + 1
	next.Scanned = true // 本次跌破只尝试一次扫描，不论成败
	next.ScanAt = time.Now().Unix()

	// 留痕优先：告警流是页面可见的，日志是事后可查的；通知是锦上添花。
	if st != nil {
		st.AddAlert("lowspace", msg)
	}
	logf("lowspace: level=%s free=%s/%s msg=%s", lvl, human(v.Free), human(v.Total), msg)

	// 只读扫描在这里触发，且只在这一刻（每次跌破一次）。
	if scanStarted := lowSpaceScan(cfg, st); scanStarted {
		logf("lowspace: 已触发一次只读扫描（可用 /scan/cancel 取消）")
	} else {
		logf("lowspace: 跳过自动扫描——已有扫描在跑，不叠加 I/O")
	}

	lowSpaceCache.state = next
	saveLowSpaceState(cfg, next)

	// 最后才弹通知：它可能失败（toast 被策略禁掉、气泡没有托盘句柄），
	// 但上面的告警与日志已经记下了，不会静默丢失。
	notify("diskmon 空间告急", msg, true)
	return lvl
}

// lowSpaceSnapshot 给自检/诊断用：当前防抖状态（不触发任何动作）。
func lowSpaceSnapshot(cfg Config) lowSpaceState {
	lowSpaceCache.mu.Lock()
	defer lowSpaceCache.mu.Unlock()
	p := lowSpaceStatePath(cfg)
	if !lowSpaceCache.valid || lowSpaceCache.path != p {
		return loadLowSpaceState(cfg)
	}
	return lowSpaceCache.state
}
