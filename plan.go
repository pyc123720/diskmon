package main

import (
	"fmt"
	"os"
	"net/url"
	"strings"
)

// ---------- 一键清理方案 ----------

type Plan struct {
	Key     string
	Label   string
	Desc    string
	Dirs    []DirEntry
	Bytes   uint64
	MinSize uint64
	ScanTS  int64 // 计划所依据的扫描快照时间；0 表示还没扫描
}

var planDefs = []struct {
	Key     string
	Label   string
	Desc    string
	Tiers   []string
	Cats    []string // confirm 档里允许的类别；空=全部 confirm 类别
	MinMB   uint64
}{
	{
		Key: "conservative", Label: "保守", MinMB: 100,
		Desc:  "只清「可清」类：临时文件、开发缓存、更新缓存、日志。最安全，随时可还原。",
		Tiers: []string{"clean"},
	},
	{
		Key: "standard", Label: "标准", MinMB: 200,
		Desc:  "可清项 + 程序缓存/浏览器缓存（≥200MB）。不含聊天媒体、游戏库、模型。",
		Tiers: []string{"clean", "confirm"},
		Cats:  []string{"program_data", "browser"},
	},
	{
		Key: "aggressive", Label: "激进", MinMB: 50,
		Desc:  "所有可回收项（含聊天媒体、游戏、模型缓存，≥50MB）。风险最高，务必逐项看过再执行。",
		Tiers: []string{"clean", "confirm"},
	},
}

func planByKey(key string) (int, bool) {
	for i, d := range planDefs {
		if d.Key == key {
			return i, true
		}
	}
	return 0, false
}

