package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ---------- 今日建议 ----------
//
// 设计约束：卡片必须随「今天实际发生了什么」变化，而不是按固定优先级永远停在同一条。
// 所以每条建议都要带一个来自实时状态的依据（数据新鲜度、今日净增、今日已回收、剩余空间），
// 没有依据就宁可说「今天没事」——静态的建议等于没有建议。

type todayItem struct {
	Kind   string // urgent / attention / quiet / done
	Title  string
	Why    string
	Href   string
	Action string
}

type todayBoard struct {
	Basis string
	Items []todayItem
}

// bi 按当前语言二选一。今日建议的文案全部走这里，避免依赖 i18n.go 的整句替换表。
func bi(zh, en string) string {
	if isEN() {
		return en
	}
	return zh
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return bi("刚刚", "just now")
	case d < time.Hour:
		return bi(fmt.Sprintf("%d 分钟前", int(d.Minutes())), fmt.Sprintf("%d min ago", int(d.Minutes())))
	case d < 24*time.Hour:
		return bi(fmt.Sprintf("%.1f 小时前", d.Hours()), fmt.Sprintf("%.1f h ago", d.Hours()))
	default:
		return bi(fmt.Sprintf("%.1f 天前", d.Hours()/24), fmt.Sprintf("%.1f d ago", d.Hours()/24))
	}
}

