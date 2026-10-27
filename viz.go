package main

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- 多盘 ----------

type VolumeInfo struct {
	Letter string
	Total  uint64
	Free   uint64
}

func listVolumes() []VolumeInfo {
	var out []VolumeInfo
	mask, _, _ := pGetLogicalDrives.Call()
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		l := string(rune('A'+i)) + ":"
		if v, err := volumeStats(l + `\`); err == nil && v.Total > 0 {
			out = append(out, VolumeInfo{Letter: l, Total: v.Total, Free: v.Free})
		}
	}
	return out
}

func volumesHTML(root string) string {
	monitored := rootVolume(root)
	vols := listVolumes()
	if len(vols) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<div style=\"margin-top:12px\"><h2 style=\"margin-bottom:6px\">多盘</h2>")
	for _, v := range vols {
		used := v.Total - v.Free
		pct := float64(used) / float64(v.Total) * 100
		col := "#4c8dff"
		if pct > 90 {
			col = "#f5486a"
		} else if pct > 80 {
			col = "#f5a524"
		}
		label := v.Letter
		if v.Letter == monitored {
			label += "（被监测）"
		}
		b.WriteString(fmt.Sprintf(`<div class="brow"><div class="blabel">%s</div><div class="btrack"><div class="bfill" style="width:%.1f%%;background:%s"></div></div><div class="bval">%s<span class="bpct">剩 %.1fG</span></div></div>`,
			label, pct, col, human(v.Total), gb(v.Free)))
	}
	b.WriteString("</div>")
	return b.String()
}

// ---------- Treemap ----------

type trect struct {
	x, y, w, h float64
	label      string
	size       uint64
	color      string
	tip        string
}

func sumBytes(items []DirEntry, from, to int) uint64 {
	var s uint64
	for i := from; i < to; i++ {
		s += items[i].Bytes
	}
	return s
}

func layoutTreemap(items []DirEntry, x, y, w, h float64, out *[]trect) {
	if len(items) == 0 || w < 0.5 || h < 0.5 {
		return
	}
	if len(items) == 1 {
		d := items[0]
		col := tierColor[cats[d.Cat].Tier]
		*out = append(*out, trect{x, y, w, h, filepath.Base(d.Path), d.Bytes, col,
			fmt.Sprintf("%s\n%s · %s", d.Path, human(d.Bytes), cats[d.Cat].Label)})
		return
	}
	total := sumBytes(items, 0, len(items))
	if total == 0 {
		return
	}
	acc := uint64(0)
	k := 1
	for i := 0; i < len(items); i++ {
		acc += items[i].Bytes
		if acc*2 >= total {
			k = i + 1
			break
		}
	}
	if k >= len(items) {
		k = len(items) - 1
	}
	left, right := items[:k], items[k:]
	frac := float64(sumBytes(left, 0, len(left))) / float64(total)
	if w >= h {
		layoutTreemap(left, x, y, w*frac, h, out)
		layoutTreemap(right, x+w*frac, y, w*(1-frac), h, out)
	} else {
		layoutTreemap(left, x, y, w, h*frac, out)
		layoutTreemap(right, x, y+h*frac, w, h*(1-frac), out)
	}
}

func treemapSVG(sc *Scan) string {
	if sc == nil {
		return "<div class=\"empty\">先扫描一次</div>"
	}
	items := append([]DirEntry(nil), sc.Deep...)
	sort.Slice(items, func(i, j int) bool { return items[i].Bytes > items[j].Bytes })
	if len(items) > 36 {
		items = items[:36]
	}
	if len(items) == 0 {
		return "<div class=\"empty\">没有足够大的可回收目录</div>"
	}
	const W, H = 640.0, 250.0
	var rects []trect
	layoutTreemap(items, 0, 0, W, H, &rects)
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" height="%.0f">`, W, H, H))
	for _, r := range rects {
		b.WriteString(fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" fill-opacity="0.85" stroke="#0e1116" stroke-width="1.5"><title>%s</title></rect>`,
			r.x, r.y, r.w, r.h, r.color, html.EscapeString(r.tip)))
		if r.w > 64 && r.h > 22 {
			b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" fill="#0b0e13" font-size="10" font-weight="600">%s</text>`,
				r.x+4, r.y+14, html.EscapeString(truncate(r.label, 12))))
			if r.h > 34 {
				b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" fill="#0b0e13" font-size="9" opacity="0.8">%s</text>`,
					r.x+4, r.y+26, human(r.size)))
			}
		}
	}
	b.WriteString("</svg>")
	return b.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------- 快照对比 ----------

func diffHTML(cfg Config) string {
	snaps := loadSnapshots(cfg)
	if len(snaps) < 2 {
		return `<div class="empty">快照不足：至少扫描两次后可用</div>`
	}
	cur := snaps[len(snaps)-1]
	base, ok := pickBaseline(snaps, 7*24*time.Hour)
	if !ok {
		return `<div class="empty">没有可对比的历史快照</div>`
	}
	growth, shrink := diffSnapshots(base, cur)
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<div class="muted">对比 %s → %s</div>`,
		time.Unix(base.TS, 0).Format("01-02 15:04"), time.Unix(cur.TS, 0).Format("01-02 15:04")))
	if len(growth) == 0 && len(shrink) == 0 {
		b.WriteString(`<div class="empty">两次扫描之间没有明显变化</div>`)
		return b.String()
	}
	b.WriteString("<table><tr><th>目录</th><th style=\"text-align:right\">变化</th></tr>")
	for _, g := range growth {
		b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td class=\"r\" style=\"color:#f5486a\">+%s</td></tr>",
			html.EscapeString(g.Path), human(uint64(g.Delta))))
	}
	for _, s := range shrink {
		b.WriteString(fmt.Sprintf("<tr><td class=\"path\">%s</td><td class=\"r\" style=\"color:#33c179\">-%s</td></tr>",
			html.EscapeString(s.Path), human(uint64(-s.Delta))))
	}
	b.WriteString("</table>")
	return b.String()
}

// ---------- 系统工具 ----------

var tools = []struct {
	Key   string
	Label string
	Desc  string
}{
	{"storagesense", "存储感知", "Windows 自带的自动清理设置"},
	{"cleanmgr", "磁盘清理", "经典 cleanmgr"},
	{"uninstall", "卸载程序", "已安装程序列表"},
	{"diskmgmt", "磁盘管理", "分区与卷"},
	{"recyclebin", "回收站", "查看可还原内容"},
}

func toolsHTML() string {
	var b strings.Builder
	b.WriteString(`<div class="muted" style="margin-bottom:8px">调用系统自带工具（只打开界面，不代你操作）</div><div class="cards">`)
	for _, t := range tools {
		b.WriteString(fmt.Sprintf(`<a class="act" style="display:block;padding:10px 12px" href="/tool?name=%s"><b>%s</b><br><span class="muted">%s</span></a>`,
			t.Key, t.Label, t.Desc))
	}
	b.WriteString("</div>")
	return b.String()
}

func launchTool(name string) error {
	switch name {
	case "storagesense":
		return startHidden("cmd.exe", "/c", "start", "", "ms-settings:storagesense")
	case "cleanmgr":
		return startHidden("cleanmgr.exe")
	case "uninstall":
		return startHidden("control.exe", "appwiz.cpl")
	case "diskmgmt":
		return startHidden("diskmgmt.msc")
	case "recyclebin":
		return startHidden("explorer.exe", "shell:::{645FF040-5081-101B-9F08-00AA002F954E}")
	}
	return fmt.Errorf("未知工具：%s", name)
}
