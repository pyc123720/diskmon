package main

import "strings"

// ---------- 详情分区页（收敛为 6 组） ----------

type sectionDef struct {
	Key   string
	Label string
	IDs   []string
	Desc  string
}

var sectionDefs = []sectionDef{
	{"capacity", "容量", []string{"overview"}, "容量环、多盘余量"},
	{"trend", "趋势", []string{"forecast", "daily", "anomaly", "diff"}, "预测、每日增长、性质、对比"},
	{"cats", "分类", []string{"cats", "treemap"}, "分类占用与 Treemap"},
	{"files", "文件", []string{"dirs", "files"}, "目录排行与大文件"},
	{"clean", "清理", []string{"plans", "risks", "quarantine", "apps", "dup", "leftovers", "procs"}, "方案、风险卡、隔离区、应用、重复、残留、进程"},
	{"system", "系统", []string{"engine", "alerts", "tools"}, "引擎、告警、系统工具"},
}

// 旧分区 key → 新的组（保持旧链接可用）
var legacySection = map[string]string{
	"overview": "capacity",
	"forecast": "trend", "daily": "trend", "anomaly": "trend", "diff": "trend",
	"risks": "clean", "quarantine": "clean", "plans": "clean", "rules": "clean",
	"apps": "clean", "procs": "clean", "dup": "clean", "leftovers": "clean",
	"engine": "system", "alerts": "system", "tools": "system",
}

func sectionByKey(key string) (sectionDef, bool) {
	for _, d := range sectionDefs {
		if d.Key == key {
			return d, true
		}
	}
	return sectionDef{}, false
}

// 从完整 HTML 里按 id 切出一个 <div> 子树（按 div 深度配对）
func sliceSection(full, id string) (string, bool) {
	marker := `id="` + id + `"`
	i := strings.Index(full, marker)
	if i < 0 {
		return "", false
	}
	start := strings.LastIndex(full[:i], "<div")
	if start < 0 {
		return "", false
	}
	depth := 0
	j := start
	for j < len(full) {
		switch {
		case strings.HasPrefix(full[j:], "<div"):
			depth++
			j += 4
		case strings.HasPrefix(full[j:], "</div>"):
			depth--
			j += 6
			if depth == 0 {
				return full[start:j], true
			}
		default:
			j++
		}
	}
	return "", false
}

func sidebarHTML(active string) string {
	var b strings.Builder
	b.WriteString(`<div class="side">`)
	for _, d := range sectionDefs {
		cls := ""
		if d.Key == active {
			cls = ` class="on"`
		}
		b.WriteString(`<a href="/detail/` + d.Key + `"` + cls + ` title="` + htmlEscape(d.Desc) + `">` + htmlEscape(d.Label) + `</a>`)
	}
	b.WriteString(`<a href="/detail" style="margin-top:8px;border-top:1px solid #1e2430;padding-top:8px">全部数据</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

func sectionPage(cfg Config, st *Store, key string) (string, bool) {
	if to, ok := legacySection[key]; ok {
		key = to
	}
	def, ok := sectionByKey(key)
	if !ok {
		return "", false
	}
	full := renderPage(cfg, st)
	var body strings.Builder
	found := 0
	for _, id := range def.IDs {
		if frag, ok := sliceSection(full, id); ok {
			body.WriteString(frag)
			found++
		}
	}
	if found == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(`<div class="withside">`)
	b.WriteString(sidebarHTML(key))
	b.WriteString(`<div>` + body.String())
	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/detail">全部数据</a>`)
	switch def.Key {
	case "clean":
		b.WriteString(`<a class="btn" href="/clean">去清理中心</a>`)
	case "trend":
		b.WriteString(`<a class="btn" href="/trend">趋势专页</a>`)
	case "files":
		b.WriteString(`<a class="btn" href="/export?what=files">导出大文件 CSV</a>`)
	}
	b.WriteString(`</div></div></div>`)
	return appShell(def.Label+" · 全部数据", "detail", b.String()), true
}
