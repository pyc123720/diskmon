package main

import (
	"fmt"
	"html"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

const css = `
*{box-sizing:border-box}
body{margin:0;background:#0e1116;color:#e7ebf2;font:14px/1.55 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
.wrap{max-width:1180px;margin:0 auto;padding:22px 20px 60px}
header{display:flex;align-items:center;gap:14px;flex-wrap:wrap;margin-bottom:18px}
h1{font-size:19px;margin:0;font-weight:600}
.pill{display:inline-flex;align-items:center;gap:6px;padding:4px 10px;border-radius:999px;font-size:12px;font-weight:600}
.dot{width:7px;height:7px;border-radius:50%}
.sub{color:#8b94a7;font-size:12px;margin-left:auto}
.grid{display:grid;gap:14px}
.g2{grid-template-columns:1.05fr 1.55fr}
.g3{grid-template-columns:repeat(3,1fr)}
@media(max-width:900px){.g2,.g3{grid-template-columns:1fr}}
.card{background:#161a22;border:1px solid #262c38;border-radius:14px;padding:16px}
.card h2{font-size:13px;margin:0 0 12px;color:#b9c2d3;font-weight:600;letter-spacing:.3px}
.kpi{display:flex;gap:18px;flex-wrap:wrap;align-items:baseline}
.kpi .big{font-size:26px;font-weight:700}
.kpi .unit{font-size:13px;color:#8b94a7}
.muted{color:#8b94a7;font-size:12px}
.brow{display:grid;grid-template-columns:1.45fr 1.85fr 92px;gap:10px;align-items:center;padding:5px 0;border-bottom:1px dashed #1e2430}
.blabel{font-size:12.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.btrack{background:#1c212b;border-radius:6px;height:10px;overflow:hidden}
.bfill{height:100%;border-radius:6px}
.bval{text-align:right;font-size:12px;font-family:Consolas,monospace}
.bpct{color:#8b94a7;margin-left:6px;font-size:11px}
.tag{font-size:10px;padding:1px 6px;border-radius:5px;margin-left:8px;background:#232a36;color:#9aa6bb}
table{width:100%;border-collapse:collapse;font-size:12.5px}
th,td{text-align:left;padding:7px 8px;border-bottom:1px solid #1e2430}
th{color:#8b94a7;font-weight:500;font-size:11.5px}
td.r{text-align:right;font-family:Consolas,monospace}
.path{font-family:Consolas,monospace;font-size:11.5px;color:#9fb0c9;word-break:break-all}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(330px,1fr));gap:12px}
.rc{background:#1a1f29;border:1px solid #262c38;border-left:3px solid #4c8dff;border-radius:11px;padding:12px 13px}
.rc .t{display:flex;justify-content:space-between;gap:8px;margin-bottom:6px}
.rc .n{font-weight:600;font-size:13.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.rc .sz{font-family:Consolas,monospace;font-size:12.5px;white-space:nowrap}
.rc .row{margin-top:5px;font-size:12px;color:#9aa6bb}
.rc .row b{color:#cfd8e8;font-weight:500}
.rc .risk{margin-top:7px;font-size:12px;color:#c7d0e0}
.empty{color:#6f7889;font-size:12.5px;padding:18px 0;text-align:center}
.alert{display:flex;gap:10px;font-size:12.5px;padding:7px 0;border-bottom:1px dashed #1e2430}
.alert .when{color:#6f7889;font-family:Consolas,monospace;white-space:nowrap}
ul.lim{margin:0;padding-left:18px;color:#8b94a7;font-size:12px;line-height:1.7}
.stack{display:flex;height:14px;border-radius:7px;overflow:hidden;background:#1c212b;margin:10px 0 6px}
.legend{display:flex;gap:14px;flex-wrap:wrap;font-size:11.5px;color:#9aa6bb;margin-top:8px}
.legend i{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:5px}
.actions{display:inline-flex;gap:8px;margin-left:12px}
.actions a{display:inline-block;padding:5px 11px;border-radius:8px;background:#1c212b;color:#c9d4e6;text-decoration:none;font-size:12px;border:1px solid #2a3240}
.act{display:inline-block;padding:3px 10px;border-radius:7px;background:#242b38;color:#cfd8e8;text-decoration:none;font-size:11.5px;border:1px solid #333c4c}
.act:hover{background:#2c3545}
.act.danger{color:#ff9db0;border-color:#6b2b3a}
footer{margin-top:26px;color:#5f6879;font-size:11.5px}
`

var tierColor = map[string]string{
	"clean": "#33c179", "confirm": "#f5a524", "adjust": "#8b5cf6", "keep": "#5b6474",
}
var tierText = map[string]string{
	"clean": "可清", "confirm": "需确认", "adjust": "可调整", "keep": "勿动",
}
var levelColor = map[string]string{
	"ok": "#33c179", "warn": "#f5a524", "danger": "#f5486a", "critical": "#ff2d55",
}

func fmtDur(d float64, ok bool) string {
	if !ok || math.IsInf(d, 1) || d <= 0 {
		return ">10 年"
	}
	if d >= 3650 {
		return ">10 年"
	}
	if d >= 365 {
		return fmt.Sprintf("%.1f 年", d/365)
	}
	return fmt.Sprintf("%.0f 天", d)
}

func renderPage(cfg Config, st *Store) string {
	v, _ := volumeStats(cfg.Root)
	level := levelOf(v, cfg)
	color := levelColor[level]
	sc := st.GetScan()
	var b strings.Builder

	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>diskmon · 磁盘监测</title><link rel=\"icon\" href=\"/favicon.ico\"><style>" + css + navCSS + themeCSS() + "</style></head><body><div class=\"wrap\">")
	b.WriteString(navBar("detail"))
	b.WriteString(dataDirBanner())
	b.WriteString(scanMismatchBanner(cfg, st))
	b.WriteString(muteBanner())
	b.WriteString(subNav([][2]string{
		{"容量", "#overview"}, {"预测", "#forecast"}, {"每日", "#daily"}, {"性质", "#anomaly"},
		{"分类", "#cats"}, {"Treemap", "#treemap"}, {"对比", "#diff"},
		{"风险卡", "#risks"}, {"隔离区", "#quarantine"},
		{"方案", "#plans"}, {"规则", "#rules"}, {"应用", "#apps"}, {"进程", "#procs"},
		{"重复", "#dup"}, {"残留", "#leftovers"}, {"目录", "#dirs"}, {"大文件", "#files"},
		{"告警", "#alerts"}, {"工具", "#tools"},
	}))
	b.WriteString("<header><h1>diskmon <span class=\"muted\" style=\"font-weight:400\">Go 单文件版</span></h1>")
	b.WriteString(fmt.Sprintf("<span class=\"pill\" style=\"background:%s22;color:%s;border:1px solid %s55\"><span class=\"dot\" style=\"background:%s\"></span>%s</span>",
		color, color, color, color, levelText(level)))
	b.WriteString("<span class=\"sub\">生成于 " + time.Now().Format("2006-01-02 15:04:05") + "</span>")
	b.WriteString("<span class=\"actions\"><a href=\"/scan\">重新扫描</a><a href=\"/\">刷新</a><a href=\"/api/status\" target=\"_blank\">API</a></span></header>")

	// 容量环 + 预测
	freeGB := gb(v.Free)
	usedGB := gb(v.Used)
	totalGB := gb(v.Total)
	pct := 0.0
	if v.Total > 0 {
		pct = float64(v.Used) / float64(v.Total) * 100
	}
	ring := svgRing(pct, color, usedGB, totalGB, freeGB)
	b.WriteString("<div class=\"grid g2\"><div class=\"card\" id=\"overview\"><h2>容量</h2>")
	b.WriteString("<div style=\"display:flex;justify-content:center\">" + ring + "</div>")
	b.WriteString(fmt.Sprintf("<div class=\"kpi\" style=\"justify-content:center\"><div><span class=\"big\">%.2f</span><span class=\"unit\"> GB 剩余</span></div><div class=\"muted\">阈值 %.0f/%.0f/%.0f GB</div></div>",
		freeGB, cfg.WarnGB, cfg.DangerGB, cfg.CriticalGB))
	b.WriteString(volumesHTML(cfg.Root))
	b.WriteString("</div>")

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

	b.WriteString("<div class=\"card\" id=\"forecast\"><h2>写满预测</h2>")
	if f.OK {
		b.WriteString(fmt.Sprintf("<div class=\"kpi\"><div><span class=\"big\">%s</span><span class=\"unit\"> 后写满（中位）</span></div><div class=\"muted\">区间 %s ～ %s　·　等效增速 %.0f MB/天</div></div>",
			fmtDur(f.DaysP50, true), fmtDur(f.DaysFast, true), fmtDur(f.DaysSlow, true), f.SlopeMB))
		b.WriteString("<div class=\"muted\">数据来源：" + html.EscapeString(source) + "　·　样本 " + fmt.Sprint(f.SampleDay) + " 天</div>")
		if strings.Contains(source, "mtime") {
			b.WriteString("<div class=\"muted\" style=\"color:#c9a24a\">⚠ 按写入量估算：实际净增长通常更慢，累积 3 天采样后自动切换</div>")
		}
		b.WriteString(svgProjection(f, freeGB, cfg))
	} else {
		b.WriteString("<div class=\"empty\">暂无增长数据（先扫描一次）</div>")
	}
	b.WriteString("</div></div>")

	// 每日新增 + 异常
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"daily\"><h2>每日新增（近 " + fmt.Sprint(len(deltas)) + " 天）</h2>")
	b.WriteString(svgBars(deltas))
	b.WriteString("<div class=\"muted\">红色柱为检测到的异常跳变日。</div></div><div class=\"card\" id=\"anomaly\"><h2>增长性质判定</h2>")
	anoms := anomalies(deltas)
	if len(anoms) == 0 {
		b.WriteString("<div class=\"empty\">未检测到异常增长</div>")
	} else {
		for _, a := range anoms {
			tag := "⚠ 跳变"
			if a.Kind == "leak" {
				tag = "🔥 泄漏"
			}
			b.WriteString("<div class=\"alert\"><span class=\"when\">" + a.Day + "</span><span>" + tag + "　" + html.EscapeString(a.Text) + "</span></div>")
		}
	}
	b.WriteString("<div class=\"muted\" style=\"margin-top:8px\">一次性跳变=安装/下载（不必处理）；持续泄漏=缓存/日志在积累（值得处理）。</div></div></div>")

	// 分类 + 可删占比
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"cats\"><h2>分类占用</h2>")
	total := uint64(0)
	if sc != nil {
		total = sc.Total
	}
	if sc != nil {
		type kv struct {
			cat string
			b   uint64
		}
		var arr []kv
		for c, n := range sc.Cats {
			arr = append(arr, kv{c, n})
		}
		order := map[string]int{"clean": 0, "confirm": 1, "adjust": 2, "keep": 3}
		sort.Slice(arr, func(i, j int) bool {
			oi, oj := order[cats[arr[i].cat].Tier], order[cats[arr[j].cat].Tier]
			if oi != oj {
				return oi < oj
			}
			return arr[i].b > arr[j].b
		})
		for _, e := range arr {
			meta := cats[e.cat]
			b.WriteString(barRow(meta.Label, e.b, total, tierColor[meta.Tier], tierText[meta.Tier], meta.Hint))
		}
	} else {
		b.WriteString("<div class=\"empty\">尚未扫描</div>")
	}
	b.WriteString("</div><div class=\"card\"><h2>可删占比（除系统必要文件外）</h2>")
	var cleanB, confirmB, adjustB, keepB uint64
	if sc != nil {
		for c, n := range sc.Cats {
			switch cats[c].Tier {
			case "clean":
				cleanB += n
			case "confirm":
				confirmB += n
			case "adjust":
				adjustB += n
			default:
				keepB += n
			}
		}
	}
	reclaim := cleanB + confirmB
	den := float64(v.Used)
	if den == 0 {
		den = 1
	}
	b.WriteString("<div class=\"stack\">")
	b.WriteString(fmt.Sprintf("<div style=\"width:%.2f%%;background:#33c179\"></div>", float64(cleanB)/den*100))
	b.WriteString(fmt.Sprintf("<div style=\"width:%.2f%%;background:#f5a524\"></div>", float64(confirmB)/den*100))
	b.WriteString(fmt.Sprintf("<div style=\"width:%.2f%%;background:#8b5cf6\"></div>", float64(adjustB)/den*100))
	b.WriteString("</div>")
	b.WriteString(fmt.Sprintf("<div class=\"legend\"><span><i style=\"background:#33c179\"></i>可清 %s（%.1f%%）</span><span><i style=\"background:#f5a524\"></i>需确认 %s（%.1f%%）</span><span><i style=\"background:#8b5cf6\"></i>可调整 %s（%.1f%%）</span></div>",
		human(cleanB), float64(cleanB)/den*100, human(confirmB), float64(confirmB)/den*100, human(adjustB), float64(adjustB)/den*100))
	b.WriteString(fmt.Sprintf("<div class=\"kpi\" style=\"margin-top:14px\"><div><span class=\"big\">%s</span><span class=\"unit\"> 可回收（%.1f%%）</span></div></div>",
		human(reclaim), float64(reclaim)/den*100))
	if sc != nil {
		b.WriteString(fmt.Sprintf("<div class=\"muted\" style=\"margin-top:6px\">扫描于 %s，%d 文件 / %d 目录，%s，耗时 %.0fs，跳过 %d 处</div>",
			time.Unix(sc.TS, 0).Format("2006-01-02 15:04"), sc.Files, sc.Dirs, human(sc.Total), sc.Duration, sc.Errors))
	}
	b.WriteString("</div></div>")

	// 风险卡
	// Treemap + 快照对比
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"treemap\"><h2>空间分布（Treemap）</h2>")
	b.WriteString(treemapSVG(sc))
	b.WriteString("<div class=\"muted\">面积 = 目录大小；颜色按处置等级：绿=可清、黄=需确认。悬停看路径。</div></div>")
	b.WriteString("<div class=\"card\" id=\"diff\"><h2>对比上次 / 一周前</h2>")
	b.WriteString(diffHTML(cfg))
	b.WriteString("</div></div>")

	b.WriteString("<div class=\"card\" id=\"risks\" style=\"margin-top:14px\"><h2>风险提示卡（可回收项 · 删前先看后果）</h2><div class=\"cards\">")
	cards := 0
	if sc != nil {
		wl := make([]string, len(cfg.Whitelist))
		for i, w := range cfg.Whitelist {
			wl[i] = strings.ToLower(w)
		}
		for _, d := range sc.Deep {
			meta, ok := cats[d.Cat]
			if !ok || (meta.Tier != "clean" && meta.Tier != "confirm") || d.Bytes < 200<<20 {
				continue
			}
			low := strings.ToLower(d.Path + `\`)
			skip := false
			for _, w := range wl {
				if strings.Contains(low, w) {
					skip = true
					break
				}
			}
			if skip || deniedForCleanup(d.Path) {
				continue
			}
			short := d.Path
			if i := strings.LastIndex(short, `\`); i > 0 {
				if j := strings.LastIndex(short[:i], `\`); j > 0 {
					short = short[j+1:]
				}
			}
			note := "可再生内容，清理后会自动重建；不会丢数据。"
			if meta.Tier == "confirm" {
				note = "可能包含账号数据或离线内容，清理前建议先确认该软件内的设置。"
			}
			b.WriteString(fmt.Sprintf("<div class=\"rc\" style=\"border-left-color:%s\"><div class=\"t\"><div class=\"n\" title=\"%s\">%s</div><div class=\"sz\">%s</div></div><div class=\"path\">%s</div><div class=\"row\"><b>类别</b> %s　<b>处置</b> %s</div><div class=\"risk\">%s<br><span class=\"muted\">依据：%s</span></div><div class=\"row\" style=\"margin-top:8px\"><a class=\"act danger\" href=\"/q/preview?path=%s\">演练 / 隔离</a></div></div>",
				tierColor[meta.Tier], html.EscapeString(d.Path), html.EscapeString(short), human(d.Bytes),
				html.EscapeString(d.Path), meta.Label, tierText[meta.Tier], note, html.EscapeString(meta.Hint),
				url.QueryEscape(d.Path)))
			cards++
			if cards >= 8 {
				break
			}
		}
	}
	if cards == 0 {
		b.WriteString("<div class=\"empty\">没有可回收的候选目录</div>")
	}
	b.WriteString("</div></div>")

	// 隔离区
	b.WriteString("<div class=\"card\" id=\"quarantine\" style=\"margin-top:14px\"><h2>隔离区（可还原，不立即删除）</h2>")
	q := openQuarantine(cfg)
	var active []QEntry
	var qbytes uint64
	for _, e := range q.entries() {
		if !e.Restored {
			active = append(active, e)
			qbytes += e.Bytes
		}
	}
	if len(active) == 0 {
		b.WriteString("<div class=\"empty\">隔离区为空。风险卡里的「演练 / 隔离」会先把内容移到隔离区，而不是直接删。</div>")
	} else {
		b.WriteString(fmt.Sprintf("<div class=\"muted\">共 %d 项 · %s / 上限 %d GB · 保留 %d 天，到期才真删</div>",
			len(active), human(qbytes), cfg.QuarantineMaxGB, cfg.QuarantineDays))
		b.WriteString("<table><tr><th>时间</th><th>原路径</th><th style=\"text-align:right\">大小</th><th></th></tr>")
		for i := len(active) - 1; i >= 0 && i >= len(active)-10; i-- {
			e := active[i]
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td class=\"path\">%s</td><td class=\"r\">%s</td><td><a class=\"act\" href=\"/q/restore?id=%s\">还原</a></td></tr>",
				time.Unix(e.TS, 0).Format("01-02 15:04"), html.EscapeString(e.Original), human(e.Bytes),
				url.QueryEscape(e.ID)))
		}
		b.WriteString("</table>")
		b.WriteString(fmt.Sprintf("<div style=\"margin-top:8px\"><a class=\"act danger\" href=\"/q/purge\">清理超期项（%d 天）</a></div>", cfg.QuarantineDays))
	}
	b.WriteString("</div>")

	// 一键清理方案
	b.WriteString("<div class=\"card\" id=\"plans\" style=\"margin-top:14px\"><h2>一键清理方案</h2>")
	b.WriteString("<div class=\"muted\" style=\"margin-bottom:10px\">三档都由你确认后执行；全部进隔离区，7 天内可还原。</div>")
	b.WriteString(planCardHTML(cfg, st))
	b.WriteString("</div>")

	// 自动清理规则（L2）
	b.WriteString("<div class=\"card\" id=\"rules\" style=\"margin-top:14px\"><h2>自动清理规则（L2）</h2>")
	rc := getConfig()
	if len(rc.Rules) == 0 {
		b.WriteString("<div class=\"muted\">还没有规则。L2 在目录超过阈值时自动清理旧文件，只允许「可清」类目录（临时文件 / 开发缓存 / 更新缓存 / 日志）。</div>")
	} else {
		stats := st.allRuleStats()
		b.WriteString("<table><tr><th>规则</th><th>状态</th><th style=\"text-align:right\">上次释放</th><th style=\"text-align:right\">累计</th></tr>")
		for _, r := range rc.Rules {
			s := stats[r.Name]
			state := "停用"
			if r.Enabled {
				state = "已启用"
				if r.DryRun {
					state = "试运行"
				}
			}
			freed := human(s.LastFreed)
			if r.DryRun && s.WouldFree > 0 {
				freed = "≈ " + human(s.WouldFree)
			}
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s<br><span class=\"muted\">%s</span></td><td>%s</td><td class=\"r\">%s</td><td class=\"r\">%s</td></tr>",
				html.EscapeString(r.Name), html.EscapeString(r.Path), state, freed, human(s.TotalFreed)))
		}
		b.WriteString("</table>")
	}
	b.WriteString("<div style=\"margin-top:8px\"><a class=\"act\" href=\"/rules\">管理规则</a></div></div>")

	// 按应用聚合的清理建议
	b.WriteString("<div class=\"card\" id=\"apps\" style=\"margin-top:14px\"><h2>按应用聚合的清理建议</h2>")
	cfgNow := getConfig()
	groups := appGroups(cfgNow, st)
	if len(groups) == 0 {
		b.WriteString("<div class=\"muted\">还没有聚合数据（先扫描一次）。</div>")
	} else {
		b.WriteString("<table><tr><th>应用</th><th style=\"text-align:right\">目录</th><th style=\"text-align:right\">可回收</th><th></th></tr>")
		for _, g := range groups {
			extra := ""
			if g.Refused > 0 {
				extra = fmt.Sprintf("<span class=\"muted\">（%d 个受保护跳过）</span>", g.Refused)
			}
			b.WriteString(fmt.Sprintf("<tr><td>%s %s</td><td class=\"r\">%d</td><td class=\"r\">%s</td><td><a class=\"act\" href=\"/app?name=%s\">查看 / 整组隔离</a></td></tr>",
				html.EscapeString(g.Name), extra, len(g.Dirs), human(g.Bytes), url.QueryEscape(g.Name)))
		}
		b.WriteString("</table>")
	}
	b.WriteString("</div>")

	// 写入热点进程
	b.WriteString("<div class=\"card\" id=\"procs\" style=\"margin-top:14px\"><h2>写入热点进程（最近 5 分钟）</h2>")
	ranks := procRanks(5*time.Minute, 8)
	if len(ranks) == 0 {
		b.WriteString("<div class=\"muted\">暂无数据（需要托盘运行一段时间）</div>")
	} else {
		totals := procTotalSum()
		b.WriteString("<table><tr><th>进程</th><th style=\"text-align:right\">最近 5 分钟</th><th style=\"text-align:right\">累计</th></tr>")
		for _, r := range ranks {
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td class=\"r\">%s</td><td class=\"r muted\">%s</td></tr>",
				html.EscapeString(r.Name), human(r.Bytes), human(totals[r.Name])))
		}
		b.WriteString("</table>")
	}
	b.WriteString("<div class=\"muted\">按进程 I/O 写入字节数排序（近似归因）。路径级归因看上面的 USN 告警。</div></div>")

	// 清理记录
	b.WriteString("<div class=\"card\" id=\"history\" style=\"margin-top:14px\"><h2>清理记录（最近）</h2>")
	hist := cleanHistory(st, 8)
	if len(hist) == 0 {
		b.WriteString("<div class=\"muted\">还没有清理记录。执行隔离、一键方案或 L2 规则后，这里会留下明细。</div>")
	} else {
		b.WriteString("<table><tr><th>时间</th><th>事件</th></tr>")
		for _, h := range hist {
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td>%s</td></tr>",
				time.Unix(h.TS, 0).Format("01-02 15:04"), html.EscapeString(h.Text)))
		}
		b.WriteString("</table>")
	}
	b.WriteString("</div>")

	// 重复文件 + 卸载残留
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"dup\"><h2>重复文件</h2>")
	b.WriteString(dupCardHTML(cfg))
	b.WriteString("</div><div class=\"card\" id=\"leftovers\"><h2>卸载残留</h2>")
	b.WriteString(leftoversCardHTML(cfg, st))
	b.WriteString("</div></div>")

	// 目录 / 文件
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"dirs\"><h2>目录排行（深度 3）</h2><table><thead><tr><th>路径</th><th>类别</th><th style=\"text-align:right\">大小</th></tr></thead><tbody>")
	if sc != nil {
		for _, d := range sc.Dirs3 {
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td>%s</td><td class=\"r\">%s</td></tr>",
				html.EscapeString(d.Path), cats[d.Cat].Label, human(d.Bytes)))
		}
	}
	b.WriteString("</tbody></table></div><div class=\"card\" id=\"files\"><h2>Top 大文件</h2><table><thead><tr><th>路径</th><th style=\"text-align:right\">大小</th><th style=\"text-align:right\">改动</th></tr></thead><tbody>")
	if sc != nil {
		for _, f := range sc.TopFiles {
			if f.Bytes < 200<<20 {
				continue
			}
			b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td class=\"r\">%s</td><td class=\"r\">%s</td></tr>",
				html.EscapeString(f.Path), human(f.Bytes), time.Unix(f.MTime, 0).Format("01-02")))
		}
	}
	b.WriteString("</tbody></table></div></div>")

	// 自占用 + 告警
	eng := st.Engine()
	engLabel := "热区重扫"
	if eng.Engine == "usn" {
		engLabel = "USN 秒级"
	} else if eng.Engine == "paused" {
		engLabel = "已暂停"
	}
	state := "未运行"
	if eng.Running {
		state = "运行中"
	}
	b.WriteString("<div class=\"grid g2\" style=\"margin-top:14px\"><div class=\"card\" id=\"engine\"><h2>引擎 / 自占用</h2>")
	b.WriteString(fmt.Sprintf("<div class=\"muted\">引擎 %s（%s）　·　进程内存 %.0f MB</div>", engLabel, state, memMB()))
	b.WriteString(fmt.Sprintf("<div class=\"muted\">采样 %d 条　·　数据 %s</div>", len(st.st.Samples), cfg.DataDir))
	b.WriteString("</div><div class=\"card\" id=\"alerts\"><h2>最近告警</h2>")
	alerts := st.Alerts()
	if len(alerts) == 0 {
		b.WriteString("<div class=\"empty\">暂无告警</div>")
	} else {
		for i := len(alerts) - 1; i >= 0 && i >= len(alerts)-8; i-- {
			a := alerts[i]
			b.WriteString("<div class=\"alert\"><span class=\"when\">" + time.Unix(a.TS, 0).Format("01-02 15:04") + "</span><span>" + html.EscapeString(a.Text) + "</span></div>")
		}
	}
	b.WriteString("</div></div>")

	b.WriteString("<div class=\"card\" id=\"tools\" style=\"margin-top:14px\"><h2>系统工具</h2>")
	b.WriteString(toolsHTML())
	b.WriteString("</div>")

	b.WriteString("<div class=\"card\" style=\"margin-top:14px\"><h2>已知限制</h2><ul class=\"lim\">")
	b.WriteString("<li>只读：本工具不会删除、改名或写入被监测卷上的任何文件。</li>")
	b.WriteString("<li>目录统计为文件大小之和，不含 NTFS 簇对齐开销，与资源管理器可能差 1-3%。</li>")
	b.WriteString("<li>USN 秒级归因需要管理员权限；普通权限下退回热区重扫（10-40 秒延迟）。</li>")
	b.WriteString("<li>mtime 回溯反映「仍留在盘上的新增内容」，真实净增长需累积几天采样。</li>")
	b.WriteString("</ul></div>")
	b.WriteString("<footer>diskmon-go · 单文件版 · 只读监测</footer></div>`+uiJS+`</body></html>")
	return translateHTML(b.String())
}

func svgRing(pct float64, color string, usedGB, totalGB, freeGB float64) string {
	const r, w = 78.0, 16.0
	circ := 2 * math.Pi * r
	dash := circ * math.Min(math.Max(pct, 0), 100) / 100
	return fmt.Sprintf(`<svg viewBox="0 0 200 200" width="200" height="200">
<circle cx="100" cy="100" r="78" fill="none" stroke="#262c38" stroke-width="16"/>
<circle cx="100" cy="100" r="78" fill="none" stroke="%s" stroke-width="16" stroke-linecap="round" stroke-dasharray="%.1f %.1f" transform="rotate(-90 100 100)"/>
<text x="100" y="98" text-anchor="middle" fill="#e7ebf2" font-size="30" font-weight="600">%.1f%%</text>
<text x="100" y="120" text-anchor="middle" fill="#8b94a7" font-size="12">已用 %.1f / %.1f GB</text>
<text x="100" y="140" text-anchor="middle" fill="%s" font-size="12">剩余 %.2f GB</text>
</svg>`, color, dash, circ, pct, usedGB, totalGB, color, freeGB)
}

func svgBars(deltas []DayDelta) string {
	if len(deltas) == 0 {
		return "<div class=\"empty\">暂无数据</div>"
	}
	w, h, padL, padB := 640.0, 170.0, 46.0, 26.0
	maxv := uint64(1)
	for _, d := range deltas {
		if d.Bytes > maxv {
			maxv = d.Bytes
		}
	}
	n := len(deltas)
	gap := 2.0
	bw := (w - padL - 8 - gap*float64(n)) / float64(n)
	if bw < 1.5 {
		bw = 1.5
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" height="%.0f">`, w, h, h))
	for frac := 0.0; frac <= 1.0; frac += 0.5 {
		y := h - padB - frac*(h-padB-12)
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#262c38"/>`, padL, y, w-8, y))
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="end" fill="#8b94a7" font-size="10">%.1fG</text>`, padL-6, y+4, float64(maxv)*frac/(1<<30)))
	}
	for i, d := range deltas {
		bh := float64(d.Bytes) / float64(maxv) * (h - padB - 12)
		x := padL + float64(i)*(bw+gap)
		y := h - padB - bh
		col := "#4c8dff"
		if d.Bytes > maxv/2 {
			col = "#f5486a"
		}
		b.WriteString(fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" rx="1.5"><title>%s %s</title></rect>`,
			x, y, bw, math.Max(bh, 0.5), col, d.Day, human(d.Bytes)))
	}
	for i, d := range deltas {
		if n > 10 && i%3 != 0 && i != n-1 {
			continue
		}
		x := padL + float64(i)*(bw+gap) + bw/2
		label := d.Day
		if len(label) > 5 {
			label = label[5:]
		}
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.0f" text-anchor="middle" fill="#8b94a7" font-size="10">%s</text>`, x, h-8, label))
	}
	b.WriteString("</svg>")
	return b.String()
}

