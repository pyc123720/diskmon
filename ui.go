package main

import (
	"fmt"
	"strings"
	"time"
)

// ---------- 新版界面骨架 ----------

const navCSS = `
a{color:#4c8dff;text-decoration:none}
html{scroll-behavior:smooth}
[id]{scroll-margin-top:64px}
.subnav{position:sticky;top:0;z-index:9;display:flex;gap:4px;flex-wrap:wrap;margin:0 0 14px;padding:8px 10px;background:#0f141c;border:1px solid #1e2430;border-radius:10px}
.subnav a{font-size:12px;color:#aab4c5;padding:4px 9px;border-radius:7px}
.subnav a:hover{background:#1a212c;color:#fff}
.withside{display:grid;grid-template-columns:180px minmax(0,1fr);gap:16px;align-items:start}
.side{position:sticky;top:12px;display:flex;flex-direction:column;gap:2px;background:#141922;border:1px solid #1e2430;border-radius:12px;padding:8px}
.side a{color:#aab4c5;text-decoration:none;font-size:13px;padding:6px 10px;border-radius:8px}
.side a:hover{background:#1a212c;color:#fff}
.side a.on{background:#1c2433;color:#fff;font-weight:600}
@media(max-width:900px){.withside{grid-template-columns:1fr}.side{position:static;flex-direction:row;overflow-x:auto}}
.btn{display:inline-block;padding:8px 16px;border-radius:8px;background:#1c212b;border:1px solid #2a3240;text-decoration:none;margin-right:10px;color:#e7ebf2}
.btn:hover{border-color:#4c8dff88}

.nav{display:flex;gap:6px;align-items:center;margin:0 0 20px;padding:8px 12px;background:#141922;border:1px solid #1e2430;border-radius:12px;flex-wrap:wrap}
.nav .brand{font-weight:700;margin-right:10px;color:#e7ebf2}
.nav a{color:#aab4c5;text-decoration:none;padding:6px 11px;border-radius:8px;font-size:13px}
.nav a:hover{background:#1a212c;color:#fff}
.nav a.on{background:#1c2433;color:#fff}
.tiles{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin:14px 0}
.tile{background:#141922;border:1px solid #1e2430;border-radius:14px;padding:16px;text-decoration:none;color:#e7ebf2;display:block}
.tile:hover{border-color:#4c8dff66}
.tile .t{font-size:15px;font-weight:600;margin-bottom:6px}
.tile .d{color:#8b94a7;font-size:12px;line-height:1.5}
.rec{background:linear-gradient(180deg,#152a1f,#141922);border:1px solid #33c17955;border-radius:16px;padding:18px 20px}
.rec h3{margin:0 0 6px;font-size:16px}
.rec .why{color:#9fb0c9;font-size:13px;margin:6px 0 12px;line-height:1.6}
.ringwrap{display:flex;gap:20px;align-items:center}
.ring{width:168px;height:168px;border-radius:50%;display:flex;align-items:center;justify-content:center;flex:none}
.ring .inner{width:132px;height:132px;border-radius:50%;background:#0e1116;display:flex;flex-direction:column;align-items:center;justify-content:center}
.ring .pct{font-size:26px;font-weight:700}
.ring .lbl{font-size:12px;color:#8b94a7;margin-top:2px}
@media(max-width:900px){.tiles{grid-template-columns:1fr 1fr}.ringwrap{flex-direction:column;align-items:flex-start}}
`

var navItems = []struct{ Label, Href, Key string }{
	{"总览", "/", "overview"},
	{"清理", "/clean", "clean"},
	{"趋势", "/trend", "trend"},
	{"详情", "/detail", "detail"},
	{"规则", "/rules", "rules"},
	{"媒体", "/media", "media"},
	{"重复", "/dup", "dup"},
	{"残留", "/leftovers", "leftovers"},
	{"设置", "/settings", "settings"},
}

func navBar(active string) string {
	var b strings.Builder
	b.WriteString(`<div class="nav"><span class="brand">diskmon</span>`)
	for _, it := range navItems {
		cls := ""
		if it.Key == active {
			cls = ` class="on"`
		}
		b.WriteString(fmt.Sprintf(`<a href="%s"%s>%s</a>`, it.Href, cls, it.Label))
	}
	b.WriteString(`<span style="flex:1"></span><a href="/detail/overview" title="分区页">分区页</a><a href="/detail" title="全部数据">⤢ 全部</a></div>`)
	return b.String()
}

