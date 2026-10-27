package main

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

var dupRunning atomic.Bool

func startDupScan(cfg Config, st *Store, root string, minMB int) {
	if !dupRunning.CompareAndSwap(false, true) {
		return
	}
	if !scanMu.TryLock() { // 全盘扫描进行中就先不跑，避免互相拖慢
		dupRunning.Store(false)
		st.AddAlert("dup-scan", "全盘扫描进行中，重复文件扫描已跳过（请稍后重试）")
		return
	}
	scanMu.Unlock()
	go func() {
		defer dupRunning.Store(false)
		runDupScan(cfg, st, root, minMB)
	}()
}

// ---------- 重复文件页 ----------

func dupPage(cfg Config, st *Store) string {
	var b strings.Builder
	b.WriteString("<h2>重复文件</h2>")
	b.WriteString(`<p style="color:#8b94a7">按大小预筛 → 首尾哈希 → 完整 SHA-256。只报告；隔离时每组保留第一份。</p>`)
	if dupRunning.Load() {
		b.WriteString(`<div class="warn">正在扫描（全盘约 1–3 分钟，受时间预算限制）。<br><a class="btn" href="/dup">刷新查看</a></div>`)
		return pageShell("重复文件", b.String())
	}
	b.WriteString(`<div style="margin-bottom:12px"><a class="btn" href="/dup/scan">开始全盘扫描（≥4 MB）</a></div>`)
	r := loadDups(cfg)
	if r == nil || len(r.Groups) == 0 {
		b.WriteString(`<div class="muted">还没有结果。点上面的按钮开始扫描。</div>`)
		return pageShell("重复文件", b.String())
	}
	b.WriteString(fmt.Sprintf("<p>扫描于 %s · 根目录 <code>%s</code> · ≥%s · %d 组 · 可释放 <b>%s</b>%s</p>",
		time.Unix(r.TS, 0).Format("01-02 15:04"), html.EscapeString(r.Root), human(r.MinSize),
		len(r.Groups), human(r.TotalWasted),
		map[bool]string{true: "（时间预算用尽，结果不完整）", false: ""}[r.Truncated]))
	b.WriteString("<table><tr><th>大小</th><th>份数</th><th>浪费</th><th>路径（保留第一份）</th><th></th></tr>")
	for i, g := range r.Groups {
		if i >= 50 {
			b.WriteString(fmt.Sprintf("<tr><td colspan='5' class='muted'>… 另外 %d 组</td></tr>", len(r.Groups)-50))
			break
		}
		var paths []string
		for j, p := range g.Paths {
			if j >= 3 {
				paths = append(paths, fmt.Sprintf("… 另外 %d 份", len(g.Paths)-3))
				break
			}
			mark := ""
			if j == 0 {
				mark = "★ "
			}
			paths = append(paths, mark+html.EscapeString(p))
		}
		warn := ""
		if hasInstallPath(g.Paths) {
			warn = " <span class='muted'>⚠ 含安装目录，执行时会跳过</span>"
		}
		tid := issueTicketKind("dup", g.Hash)
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%d</td><td>%s</td><td><code>%s</code>%s</td><td><a class='act danger' href='/dup/apply?n=%s'>隔离副本</a></td></tr>",
			human(g.Size), len(g.Paths), human(g.Wasted), strings.Join(paths, "<br>"), warn, tid))
	}
	b.WriteString("</table>")
	b.WriteString(`<div class="muted">★ 表示保留的那一份。隔离后可随时还原。</div>`)
	return pageShell("重复文件", b.String())
}

// ---------- 卸载残留页 ----------