func svgProjection(f Forecast, freeGB float64, cfg Config) string {
	const w, h, padL, padR, padT, padB = 640.0, 190.0, 46.0, 12.0, 12.0, 26.0
	p50, fast, slow := projection(uint64(freeGB*(1<<30)), f, 45)
	ymax := math.Max(freeGB*1.06, 1)
	X := func(i float64) float64 { return padL + i/45*(w-padL-padR) }
	Y := func(v float64) float64 { return padT + (1-v/ymax)*(h-padT-padB) }
	path := func(vals []float64) string {
		var sb strings.Builder
		for i, v := range vals {
			if i == 0 {
				sb.WriteString(fmt.Sprintf("M%.1f,%.1f", X(float64(i)), Y(v)))
			} else {
				sb.WriteString(fmt.Sprintf(" L%.1f,%.1f", X(float64(i)), Y(v)))
			}
		}
		return sb.String()
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" height="%.0f">`, w, h, h))
	for _, gv := range []float64{ymax, ymax / 2, 0} {
		y := Y(gv)
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#262c38"/>`, padL, y, w-padR, y))
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="end" fill="#8b94a7" font-size="10">%.0fG</text>`, padL-6, y+4, gv))
	}
	for _, t := range []struct {
		gb  float64
		col string
	}{{cfg.WarnGB, "#f5a524"}, {cfg.DangerGB, "#f5486a"}, {cfg.CriticalGB, "#ff2d55"}} {
		if t.gb <= ymax {
			y := Y(t.gb)
			b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-dasharray="4 4" opacity="0.55"/>`, padL, y, w-padR, y, t.col))
		}
	}
	b.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="#f5486a" stroke-width="1.4" stroke-dasharray="5 4" opacity="0.75"/>`, path(fast)))
	b.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="#33c179" stroke-width="1.4" stroke-dasharray="5 4" opacity="0.75"/>`, path(slow)))
	b.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="#4c8dff" stroke-width="2.4"/>`, path(p50)))
	for i := 0; i <= 45; i += 10 {
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.0f" text-anchor="middle" fill="#8b94a7" font-size="10">+%dd</text>`, X(float64(i)), h-8, i))
	}
	b.WriteString("</svg>")
	return b.String()
}

func barRow(label string, bytes, total uint64, color, tag, tip string) string {
	pct := 0.0
	if total > 0 {
		pct = float64(bytes) / float64(total) * 100
	}
	return fmt.Sprintf(`<div class="brow" title="%s"><div class="blabel">%s<span class="tag">%s</span></div><div class="btrack"><div class="bfill" style="width:%.2f%%;background:%s"></div></div><div class="bval">%s<span class="bpct">%.1f%%</span></div></div>`,
		html.EscapeString(tip), html.EscapeString(label), tag, pct, color, human(bytes), pct)
}