// 统一外壳：所有子页面都用它，保证有导航栏
func appShell(title, active, body string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + htmlEscape(title) + ` · diskmon</title>
<link rel="icon" href="/favicon.ico">
<style>` + css + navCSS + `</style></head><body><div class="wrap">` + navBar(active) + dataDirBanner() + muteBanner() + body + `</div></body></html>`
}

// ---------- 今日建议 ----------

type suggestion struct {
	Title  string
	Why    string
	Href   string
	Action string
	Extra  string
}

func todaySuggestion(cfg Config, st *Store) suggestion {
	// 1) 规则试运行里有可释放的
	stats := st.allRuleStats()
	bestRule := ""
	var bestFree uint64
	for _, r := range cfg.Rules {
		s := stats[r.Name]
		if s.WouldFree > bestFree {
			bestFree = s.WouldFree
			bestRule = r.Name
		}
	}
	if bestFree > 0 {
		return suggestion{
			Title:  "确认一条规则，常驻自动清理",
			Why:    fmt.Sprintf("规则「%s」试运行显示可释放 %s。确认启用后，它会按天自动处理；不放心可以先点「立即试运行」再决定。", bestRule, human(bestFree)),
			Href:   "/rules",
			Action: "去确认启用",
			Extra:  "全部动作可还原（7 天）",
		}
	}
	// 2) 保守方案
	if p := buildPlan(cfg, st, "conservative"); len(p.Dirs) > 0 {
		return suggestion{
			Title:  "先做保守清理",
			Why:    fmt.Sprintf("保守方案能回收 %s（%d 个目录，全部是可清类：临时文件/开发缓存/更新缓存/日志）。执行后 7 天内可还原。", human(p.Bytes), len(p.Dirs)),
			Href:   "/plan/preview?key=conservative",
			Action: "演练并清理",
			Extra:  fmt.Sprintf("标准 %s · 激进 %s", human(buildPlan(cfg, st, "standard").Bytes), human(buildPlan(cfg, st, "aggressive").Bytes)),
		}
	}
	// 3) 重复文件
	if r := loadDups(cfg); r != nil && r.TotalWasted > 0 {
		return suggestion{
			Title:  "处理重复文件",
			Why:    fmt.Sprintf("上次扫描发现 %d 组重复，可释放 %s。每组保留第一份，其余进隔离区。", len(r.Groups), human(r.TotalWasted)),
			Href:   "/dup",
			Action: "查看重复文件",
		}
	}
	// 4) 兜底
	return suggestion{
		Title:  "先扫一遍拿到数据",
		Why:    "还没有扫描结果。全盘扫描约 100 秒，之后分类、方案、趋势都会有数字。",
		Href:   "/scan",
		Action: "开始扫描",
	}
}

// ---------- 总览页 ----------

