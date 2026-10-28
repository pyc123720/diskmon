package main

import (
	"fmt"
	"path/filepath"
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
.subnav a.on{background:#1c2433;color:#fff;font-weight:600}
.tsearch{margin:6px 0 4px}
.tsearch input{width:280px;max-width:100%;padding:7px 10px;border-radius:8px;background:#1c212b;border:1px solid #2a3240;color:#e7ebf2;font-size:12px}
th{cursor:default}
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
@media(max-width:820px){
  .grid.g2,.grid.g3,.withside{grid-template-columns:1fr!important}
  .wrap{padding:14px!important}
  .nav{gap:2px}
  table{display:block;overflow-x:auto;white-space:nowrap}
  .side{position:static!important;flex-direction:row!important;overflow-x:auto}
  .ring{width:132px;height:132px}
  .ring .inner{width:104px;height:104px}
}
`

var navItems = []struct{ Label, Href, Key string }{
	{"总览", "/", "overview"},
	{"清理", "/clean", "clean"},
	{"趋势", "/trend", "trend"},
	{"全部数据", "/detail", "detail"},
	{"自动清理", "/rules", "rules"},
	{"查路径", "/inspect", "inspect"},
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
		dot := `<span title="只读" style="display:inline-block;width:6px;height:6px;border-radius:50%;background:#33c179;margin-right:6px;vertical-align:middle"></span>`
		if it.Key == "clean" || it.Key == "rules" {
			dot = `<span title="会移动/删除文件" style="display:inline-block;width:6px;height:6px;border-radius:50%;background:#f5a524;margin-right:6px;vertical-align:middle"></span>`
		}
		b.WriteString(fmt.Sprintf(`<a href="%s"%s>%s%s</a>`, it.Href, cls, dot, it.Label))
	}
	b.WriteString(`<span style="flex:1"></span><span id="actbar" style="display:flex;gap:6px;align-items:center;margin-right:8px"></span>`)
	b.WriteString(`<a href="/export" title="导出 CSV">导出</a>`)
	if isEN() {
		b.WriteString(`<a href="/lang?to=zh" title="切换到中文">中文</a>`)
	} else {
		b.WriteString(`<a href="/lang?to=en" title="Switch to English">EN</a>`)
	}
	if currentTheme() == "light" {
		b.WriteString(`<a href="/theme?mode=dark" title="切换到暗色">🌙 暗色</a>`)
	} else {
		b.WriteString(`<a href="/theme?mode=light" title="切换到亮色">☀ 亮色</a>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// 统一外壳：所有子页面都用它，保证有导航栏
func appShell(title, active, body string) string {
	switch active {
	case "dup", "media", "leftovers":
		active = "clean"
	}
	if active == "clean" {
		body = cleanTabs(cleanTabOf(title)) + body
	}
	html := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + htmlEscape(title) + ` · diskmon</title>
<link rel="icon" href="/favicon.ico">
<style>` + css + navCSS + themeCSS() + `</style></head><body><div class="wrap">` + navBar(active) + dataDirBanner() + muteBanner() + body + uiJS + `</div></body></html>`
	html = strings.Replace(html, "</body></html>", loadOverlay + "</body></html>", 1)
	return translateHTML(html)
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
	board := todayBoardFor(cfg, st)
	var b strings.Builder

	b.WriteString(firstRunBanner(cfg, st))
	b.WriteString(`<div class="grid g2">`)

	// 左：容量环
	rootLabel := strings.ToUpper(strings.TrimRight(filepath.VolumeName(cfg.Root), `\`))
	if rootLabel == "" {
		rootLabel = "C:"
	}
	h2 := "系统盘 " + rootLabel
	if isEN() {
		h2 = "System drive " + rootLabel
	}
	b.WriteString(`<div class="card"><h2>` + h2 + `</h2><div class="ringwrap">`)
	b.WriteString(fmt.Sprintf(`<div class="ring" style="background:conic-gradient(%s 0 %.1f%%, #232a36 %.1f%% 100%%)"><div class="inner"><div class="pct">%.1f%%</div><div class="lbl">已用</div></div></div>`,
		color, pct, pct, pct))
	b.WriteString(`<div>`)
	b.WriteString(fmt.Sprintf(`<div style="font-size:26px;font-weight:700">%s <span class="muted" style="font-size:13px">剩余</span></div>`, human(v.Free)))
	if isEN() {
		b.WriteString(fmt.Sprintf(`<div class="muted">Total %s · used %s</div>`, human(v.Total), human(v.Total-v.Free)))
	} else {
		b.WriteString(fmt.Sprintf(`<div class="muted">总容量 %s · 已用 %s</div>`, human(v.Total), human(v.Total-v.Free)))
	}
	b.WriteString(fmt.Sprintf(`<div style="margin-top:8px"><span class="pill" style="background:%s22;color:%s;border:1px solid %s55">%s</span></div>`, color, color, color, levelText(level)))
	if sc := st.GetScan(); sc != nil {
		if isEN() {
			b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:10px">Last scan %s · %d files · took %ds</div>`,
				time.Unix(sc.TS, 0).Format("01-02 15:04"), sc.Files, int(sc.Duration)))
		} else {
			b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:10px">上次扫描 %s · %d 文件 · 耗时 %ds</div>`,
				time.Unix(sc.TS, 0).Format("01-02 15:04"), sc.Files, int(sc.Duration)))
		}
	}
	if sc2 := st.GetScan(); sc2 != nil {
		src := "全量扫描"
		if isEN() {
			src = "full scan"
			if sc2.Source == "incremental" {
				src = fmt.Sprintf("incremental scan (%d subtrees reused, %d rescanned)", sc2.ReusedChildren, sc2.ScannedChildren)
			} else if sc2.Source == "cached" {
				src = "cached (no writes since last scan)"
			}
			b.WriteString(`<div class="muted" style="margin-top:6px">Source: ` + src + `</div>`)
		} else {
			if sc2.Source == "incremental" {
				src = fmt.Sprintf("增量扫描（复用 %d 个子树，重扫 %d 个）", sc2.ReusedChildren, sc2.ScannedChildren)
			} else if sc2.Source == "cached" {
				src = "缓存（上次扫描后无写入）"
			}
			b.WriteString(`<div class="muted" style="margin-top:6px">数据来源：` + src + `</div>`)
		}
	}
	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/scan">重新扫描</a><a class="btn" href="/scan?full=1">强制全扫</a></div>`)
	b.WriteString(`</div></div></div>`)

	// 右：今日建议（结论随当天状态变化，依据行说明「为什么是今天」）
	if len(board.Items) == 0 { // 兜底：绝不因为建议为空把整页打崩
		board.Items = append(board.Items, todayItem{Kind: "quiet",
			Title:  bi("今天没有需要处理的事", "Nothing to do today"),
			Why:    bi("没有异常增长，也没有待处理的隔离项。", "No unusual growth and nothing waiting in quarantine."),
			Href:   "/trend", Action: bi("看一眼趋势", "Check the trend")})
	}
	head, rest := board.Items[0], board.Items[1:]
	b.WriteString(`<div class="card"><h2>今日建议<span class="muted" style="float:right;font-weight:400;font-size:13px">` +
		time.Now().Format("01-02") + `</span></h2>`)
	b.WriteString(`<div class="muted" style="font-size:12px;line-height:1.6;margin:-2px 0 12px">` + htmlEscape(board.Basis) + `</div>`)
	b.WriteString(`<div class="rec">`)
	b.WriteString(`<h3>` + htmlEscape(head.Title) + `</h3>`)
	b.WriteString(`<div class="why">` + htmlEscape(head.Why) + `</div>`)
	btnStyle := "background:#1f6f4a;border-color:#33c179"
	if head.Kind == "quiet" {
		btnStyle = ""
	}
	b.WriteString(`<a class="btn" style="` + btnStyle + `" href="` + head.Href + `">` + htmlEscape(head.Action) + `</a>`)
	b.WriteString(`</div>`)
	for _, it := range rest {
		dot := "#c9a24a"
		switch it.Kind {
		case "done":
			dot = "#33c179"
		case "urgent":
			dot = "#e05d5d"
		case "quiet":
			dot = "#5c6779"
		}
		b.WriteString(`<div style="margin-top:12px;padding-top:10px;border-top:1px solid #232a36">`)
		b.WriteString(fmt.Sprintf(`<div style="font-size:13px;font-weight:600"><span style="color:%s">●</span> `, dot) +
			htmlEscape(it.Title) + `</div>`)
		b.WriteString(`<div class="muted" style="font-size:12px;line-height:1.6;margin:3px 0 6px">` + htmlEscape(it.Why) + `</div>`)
		b.WriteString(`<a class="act" href="` + it.Href + `">` + htmlEscape(it.Action) + ` →</a>`)
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></div>`) // 今日建议 card, .g2 网格

	// 路径体检入口
	b.WriteString(`<div class="card" style="margin-top:14px"><h2>查路径</h2>
<form method="get" action="/inspect" style="display:flex;gap:8px;flex-wrap:wrap">
<input name="path" placeholder="粘贴一个路径，看看能不能清（不会删除）" style="flex:1;min-width:260px;padding:9px 12px;background:#1c212b;border:1px solid #2a3240;border-radius:10px;color:#e7ebf2">
<button class="btn" type="submit" style="cursor:pointer">查一下</button></form></div>`)

	// 懒人清理（始终显示入口：清理完不能"消失"）
	lp := lazyPreview(cfg, st)
	ls := lazyState()
	b.WriteString(`<div class="card" style="margin-top:14px;border-color:#33c17955"><h2>懒人清理</h2>`)
	switch {
	case ls.Running:
		pct := 0
		if ls.Planned > 0 {
			pct = int(float64(ls.Freed) / float64(ls.Planned) * 100)
		}
		if pct > 100 {
			pct = 100
		}
		state := "清理中"
		switch {
		case ls.Stopping:
			state = "正在停止"
		case ls.Paused:
			state = "已暂停"
		case ls.Measuring:
			state = "正在重新测量"
		}
		b.WriteString(fmt.Sprintf(`<div><span class="big" style="font-size:26px;font-weight:700">%s</span> <span class="muted">/ 计划 %s · %d 个文件已移动 · %s</span></div>`,
			human(ls.Freed), human(ls.Planned), ls.Files, state))
		b.WriteString(fmt.Sprintf(`<div style="height:8px;background:#1c212b;border-radius:5px;overflow:hidden;margin:10px 0"><div style="height:100%%;width:%d%%;background:#33c179;transition:width .4s"></div></div>`, pct))
		b.WriteString(`<a class="btn" href="/lazy">看进度 / 暂停 / 停止</a>`)
	case len(lp.Dirs) > 0:
		b.WriteString(`<div style="display:flex;align-items:center;gap:18px;flex-wrap:wrap">`)
		b.WriteString(fmt.Sprintf(`<div><span class="big" style="font-size:26px;font-weight:700">%s</span> <span class="muted">可释放 · %d 个目录</span></div>`, human(lp.Bytes), len(lp.Dirs)))
		b.WriteString(`<a class="btn" style="background:#1f6f4a;border-color:#33c179;padding:10px 20px" href="/lazy">一键清理（可撤销）</a>`)
		b.WriteString(`<span class="muted">只清临时/开发缓存/更新缓存/日志，全部进隔离区</span>`)
		b.WriteString(`</div>`)
	case !ls.Finished.IsZero():
		last := "上次实际释放"
		if ls.Stopped {
			last = "上次手动停止前实际释放"
		}
		b.WriteString(fmt.Sprintf(`<div style="display:flex;align-items:center;gap:18px;flex-wrap:wrap"><div><span class="big" style="font-size:26px;font-weight:700">%s</span> <span class="muted">%s · 计划 %s</span></div>`,
			human(ls.Freed), last, human(ls.Planned)))
		b.WriteString(`<a class="btn" href="/lazy">查看 / 撤销</a>`)
		b.WriteString(`<span class="muted">隔离区里的项 7 天内可还原</span></div>`)
	default:
		b.WriteString(`<div style="display:flex;align-items:center;gap:18px;flex-wrap:wrap">`)
		b.WriteString(`<span class="muted">当前没有达到阈值的可清目录；已清理的项在隔离区里，7 天内可撤销。</span>`)
		b.WriteString(`<a class="btn" href="/lazy">打开懒人清理</a>`)
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div class="muted" style="margin-top:6px">` + htmlEscape(lazyScheduleText(cfg)) + `</div>`)
	b.WriteString(`</div>`)

	// 三个主动作
	b.WriteString(`<div class="tiles" style="grid-template-columns:repeat(3,minmax(0,1fr))">`)
	actions := []struct{ T, D, H string }{
		{"① 扫描一次", "拿到数据，约 1–2 分钟（只读）", "/scan"},
		{"② 懒人清理", "只清可清类，随时撤销", "/lazy"},
		{"③ 看趋势", "谁在涨、多久装满", "/trend"},
	}
	for _, a := range actions {
		b.WriteString(fmt.Sprintf(`<a class="tile" href="%s"><div class="t">%s</div><div class="d">%s</div></a>`, a.H, a.T, a.D))
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

func cleanTabOf(title string) string {
	switch {
	case strings.Contains(title, "重复"):
		return "dup"
	case strings.Contains(title, "媒体"):
		return "media"
	case strings.Contains(title, "残留"):
		return "leftovers"
	default:
		return "plan"
	}
}

func cleanTabs(active string) string {
	tabs := []struct{ Label, Href, Key string }{
		{"方案", "/clean", "plan"},
		{"重复", "/dup", "dup"},
		{"媒体", "/media", "media"},
		{"残留", "/leftovers", "leftovers"},
		{"隔离区", "/detail#quarantine", "quarantine"},
	}
	var b strings.Builder
	b.WriteString(`<div class="subnav">`)
	for _, x := range tabs {
		cls := ""
		if x.Key == active {
			cls = ` class="on"`
		}
		b.WriteString(`<a href="` + x.Href + `"` + cls + `>` + x.Label + `</a>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

const uiJS = `<script>
(function(){
  function toBytes(s){
    var m=/^([\d.]+)\s*(TB|GB|MB|KB|B)\s*$/i.exec(s.trim());
    if(!m) return -1;
    var v=parseFloat(m[1]); var u=m[2].toUpperCase();
    var mul={B:1,KB:1024,MB:1048576,GB:1073741824,TB:1099511627776}[u]||1;
    return v*mul;
  }
  function cmp(a,b){
    var ba=toBytes(a), bb=toBytes(b);
    if(ba>=0&&bb>=0) return ba-bb;
    var na=parseFloat(a.replace(/[^0-9.\-]/g,'')), nb=parseFloat(b.replace(/[^0-9.\-]/g,''));
    if(!isNaN(na)&&!isNaN(nb)&&/^[\d.,\s%]+$/.test(a+b)) return na-nb;
    return a.localeCompare(b,'zh');
  }
  var tables=document.querySelectorAll('table');
  for(var i=0;i<tables.length;i++){
    (function(t){
      var rows=Array.prototype.slice.call(t.rows);
      if(rows.length<6) return;
      var head=rows[0];
      if(!head||head.cells.length===0||head.cells[0].tagName!=='TH') return;
      var wrap=document.createElement('div'); wrap.className='tsearch';
      var inp=document.createElement('input'); inp.placeholder='搜索（路径 / 名称）'; wrap.appendChild(inp);
      t.parentNode.insertBefore(wrap,t);
      inp.addEventListener('input',function(){
        var q=inp.value.toLowerCase();
        for(var r=1;r<rows.length;r++){
          rows[r].style.display=rows[r].textContent.toLowerCase().indexOf(q)>=0?'':'none';
        }
      });
      var cells=head.cells;
      for(var c=0;c<cells.length;c++){
        (function(th,ci){
          if(!th||th.tagName!=='TH') return;
          th.style.cursor='pointer'; th.title='点击排序';
          var asc=true;
          th.addEventListener('click',function(){
            asc=!asc;
            var body=rows.slice(1).filter(function(r){return r.cells.length>ci});
            body.sort(function(x,y){
              var r=cmp(x.cells[ci].innerText||'', y.cells[ci].innerText||'');
              return asc?r:-r;
            });
            body.forEach(function(r){t.appendChild(r)});
          });
        })(cells[c],c);
      }
    })(tables[i]);
  }
})();

// ---- 全局动作条：扫描 / 清理进行中时，在任何页面都能看到 ----
function actPill(href, text, color, bg){
  return '<a href="'+href+'" style="text-decoration:none;font-size:12px;line-height:1;padding:6px 10px;border-radius:999px;border:1px solid '+color+'55;background:'+bg+';color:'+color+'">'+text+'</a>';
}
function actTick(){
  var el = document.getElementById('actbar');
  if(!el) return;
  fetch('/api/actions').then(function(r){return r.json()}).then(function(a){
    var h = '';
    if(a.scan && a.scan.running){
      h += actPill('/scan', '● 扫描中 ' + Math.round(a.scan.pct||0) + '%', '#8ff0b8', '#173327');
    }
    if(a.op && a.op.running){
      var ogb = ((a.op.bytes||0)/1073741824).toFixed(1);
      h += actPill('/op', '● 移动文件 ' + ogb + ' GB', '#9fd0ff', '#172433');
    }
    if(a.lazy && a.lazy.running){
      var gb = ((a.lazy.freed||0)/1073741824).toFixed(1);
      var pg = ((a.lazy.planned||0)/1073741824).toFixed(1);
      if(a.lazy.paused){
        h += actPill('/lazy', '⏸ 已暂停 ' + gb + ' / ' + pg + ' GB', '#ffd08a', '#332a17');
      } else if(a.lazy.stopping){
        h += actPill('/lazy', '■ 正在停止 ' + gb + ' GB', '#ffb3c0', '#331a20');
      } else {
        h += actPill('/lazy', '● 清理中 ' + gb + ' / ' + pg + ' GB', '#ffd08a', '#332a17');
      }
    } else if(a.lazy && a.lazy.finished && (Date.now()/1000 - a.lazy.finished) < 120 && a.lazy.freed > 0){
      var gb = (a.lazy.freed/1073741824).toFixed(1);
      var doneTxt = a.lazy.stopped ? '■ 已停止 ' : '✓ 清理完成 ';
      h += actPill('/lazy', doneTxt + gb + ' GB', '#8ff0b8', '#173327');
    }
    el.innerHTML = h;
  }).catch(function(){});
}
actTick(); setInterval(actTick, 1500);
</script>`

func themeCSS() string {
	if currentTheme() == "light" {
		return lightCSS
	}
	return ""
}


// loadOverlay：点击链接/提交表单后，浏览器等待服务器响应期间的反馈
const loadOverlay = `<div id="dshload" aria-hidden="true"><div class="lbar"><i></i></div><div class="ltext">正在处理…</div></div>
<style>
#dshload{position:fixed;inset:0;display:none;z-index:9999;pointer-events:none}
#dshload.on{display:block}
#dshload .lbar{position:absolute;top:0;left:0;right:0;height:3px;background:#1c212b;overflow:hidden}
#dshload .lbar i{display:block;height:100%;width:30%;background:linear-gradient(90deg,transparent,#33c179,#7df0b3);animation:dshbar 1.1s ease-in-out infinite}
#dshload .ltext{position:absolute;left:50%;top:52%;transform:translate(-50%,-50%);background:#151a22ee;border:1px solid #2a3240;border-radius:10px;padding:10px 16px;color:#c7d3e3;font-size:13px;display:none;box-shadow:0 10px 30px #0008}
#dshload.slow .ltext{display:block}
@keyframes dshbar{0%{margin-left:-30%}100%{margin-left:100%}}
</style>
<script>
(function(){
  var el = document.getElementById('dshload');
  if(!el) return;
  var t1=null,t2=null;
  function show(msg){
    el.classList.add('on');
    el.classList.remove('slow');
    t1 = setTimeout(function(){ el.classList.add('slow'); }, 600);
    var tx = el.querySelector('.ltext');
    if(msg) tx.textContent = msg;
  }
  function hide(){ clearTimeout(t1); el.classList.remove('on','slow'); }
  document.addEventListener('click', function(e){
    var a = e.target.closest ? e.target.closest('a') : null;
    if(!a) return;
    if(a.target === '_blank' || a.hasAttribute('download')) return;
    var href = a.getAttribute('href') || '';
    if(!href || href.charAt(0) === '#' || href.indexOf('javascript:') === 0) return;
    if(href.indexOf('/reveal') === 0) return;
    show(a.getAttribute('data-loading') || '正在打开…');
  }, true);
  document.addEventListener('submit', function(){
    show(document.body.getAttribute('data-submit') || '正在处理…');
  }, true);
  window.addEventListener('pageshow', hide);
  window.addEventListener('load', hide);
})();
</script>`