func leftoversPage(cfg Config, st *Store) string {
	items := leftoversCached(cfg, st)
	at, busy := leftoversState()
	var b strings.Builder
	b.WriteString("<h2>卸载残留检查</h2>")
	b.WriteString(`<p style="color:#8b94a7">基于注册表卸载项 + 文件系统启发。注册表孤立项只报告不代删。</p>`)
	if at.IsZero() || busy {
		b.WriteString(`<div class="warn">正在后台计算，几秒后刷新即可看到结果。<a href="/leftovers">刷新</a></div>`)
	} else {
		b.WriteString(fmt.Sprintf(`<div class="muted">结果计算于 %s · <a href="/leftovers?refresh=1">重新扫描</a></div>`, at.Format("15:04:05")))
	}
	if len(items) == 0 {
		if at.IsZero() || busy {
			b.WriteString(`<div class="muted">正在计算…</div>`)
		} else {
			b.WriteString(`<div class="ok">没有发现明显的卸载残留。</div>`)
		}
		return pageShell("卸载残留", b.String())
	}
	var dataBytes uint64
	var dataN int
	for _, it := range items {
		if it.Kind == "data-leftover" {
			dataBytes += it.Bytes
			dataN++
		}
	}
	b.WriteString(fmt.Sprintf("<p>疑似数据残留 <b>%d</b> 项 · <b>%s</b>；注册表孤立 <b>%d</b> 条。</p>",
		dataN, human(dataBytes), countKind(items, "registry-orphan")))
	b.WriteString("<table><tr><th>类型</th><th>名称</th><th>路径</th><th>大小</th><th></th></tr>")
	for _, it := range items {
		kind := "注册表孤立"
		act := ""
		if it.Kind == "data-leftover" {
			kind = "数据残留"
			act = fmt.Sprintf("<a class='act' href='/q/preview?path=%s'>演练/隔离</a>", url.QueryEscape(it.Path))
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td><code>%s</code><br><span class='muted'>%s</span></td><td>%s</td><td>%s</td></tr>",
			kind, html.EscapeString(it.Name), html.EscapeString(it.Path), html.EscapeString(it.Reason),
			human(it.Bytes), act))
	}
	b.WriteString("</table>")
	return pageShell("卸载残留", b.String())
}

// ---------- 看板卡片 ----------

func dupCardHTML(cfg Config) string {
	var b strings.Builder
	r := loadDups(cfg)
	b.WriteString(`<div class="kpi"><div><span class="big">`)
	if r != nil {
		b.WriteString(human(r.TotalWasted))
	} else {
		b.WriteString("—")
	}
	b.WriteString(`</span><span class="unit"> 可释放（重复副本）</span></div>`)
	if r != nil {
		b.WriteString(fmt.Sprintf(`<div class="muted">%d 组 · 扫描于 %s</div>`, len(r.Groups), time.Unix(r.TS, 0).Format("01-02 15:04")))
	}
	b.WriteString(`</div>`)
	if r != nil && len(r.Groups) > 0 {
		b.WriteString(`<table style="margin-top:8px">`)
		for i, g := range r.Groups {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf("<tr><td>%s × %d</td><td class='r'>%s</td></tr>", human(g.Size), len(g.Paths), human(g.Wasted)))
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`<div style="margin-top:8px"><a class="act" href="/dup">查看 / 扫描重复文件</a></div>`)
	return b.String()
}

func leftoversCardHTML(cfg Config, st *Store) string {
	items := leftoversCached(cfg, st)
	at, busy := leftoversState()
	var dataBytes uint64
	var dataN, regN int
	for _, it := range items {
		if it.Kind == "data-leftover" {
			dataBytes += it.Bytes
			dataN++
		} else {
			regN++
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<div class="kpi"><div><span class="big">%s</span><span class="unit"> 疑似残留数据</span></div><div class="muted">%d 项 · 注册表孤立 %d 条</div></div>`,
		human(dataBytes), dataN, regN))
	if at.IsZero() || busy {
		b.WriteString(`<div class="muted" style="margin-top:6px">正在后台计算…</div>`)
	} else {
		b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:6px">计算于 %s · 依据：数据目录存在但无安装程序/卸载登记</div>`, at.Format("15:04:05")))
	}
	b.WriteString(`<div style="margin-top:8px"><a class="act" href="/leftovers">查看明细</a></div>`)
	return b.String()
}

func hasInstallPath(paths []string) bool {
	for _, p := range paths {
		if isInstallPath(p) {
			return true
		}
	}
	return false
}