func overviewPage(cfg Config, st *Store) string {
	v, _ := volumeStats(cfg.Root)
	level := levelOf(v, cfg)
	color := levelColor[level]
	pct := 0.0
	if v.Total > 0 {
		pct = float64(v.Total-v.Free) / float64(v.Total) * 100
	}
	sug := todaySuggestion(cfg, st)
	var b strings.Builder

	b.WriteString(firstRunBanner(cfg, st))
	b.WriteString(`<div class="grid g2">`)

	// 左：容量环
	b.WriteString(`<div class="card"><h2>系统盘 C:</h2><div class="ringwrap">`)
	b.WriteString(fmt.Sprintf(`<div class="ring" style="background:conic-gradient(%s 0 %.1f%%, #232a36 %.1f%% 100%%)"><div class="inner"><div class="pct">%.1f%%</div><div class="lbl">已用</div></div></div>`,
		color, pct, pct, pct))
	b.WriteString(`<div>`)
	b.WriteString(fmt.Sprintf(`<div style="font-size:26px;font-weight:700">%s <span class="muted" style="font-size:13px">剩余</span></div>`, human(v.Free)))
	b.WriteString(fmt.Sprintf(`<div class="muted">总容量 %s · 已用 %s</div>`, human(v.Total), human(v.Total-v.Free)))
	b.WriteString(fmt.Sprintf(`<div style="margin-top:8px"><span class="pill" style="background:%s22;color:%s;border:1px solid %s55">%s</span></div>`, color, color, color, levelText(level)))
	if sc := st.GetScan(); sc != nil {
		b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:10px">上次扫描 %s · %d 文件 · 耗时 %ds</div>`,
			time.Unix(sc.TS, 0).Format("01-02 15:04"), sc.Files, int(sc.Duration)))
	}
	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/scan">重新扫描</a><a class="btn" href="/detail">全部数据</a></div>`)
	b.WriteString(`</div></div></div>`)

	// 右：今日建议
	b.WriteString(`<div class="card"><h2>今日建议</h2><div class="rec">`)
	b.WriteString(`<h3>` + htmlEscape(sug.Title) + `</h3>`)
	b.WriteString(`<div class="why">` + htmlEscape(sug.Why) + `</div>`)
	b.WriteString(`<a class="btn" style="background:#1f6f4a;border-color:#33c179" href="` + sug.Href + `">` + htmlEscape(sug.Action) + `</a>`)
	if sug.Extra != "" {
		b.WriteString(`<span class="muted" style="margin-left:10px">` + htmlEscape(sug.Extra) + `</span>`)
	}
	b.WriteString(`</div></div></div>`)

	// 懒人清理
	lp := lazyPreview(cfg, st)
	if len(lp.Dirs) > 0 {
		b.WriteString(`<div class="card" style="margin-top:14px;border-color:#33c17955"><h2>懒人清理</h2>`)
		b.WriteString(`<div style="display:flex;align-items:center;gap:18px;flex-wrap:wrap">`)
		b.WriteString(fmt.Sprintf(`<div><span class="big" style="font-size:26px;font-weight:700">%s</span> <span class="muted">可释放 · %d 个目录</span></div>`, human(lp.Bytes), len(lp.Dirs)))
		b.WriteString(`<a class="btn" style="background:#1f6f4a;border-color:#33c179;padding:10px 20px" href="/lazy">一键清理（可撤销）</a>`)
		b.WriteString(`<span class="muted">只清临时/开发缓存/更新缓存/日志，全部进隔离区</span>`)
		b.WriteString(`</div></div>`)
	}

	// 快捷入口
	b.WriteString(`<div class="tiles">`)
	tiles := []struct{ T, D, H string }{
		{"清理中心", "一键方案 · 按应用 · 隔离区", "/clean"},
		{"增长与趋势", "预测、每日增量、对比上次", "/trend"},
		{"重复文件", "200 组待处理时可回收 7 GB+", "/dup"},
		{"自动规则", "阈值 + 天数 + 执行窗口", "/rules"},
		{"卸载残留", "注册表孤立 + 数据残留", "/leftovers"},
		{"全部数据", "分类、Treemap、目录、文件", "/detail"},
	}
	for _, t := range tiles {
		b.WriteString(fmt.Sprintf(`<a class="tile" href="%s"><div class="t">%s</div><div class="d">%s</div></a>`, t.H, t.T, t.D))
	}
	b.WriteString(`</div>`)

	// 最近告警
	alerts := st.Alerts()
	var shown []Alert
	for i := len(alerts) - 1; i >= 0 && len(shown) < 6; i-- {
		if importantAlert(alerts[i].Kind) {
			shown = append(shown, alerts[i])
		}
	}
	b.WriteString(`<div class="card"><h2>最近告警</h2>`)
	if len(shown) == 0 {
		b.WriteString(`<div class="muted">暂无重要告警。增长或容量变化时会出现在这里。</div>`)
	} else {
		b.WriteString(`<table><tr><th style="width:96px">时间</th><th>事件</th></tr>`)
		for _, a := range shown {
			b.WriteString(fmt.Sprintf(`<tr><td class="muted">%s</td><td>%s</td></tr>`,
				time.Unix(a.TS, 0).Format("01-02 15:04"), htmlEscape(shortText(a.Text, 120))))
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(scanMismatchBanner(cfg, st))
	return appShell("总览", "overview", b.String())
}

// ---------- 清理中心 ----------

func cleanPage(cfg Config, st *Store) string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">清理中心</h2>`)
	b.WriteString(`<p class="muted">所有清理动作都先进隔离区，7 天内可还原；永久删除只走 L2 规则。</p>`)

	b.WriteString(`<div class="card"><h2>媒体分类</h2><p class="muted">按图片/视频/文档/音频/压缩包拆开看，可只清 30 天前的某一类。</p><a class="btn" href="/media">打开媒体分类</a></div>`)
	b.WriteString(`<div class="card" style="border-color:#33c17955"><h2>懒人清理</h2><p class="muted">不想挑？一个按钮跑一轮最安全的清理，全部可撤销。</p><a class="btn" style="background:#1f6f4a;border-color:#33c179" href="/lazy">打开懒人清理</a></div>`)
	b.WriteString(`<div class="card"><h2>一键方案</h2>` + planCardHTML(cfg, st) + `</div>`)

	// 按应用（前 6）
	groups := appGroups(cfg, st)
	if len(groups) > 6 {
		groups = groups[:6]
	}
	b.WriteString(`<div class="card"><h2>按应用聚合</h2><table><tr><th>应用</th><th style="text-align:right">可回收</th><th></th></tr>`)
	for _, g := range groups {
		b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td class="r">%s</td><td><a class="act" href="/app?name=%s">查看</a></td></tr>`,
			htmlEscape(g.Name), human(g.Bytes), urlQuery(g.Name)))
	}
	b.WriteString(`</table></div>`)

	b.WriteString(`<div class="grid g2"><div class="card"><h2>重复文件</h2>` + dupCardHTML(cfg) + `</div>`)
	b.WriteString(`<div class="card"><h2>卸载残留</h2>` + leftoversCardHTML(cfg, st) + `</div></div>`)

	// 隔离区
	q := openQuarantine(cfg)
	var qb uint64
	n := 0
	for _, e := range q.entries() {
		if !e.Restored {
			qb += e.Bytes
			n++
		}
	}
	b.WriteString(`<div class="card"><h2>隔离区</h2>`)
	b.WriteString(fmt.Sprintf(`<div class="kpi"><div><span class="big">%s</span><span class="unit"> 已隔离（%d 项）</span></div><div class="muted">上限 %d GB · 保留 %d 天</div></div>`,
		human(qb), n, cfg.QuarantineMaxGB, cfg.QuarantineDays))
	b.WriteString(`<div class="muted">还原与清理在「全部数据 → 隔离区」里操作。</div>`)
	b.WriteString(`<div style="margin-top:8px"><a class="act" href="/q/rebuild">重建隔离索引</a></div></div>`)

	// 清理记录
	hist := cleanHistory(st, 8)
	b.WriteString(`<div class="card"><h2>清理记录</h2>`)
	if len(hist) == 0 {
		b.WriteString(`<div class="muted">还没有清理记录。</div>`)
	} else {
		b.WriteString(`<table><tr><th style="width:96px">时间</th><th>事件</th></tr>`)
		for _, h := range hist {
			b.WriteString(fmt.Sprintf(`<tr><td class="muted">%s</td><td>%s</td></tr>`,
				time.Unix(h.TS, 0).Format("01-02 15:04"), htmlEscape(h.Text)))
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`</div>`)

	return appShell("清理中心", "clean", b.String())
}

func shortText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func importantAlert(kind string) bool {
	switch kind {
	case "growth", "quarantine", "engine", "dup-scan", "rule-run", "plan-run", "purge", "restore":
		return true
	}
	return false
}

// ---------- 趋势页 ----------

func trendPage(cfg Config, st *Store) string {
	v, _ := volumeStats(cfg.Root)
	level := levelOf(v, cfg)
	freeGB := gb(v.Free)
	sc := st.GetScan()

	var deltas []DayDelta
	source := "mtime 回溯（≈每日新增写入量）"
	if net := netGrowthSamples(st.st.Samples); len(net) >= 3 {
		deltas = net
		source = "真实采样（每日净增长）"
	} else if sc != nil {
		deltas = sortedDayWrites(sc.DayWrites)
	}
	if len(deltas) > 21 {
		deltas = deltas[len(deltas)-21:]
	}
	f := forecastFrom(v.Free, deltas, source)

	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">增长与趋势</h2>`)
	b.WriteString(fmt.Sprintf(`<p class="muted">剩余 %s（%s）· 数据来源：%s</p>`,
		human(v.Free), levelText(level), htmlEscape(source)))

	// 预测
	b.WriteString(`<div class="card"><h2>写满预测</h2>`)
	if f.OK {
		b.WriteString(fmt.Sprintf(`<div class="kpi"><div><span class="big">%s</span><span class="unit"> 后写满（中位）</span></div><div class="muted">区间 %s ～ %s · 等效增速 %.0f MB/天</div></div>`,
			fmtDur(f.DaysP50, true), fmtDur(f.DaysFast, true), fmtDur(f.DaysSlow, true), f.SlopeMB))
		if strings.Contains(source, "mtime") {
			b.WriteString(`<div class="muted" style="color:#c9a24a">⚠ 按写入量估算：实际净增长通常更慢，累积 3 天采样后自动切换</div>`)
		}
		b.WriteString(svgProjection(f, freeGB, cfg))
	} else {
		b.WriteString(`<div class="empty">暂无增长数据（先扫描一次）</div>`)
	}
	b.WriteString(`</div>`)

	// 每日新增 + 异常
	b.WriteString(fmt.Sprintf(`<div class="grid g2" style="margin-top:14px"><div class="card"><h2>每日新增（近 %d 天）</h2>`, len(deltas)))
	b.WriteString(svgBars(deltas))
	b.WriteString(`<div class="muted">红色柱为检测到的异常跳变日。</div></div>`)
	b.WriteString(`<div class="card"><h2>增长性质判定</h2>`)
	anoms := anomalies(deltas)
	if len(anoms) == 0 {
		b.WriteString(`<div class="empty">未检测到异常增长</div>`)
	} else {
		for _, a := range anoms {
			tag := "⚠ 跳变"
			if a.Kind == "leak" {
				tag = "🔥 泄漏"
			}
			b.WriteString(`<div class="alert"><span class="when">` + a.Day + `</span><span>` + tag + `　` + htmlEscape(a.Text) + `</span></div>`)
		}
	}
	b.WriteString(`<div class="muted" style="margin-top:8px">一次性跳变=安装/下载；持续泄漏=缓存/日志在积累。</div></div></div>`)

	// 对比
	b.WriteString(`<div class="card" style="margin-top:14px"><h2>对比上次 / 一周前</h2>`)
	b.WriteString(diffHTML(cfg))
	b.WriteString(`</div>`)

	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/detail#forecast">详情：预测</a><a class="btn" href="/detail#daily">详情：每日</a><a class="btn" href="/detail#diff">详情：对比</a><a class="btn" href="/detail">全部数据</a><a class="btn" href="/scan">重新扫描</a></div>`)
	return appShell("增长与趋势", "trend", b.String())
}

