package main

import "strings"

// ---------- 详情分区页（B 方案） ----------
//
// 只保留一份渲染真相：/detail 整体渲染，分区页从它的 HTML 里按 id 切出对应子树。
// 好处是不存在"两套页面代码"，不会出现详情改了、分区页没改；代价是服务端仍渲染整页（~10ms）。

type sectionDef struct {
	Key   string
	Label string
	ID    string
	Desc  string
}

var sectionDefs = []sectionDef{
	{"overview", "容量", "overview", "容量环与多盘余量"},
	{"forecast", "预测", "forecast", "写满预测与投影"},
	{"daily", "每日", "daily", "每日新增柱状图"},
	{"anomaly", "性质", "anomaly", "跳变 vs 泄漏判定"},
	{"cats", "分类", "cats", "分类占用与可删占比"},
	{"treemap", "Treemap", "treemap", "空间分布"},
	{"diff", "对比", "diff", "与上次/一周前对比"},
	{"risks", "风险卡", "risks", "可回收项与后果"},
	{"quarantine", "隔离区", "quarantine", "可还原的隔离项"},
	{"plans", "方案", "plans", "一键清理三档"},
	{"rules", "规则", "rules", "自动清理规则状态"},
	{"apps", "应用", "apps", "按应用聚合"},
	{"procs", "进程", "procs", "写入热点进程"},
	{"dup", "重复", "dup", "重复文件摘要"},
	{"leftovers", "残留", "leftovers", "卸载残留摘要"},
	{"dirs", "目录", "dirs", "目录排行"},
	{"files", "大文件", "files", "Top 大文件"},
	{"engine", "引擎", "engine", "引擎与自占内存"},
	{"alerts", "告警", "alerts", "最近告警"},
	{"tools", "工具", "tools", "系统工具入口"},
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
	def, ok := sectionByKey(key)
	if !ok {
		return "", false
	}
	full := renderPage(cfg, st)
	frag, ok := sliceSection(full, def.ID)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(`<div class="withside">`)
	b.WriteString(sidebarHTML(key))
	b.WriteString(`<div>` + frag)
	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/detail">全部数据</a>`)
	switch def.Key {
	case "plans", "quarantine", "dup", "leftovers", "apps":
		b.WriteString(`<a class="btn" href="/clean">去清理中心</a>`)
	case "forecast", "daily", "anomaly", "diff":
		b.WriteString(`<a class="btn" href="/trend">去趋势页</a>`)
	case "rules":
		b.WriteString(`<a class="btn" href="/rules">去规则页</a>`)
	}
	b.WriteString(`</div></div></div>`)
	return appShell(def.Label+" · 详情", "detail", b.String()), true
}
