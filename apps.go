package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ---------- 按应用聚合的清理建议 ----------

type AppGroup struct {
	Name    string
	Bytes   uint64
	Dirs    []DirEntry
	Refused int
	Tier    map[string]int
}

func appGroups(cfg Config, st *Store) []AppGroup {
	sc := st.GetScan()
	if sc == nil {
		return nil
	}
	wl := make([]string, len(cfg.Whitelist))
	for i, w := range cfg.Whitelist {
		wl[i] = strings.ToLower(w)
	}
	byName := map[string]*AppGroup{}
	for _, d := range sc.Deep {
		meta, ok := cats[d.Cat]
		if !ok || (meta.Tier != "clean" && meta.Tier != "confirm") {
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
		name := ownerOf(d.Path)
		if name == "" {
			// 兜底：用末级目录名，避免所有未识别目录堆进「未归属」
			trimmed := strings.TrimRight(d.Path, `\`)
			if i := strings.LastIndex(trimmed, `\`); i > 0 {
				name = trimmed[i+1:]
			}
		}
		if name == "" {
			name = "未归属"
		}
		g := byName[name]
		if g == nil {
			g = &AppGroup{Name: name, Tier: map[string]int{}}
			byName[name] = g
		}
		if blocked || deniedForCleanup(d.Path) {
			g.Refused++
			continue
		}
		g.Bytes += d.Bytes
		g.Dirs = append(g.Dirs, d)
		g.Tier[meta.Tier]++
	}
	var out []AppGroup
	for _, g := range byName {
		if g.Bytes == 0 {
			continue
		}
		sort.Slice(g.Dirs, func(i, j int) bool { return g.Dirs[i].Bytes > g.Dirs[j].Bytes })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func appPage(cfg Config, st *Store, name string) string {
	var b strings.Builder
	b.WriteString("<h2>应用聚合建议：" + htmlEscape(name) + "</h2>")
	var total uint64
	var dirs []DirEntry
	for _, g := range appGroups(cfg, st) {
		if g.Name == name {
			total = g.Bytes
			dirs = g.Dirs
			break
		}
	}
	if len(dirs) == 0 {
		b.WriteString(`<div class="warn">没有可回收的目录</div><a class="btn" href="/">返回看板</a>`)
		return pageShell(name, b.String())
	}
	b.WriteString(fmt.Sprintf("<p>合计 <b>%s</b>，%d 个目录。逐项演练，或一次性全部隔离（可还原）。</p>", human(total), len(dirs)))
	b.WriteString("<table><tr><th>目录</th><th>类别</th><th style='text-align:right'>大小</th><th></th></tr>")
	for _, d := range dirs {
		meta := cats[d.Cat]
		b.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td>%s / %s</td><td style='text-align:right'>%s</td><td><a class='act' href='/q/preview?path=%s'>演练/隔离</a></td></tr>",
			htmlEscape(d.Path), meta.Label, tierText[meta.Tier], human(d.Bytes), url.QueryEscape(d.Path)))
	}
	b.WriteString("</table>")
	b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/app/preview?name=` + url.QueryEscape(name) + `">隔离全部（可还原）</a>`)
	b.WriteString(`<a class="btn" href="/">返回看板</a>`)
	return pageShell(name, b.String())
}

// 一次性隔离某应用的所有可回收目录
func appApply(cfg Config, st *Store, q *Quarantine, name string) (int, uint64, []string) {
	var okN int
	var freed uint64
	var failures []string
	for _, g := range appGroups(cfg, st) {
		if g.Name != name {
			continue
		}
		for _, d := range g.Dirs {
			e, err := q.apply(cfg, d.Path, "app:"+name)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", d.Path, err))
				continue
			}
			okN++
			freed += e.Bytes
		}
	}
	return okN, freed, failures
}

func printApps(cfg Config, st *Store) {
	groups := appGroups(cfg, st)
	if len(groups) == 0 {
		fmt.Println("没有可聚合的建议（先扫描）")
		return
	}
	for _, g := range groups {
		dirs := len(g.Dirs)
		fmt.Printf("%-16s %10s  %d 个目录", g.Name, human(g.Bytes), dirs)
		if g.Refused > 0 {
			fmt.Printf("（%d 个被保护跳过）", g.Refused)
		}
		fmt.Println()
		for i, d := range g.Dirs {
			if i >= 3 {
				fmt.Printf(enFormat("    … 另外 %d 个\n", "    … plus %d more\n"), dirs-3)
				break
			}
			fmt.Printf("    %10s  %s\n", human(d.Bytes), d.Path)
		}
	}
}