func subNav(items [][2]string) string {
	var b strings.Builder
	b.WriteString(`<div class="subnav">`)
	for _, it := range items {
		b.WriteString(`<a href="` + it[1] + `">` + htmlEscape(it[0]) + `</a>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// 数据目录降级时的醒目提示（P0：不能静默换目录）
func dataDirBanner() string {
	if !dataDirFallback {
		return ""
	}
	return `<div class="warn" style="margin-bottom:14px">⚠ 数据目录已降级：<code>` +
		htmlEscape(dataDirActual) + `</code>（原 <code>` + htmlEscape(dataDirRequested) +
		`</code> 不可写）。历史、隔离区、规则统计都在新位置；修好原盘后可改回 config.json 的 data_dir。</div>`
}

// 切换扫描根目录后，旧扫描数据不属于当前盘：提示重扫，避免误读
func scanMismatchBanner(cfg Config, st *Store) string {
	sc := st.GetScan()
	if sc == nil || sc.Root == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimRight(sc.Root, `\`), strings.TrimRight(cfg.Root, `\`)) {
		return ""
	}
	return `<div class="warn" style="margin-bottom:14px">扫描数据属于 <code>` + htmlEscape(sc.Root) +
		`</code>，当前监测的是 <code>` + htmlEscape(cfg.Root) + `</code>。` +
		`<a href="/scan">重新扫描当前盘</a> 之后两边数据才一致。</div>`
}

// 首次使用（还没有扫描数据）时给三步引导，避免一进来就迷路
func firstRunBanner(cfg Config, st *Store) string {
	if st.GetScan() != nil {
		return ""
	}
	return `<div class="warn" style="margin-bottom:14px">👋 第一次使用，按三步来：
<b>①</b> 先<a href="/scan">扫描一次</a>（约 1–2 分钟）→
<b>②</b> 回来看「今日建议」→
<b>③</b> 想省事就点「懒人清理」（全部可撤销）。<br>
<span class="muted">监测是只读的；任何清理都先进隔离区，7 天内可还原。</span></div>`
}