func buildPlan(cfg Config, st *Store, key string) Plan {
	idx, ok := planByKey(key)
	if !ok {
		return Plan{Key: key, Label: "未知方案"}
	}
	def := planDefs[idx]
	p := Plan{Key: def.Key, Label: def.Label, Desc: def.Desc, MinSize: def.MinMB << 20}
	sc := st.GetScan()
	if sc == nil {
		return p
	}
	p.ScanTS = sc.TS
	wl := make([]string, len(cfg.Whitelist))
	for i, w := range cfg.Whitelist {
		wl[i] = strings.ToLower(w)
	}
	tierOK := map[string]bool{}
	for _, t := range def.Tiers {
		tierOK[t] = true
	}
	catOK := map[string]bool{}
	for _, c := range def.Cats {
		catOK[c] = true
	}
	for _, d := range sc.Deep {
		meta, ok := cats[d.Cat]
		if !ok || !tierOK[meta.Tier] || d.Bytes < p.MinSize {
			continue
		}
		if meta.Tier == "confirm" && len(catOK) > 0 && !catOK[d.Cat] {
			continue
		}
		low := strings.ToLower(d.Path + `\`)
		blocked := false
		for _, w := range wl {
			if strings.Contains(low, w) {
				blocked = true
				break
			}
		}
		if blocked || deniedForCleanup(d.Path) {
			continue
		}
		if _, err := os.Stat(d.Path); err != nil {
			continue // 扫描后已被清掉/移走，计划里不再呈现
		}
		p.Dirs = append(p.Dirs, d)
	}
	p.Dirs = dedupeNested(p.Dirs)
	for _, d := range p.Dirs {
		p.Bytes += d.Bytes
	}
	return p
}

// dedupeNested 去掉被父目录覆盖的子目录，避免"计划里算两遍、实际只移一次"
func dedupeNested(dirs []DirEntry) []DirEntry {
	out := make([]DirEntry, 0, len(dirs))
	for _, d := range dirs {
		covered := false
		for _, k := range out {
			kp := strings.ToLower(strings.TrimRight(k.Path, `\`)) + `\`
			if strings.HasPrefix(strings.ToLower(strings.TrimRight(d.Path, `\`))+`\`, kp) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	return out
}

func allPlans(cfg Config, st *Store) []Plan {
	out := make([]Plan, 0, len(planDefs))
	for _, d := range planDefs {
		out = append(out, buildPlan(cfg, st, d.Key))
	}
	return out
}

func planPreviewPage(cfg Config, st *Store, q *Quarantine, key string) string {
	p := buildPlan(cfg, st, key)
	var b strings.Builder
	b.WriteString("<h2>一键清理演练：" + htmlEscape(p.Label) + "</h2>")
	b.WriteString(`<p style="color:#8b94a7">` + htmlEscape(p.Desc) + `</p>`)
	if len(p.Dirs) == 0 {
		b.WriteString(`<div class="warn">没有符合条件的目录（可能还没扫描，或都被阈值/白名单排除了）。</div>`)
		b.WriteString(`<a class="btn" href="/">返回</a>`)
		return pageShell("一键清理演练", b.String())
	}
	b.WriteString(fmt.Sprintf("<p>将隔离 <b>%d</b> 个目录，合计 <b>%s</b>。全部可还原（%d 天内）。</p>",
		len(p.Dirs), human(p.Bytes), cfg.QuarantineDays))
	if used := q.totalBytes(); used+ p.Bytes > uint64(max(cfg.QuarantineMaxGB,1))<<30 {
		b.WriteString(`<div class="warn">隔离区会超过上限，请先还原/清理隔离区再执行。</div>`)
		b.WriteString(`<a class="btn" href="/">返回</a>`)
		return pageShell("隔离区已满", b.String())
	}
	b.WriteString("<table><tr><th>目录</th><th>类别</th><th style='text-align:right'>大小</th></tr>")
	for i, d := range p.Dirs {
		if i >= 80 {
			b.WriteString(fmt.Sprintf(enFormat("<tr><td colspan='3' style='color:#8b94a7'>… 另外 %d 个</td></tr>", "<tr><td colspan='3' style='color:#8b94a7'>… plus %d more</td></tr>"), len(p.Dirs)-80))
			break
		}
		meta := cats[d.Cat]
		b.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td>%s / %s</td><td style='text-align:right'>%s</td></tr>",
			htmlEscape(d.Path), meta.Label, tierText[meta.Tier], human(d.Bytes)))
	}
	b.WriteString("</table>")
	tid := issueTicketKind("plan", key)
	b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/plan/apply?n=` + tid + `">确认隔离这些目录</a>`)
	b.WriteString(`<a class="btn" href="/">取消</a>`)
	return pageShell("一键清理演练", b.String())
}

func planApply(cfg Config, st *Store, q *Quarantine, key string) (int, uint64, []string) {
	p := buildPlan(cfg, st, key)
	var okN int
	var freed uint64
	var fails []string
	for _, d := range p.Dirs {
		e, err := q.apply(cfg, d.Path, "plan:"+key)
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s: %v", d.Path, err))
			continue
		}
		okN++
		freed += e.Bytes
	}
	return okN, freed, fails
}

// 清理历史（从告警里筛出清理类事件）
func cleanHistory(st *Store, limit int) []Alert {
	alerts := st.Alerts()
	var out []Alert
	for i := len(alerts) - 1; i >= 0 && len(out) < limit; i-- {
		switch alerts[i].Kind {
		case "plan-run", "rule-run", "quarantine", "app-quarantine", "purge", "restore":
			out = append(out, alerts[i])
		}
	}
	return out
}

func planCardHTML(cfg Config, st *Store) string {
	plans := allPlans(cfg, st)
	var b strings.Builder
	b.WriteString(`<div class="cards">`)
	for _, p := range plans {
		b.WriteString(fmt.Sprintf(
			`<div class="rc" style="border-left-color:#4c8dff"><div class="t"><div class="n">%s</div><div class="sz">%s</div></div><div class="risk">%s</div><div class="row" style="margin-top:8px"><a class="act danger" href="/plan/preview?key=%s">演练并执行（%d 个目录）</a></div></div>`,
			p.Label, human(p.Bytes), htmlEscape(p.Desc), url.QueryEscape(p.Key), len(p.Dirs)))
	}
	b.WriteString("</div>")
	return b.String()
}