func todayBoardFor(cfg Config, st *Store) todayBoard {
	v, _ := volumeStats(cfg.Root)
	level := levelOf(v, cfg)
	sc := st.GetScan()

	// ---- 增长数据：优先真实采样，其次按 mtime 估算 ----
	var deltas []DayDelta
	src := bi("按文件修改时间估算", "estimated from mtimes")
	if net := netGrowthSamples(st.st.Samples); len(net) >= 3 {
		deltas = net
		src = bi("真实采样", "sampled")
	} else if sc != nil {
		deltas = sortedDayWrites(sc.DayWrites)
	}
	if len(deltas) > 21 {
		deltas = deltas[len(deltas)-21:]
	}
	f := forecastFrom(v.Free, deltas, src)

	today := time.Now().Format("2006-01-02")
	var todayGrow uint64
	hasToday := false
	for _, d := range deltas {
		if d.Day == today {
			todayGrow, hasToday = d.Bytes, true
		}
	}
	var recent []float64
	for _, d := range deltas {
		recent = append(recent, float64(d.Bytes))
	}
	if len(recent) > 7 {
		recent = recent[len(recent)-7:]
	}
	med := uint64(median(recent))

	// ---- 今天已经回收了多少（隔离区，持久化且带时间戳） ----
	var qToday uint64
	qOpen := 0
	for _, e := range openQuarantine(cfg).entries() {
		if e.Restored {
			continue
		}
		qOpen++
		if time.Unix(e.TS, 0).Format("2006-01-02") == today {
			qToday += e.Bytes
		}
	}

	// ---- 依据行：这张卡片「为什么是今天」 ----
	var basis strings.Builder
	scanAge := time.Duration(0)
	if sc != nil {
		scanAge = time.Since(time.Unix(sc.TS, 0))
		basis.WriteString(bi(
			fmt.Sprintf("依据 %s 的扫描（%s）", time.Unix(sc.TS, 0).Format("01-02 15:04"), humanAge(scanAge)),
			fmt.Sprintf("based on the %s scan (%s)", time.Unix(sc.TS, 0).Format("01-02 15:04"), humanAge(scanAge))))
	} else {
		basis.WriteString(bi("还没有扫描数据", "no scan yet"))
	}
	if hasToday {
		basis.WriteString(bi(fmt.Sprintf(" · 今日净增 %s", human(todayGrow)), fmt.Sprintf(" · +%s today", human(todayGrow))))
	} else if len(deltas) > 0 && scanAge < 24*time.Hour {
		basis.WriteString(bi(" · 今日暂无增量", " · no growth today yet"))
	}
	if med > 0 {
		basis.WriteString(bi(fmt.Sprintf(" · 近 7 天中位 %s/天", human(med)), fmt.Sprintf(" · median %s/day", human(med))))
	}
	if f.OK && !math.IsInf(f.DaysP50, 1) && f.DaysP50 > 0 && f.DaysP50 < 3650 {
		basis.WriteString(bi(fmt.Sprintf(" · 约 %s后写满", fmtDur(f.DaysP50, true)), fmt.Sprintf(" · full in ~%s", fmtDur(f.DaysP50, true))))
	}

	b := todayBoard{Basis: basis.String()}

	// ---- 首次使用 ----
	if sc == nil {
		b.Items = append(b.Items, todayItem{"attention",
			bi("先扫一遍拿到数据", "Scan once to get data"),
			bi("还没有扫描结果。全盘扫描约 1–2 分钟，之后分类、方案、趋势都会有数字。",
				"No scan yet. A full scan takes about 1–2 minutes; categories, presets and trends need numbers."),
			"/scan", bi("开始扫描", "Start scan")})
		return b
	}

	tight := level == "danger" || level == "critical" || (f.OK && f.DaysP50 < 7)

	// ---- 1. 空间紧张：优先级最高，必须先腾地方 ----
	if tight {
		plan := buildPlan(cfg, st, "conservative")
		why := bi(fmt.Sprintf("剩余 %s。", human(v.Free)), fmt.Sprintf("%s free.", human(v.Free)))
		if f.OK && !math.IsInf(f.DaysP50, 1) && f.DaysP50 < 3650 {
			why += bi(fmt.Sprintf("按最近 %s/天 的增速，约 %s后写满。", human(med), fmtDur(f.DaysP50, true)),
				fmt.Sprintf(" At %s/day it fills up in ~%s.", human(med), fmtDur(f.DaysP50, true)))
		}
		if hasToday && todayGrow > 0 {
			why += bi(fmt.Sprintf("今天已经涨了 %s。", human(todayGrow)), fmt.Sprintf(" It has grown %s today.", human(todayGrow)))
		}
		if len(plan.Dirs) > 0 {
			why += bi(fmt.Sprintf("保守方案能回收 %s，全部进隔离区，7 天内可还原。", human(plan.Bytes)),
				fmt.Sprintf(" The conservative preset reclaims %s, all undoable for 7 days.", human(plan.Bytes)))
			b.Items = append(b.Items, todayItem{"urgent", bi("先腾出空间", "Free up space first"), why,
				"/plan/preview?key=conservative", bi("演练并清理", "Preview & clean")})
		} else {
			b.Items = append(b.Items, todayItem{"urgent", bi("先查是谁在写盘", "Find what is writing"),
				why + bi("可清类目录都在阈值以下，先去趋势页看增长来源。", " Nothing safe-tier is above threshold; check the trend page."),
				"/trend", bi("看增长来源", "See growth")})
		}
	}

	// ---- 2. 数据过期：结论基于旧数据就是在骗人 ----
	if scanAge > 48*time.Hour {
		b.Items = append(b.Items, todayItem{"attention",
			bi(fmt.Sprintf("数据是 %.1f 天前的，先重扫", scanAge.Hours()/24), fmt.Sprintf("Data is %.1f days old — rescan", scanAge.Hours()/24)),
			bi("下面的数字都来自那次扫描，增量也可能已经漏掉。重扫一次约 1–2 分钟，只读。",
				"The numbers below come from that scan and may have missed growth. A rescan is read-only and takes 1–2 minutes."),
			"/scan", bi("重新扫描", "Rescan")})
	}

	// ---- 3. 今天的异常增长：这是让卡片"每天不一样"的主来源 ----
	anomaly := hasToday && todayGrow > 0 && med > 0 && todayGrow > 2*med && todayGrow > 500_000_000
	if anomaly {
		b.Items = append(b.Items, todayItem{"attention",
			bi(fmt.Sprintf("今天涨了 %s，比平时快", human(todayGrow)), fmt.Sprintf("%s today — faster than usual", human(todayGrow))),
			bi(fmt.Sprintf("近 7 天中位是 %s/天，今天已经到 %.1f 倍。一次性跳变通常是安装/更新，持续几天才是泄漏。",
				human(med), float64(todayGrow)/float64(med)),
				fmt.Sprintf("Median is %s/day; today is %.1fx. A one-day jump is usually an install; several days in a row is a leak.", human(med), float64(todayGrow)/float64(med))),
			"/trend", bi("看是谁在涨", "See what grew")})
	}

	// ---- 4. 今天已经清过：给结果，别重复催同一条 ----
	if qToday > 0 {
		b.Items = append(b.Items, todayItem{"done",
			bi(fmt.Sprintf("今天已隔离 %s（%d 项）", human(qToday), qOpen), fmt.Sprintf("%s quarantined today (%d items)", human(qToday), qOpen)),
			bi("还没真正删除，7 天内可以逐项还原。注意：空间要等隔离区清空（或到期）才会回到盘上。",
				"Nothing is deleted yet — restore any item within 7 days. Note: the space only comes back once quarantine is purged or expires."),
			"/q", bi("看/撤销隔离项", "Review / restore")})
	}

	// ---- 5. 可执行的机会：只在确实够大时才提 ----
	plan := buildPlan(cfg, st, "conservative")
	if len(plan.Dirs) > 0 && plan.Bytes >= 200_000_000 && !tight {
		b.Items = append(b.Items, todayItem{"attention",
			bi(fmt.Sprintf("保守方案可回收 %s", human(plan.Bytes)), fmt.Sprintf("Conservative preset reclaims %s", human(plan.Bytes))),
			bi(fmt.Sprintf("%d 个可清目录（临时文件/开发缓存/更新缓存/日志）。全部进隔离区，随时可撤销。", len(plan.Dirs)),
				fmt.Sprintf("%d safe-tier folders (temp / dev cache / update cache / logs). Quarantined, undoable.", len(plan.Dirs))),
			"/plan/preview?key=conservative", bi("演练并清理", "Preview & clean")})
	}

	// ---- 6. 兜底：真的没事就说没事，并交代下一次检查 ----
	if len(b.Items) == 0 {
		if r := loadDups(cfg); r != nil && r.TotalWasted > 0 {
			b.Items = append(b.Items, todayItem{"attention",
				bi(fmt.Sprintf("有 %d 组重复文件，可释放 %s", len(r.Groups), human(r.TotalWasted)),
					fmt.Sprintf("%d duplicate groups, %s reclaimable", len(r.Groups), human(r.TotalWasted))),
				bi("每组保留第一份，其余进隔离区。", "One copy is kept per group; the rest go to quarantine."),
				"/dup", bi("查看重复文件", "Review duplicates")})
		}
	}
	if len(b.Items) == 0 {
		why := bi("剩余空间正常，没有异常增长，也没有需要处理的隔离项。",
			"Space is fine, no unusual growth, nothing waiting in quarantine.")
		if cfg.AutoScanHours > 0 {
			why += bi(fmt.Sprintf("下次自动扫描在 %d 小时内。", cfg.AutoScanHours), fmt.Sprintf(" Next auto-scan within %d h.", cfg.AutoScanHours))
		}
		b.Items = append(b.Items, todayItem{"quiet",
			bi("今天没有需要处理的事", "Nothing to do today"), why, "/trend", bi("看一眼趋势", "Check the trend")})
	}

	// 最多三条：一条主建议 + 两条补充
	if len(b.Items) > 3 {
		b.Items = b.Items[:3]
	}
	return b
}
