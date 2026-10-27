package main

import (
	"fmt"
	"strings"
	"time"
)

// ---------- 隔离区页面（批量还原） ----------

func qPage(cfg Config, st *Store) string {
	q := openQuarantine(cfg)
	entries := q.entries()
	var live []QEntry
	var bytes uint64
	for _, e := range entries {
		if e.Restored {
			continue
		}
		live = append(live, e)
		bytes += e.Bytes
	}
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">隔离区</h2>`)
	b.WriteString(fmt.Sprintf(`<div class="kpi"><div><span class="big">%s</span><span class="unit"> 已隔离 · %d 项</span></div><div class="muted">上限 %d GB · 保留 %d 天</div></div>`,
		human(bytes), len(live), cfg.QuarantineMaxGB, cfg.QuarantineDays))
	b.WriteString(`<div class="muted">这里的文件只是被移走，没有删除。还原会把它们放回原路径；超过保留天数会永久清除。</div>`)

	if len(live) == 0 {
		b.WriteString(`<div class="ok" style="margin-top:12px">隔离区是空的。</div>`)
		b.WriteString(`<a class="btn" href="/">回总览</a>`)
		return appShell("隔离区", "clean", b.String())
	}

	b.WriteString(`<form method="get" action="/q/restore-batch" style="margin-top:14px">`)
	b.WriteString(`<table><tr><th style="width:36px">选</th><th>原路径</th><th style="text-align:right">大小</th><th>时间</th><th>原因</th><th></th></tr>`)
	for i, e := range live {
		if i >= 300 {
			b.WriteString(fmt.Sprintf(`<tr><td colspan="6" class="muted">… 还有 %d 项</td></tr>`, len(live)-i))
			break
		}
		b.WriteString(fmt.Sprintf(`<tr><td><input type="checkbox" name="ids" value="%s"></td><td><code>%s</code></td><td class="r">%s</td><td class="muted">%s</td><td class="muted">%s</td><td><a class="act" href="/q/restore?id=%s">还原</a></td></tr>`,
			htmlEscape(e.ID), htmlEscape(e.Original), human(e.Bytes), time.Unix(e.TS, 0).Format("01-02 15:04"), htmlEscape(e.Reason), htmlEscape(e.ID)))
	}
	b.WriteString(`</table>`)
	b.WriteString(`<div style="margin-top:12px;display:flex;gap:10px;flex-wrap:wrap">`)
	b.WriteString(`<button class="btn" type="submit" style="cursor:pointer;background:#1f6f4a;border-color:#33c179">还原选中</button>`)
	b.WriteString(`<a class="btn" href="/q/restore-batch?all=1">还原全部（` + fmt.Sprintf("%d 项", len(live)) + `）</a>`)
	b.WriteString(`<a class="btn" href="/q/rebuild">重建索引</a>`)
	b.WriteString(`</div></form>`)
	b.WriteString(`<div class="muted" style="margin-top:10px">还原是后台任务，会显示进度；同一时间只跑一个。</div>`)
	return appShell("隔离区", "clean", b.String())
}

// qRestoreIDs 收集要还原的隔离项：all=1 取全部未还原项，否则用勾选的 ids
func qRestoreIDs(cfg Config, all bool, ids []string) []string {
	if all {
		var out []string
		for _, e := range openQuarantine(cfg).entries() {
			if !e.Restored {
				out = append(out, e.ID)
			}
		}
		return out
	}
	return ids
}
