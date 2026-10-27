package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
)

// ---------- CSV 导出 ----------

type exportDef struct {
	Key   string
	Label string
	Desc  string
}

var exportDefs = []exportDef{
	{"dirs", "目录排行", "按大小排序的目录（深度 3）"},
	{"files", "大文件", "Top 大文件"},
	{"apps", "按应用聚合", "应用 → 可回收大小"},
	{"dups", "重复文件", "每组重复文件与副本路径"},
	{"media", "媒体分类", "目录 × 类型 的大小"},
	{"alerts", "告警历史", "最近 200 条告警"},
	{"quarantine", "隔离区", "当前隔离项"},
}

func exportPage(cfg Config) string {
	var b = `<h2 style="margin-top:0">导出 CSV</h2>
<p class="muted">带 UTF-8 BOM，Excel 直接打开中文不乱码。</p><table><tr><th>数据</th><th>说明</th><th></th></tr>`
	for _, d := range exportDefs {
		b += fmt.Sprintf(`<tr><td>%s</td><td class="muted">%s</td><td><a class="act" href="/export?what=%s">下载 CSV</a></td></tr>`,
			d.Label, htmlEscape(d.Desc), d.Key)
	}
	b += `</table>`
	return appShell("导出", "export", b)
}

func writeCSV(w http.ResponseWriter, name string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM，Excel 友好
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, r := range rows {
		_ = cw.Write(r)
	}
	cw.Flush()
}

func handleExport(cfg Config, st *Store, q *Quarantine, w http.ResponseWriter, r *http.Request) {
	what := r.URL.Query().Get("what")
	switch what {
	case "dirs":
		rows := [][]string{}
		if sc := st.GetScan(); sc != nil {
			for i, d := range sc.Dirs3 {
				if i >= 500 {
					break
				}
				meta := cats[d.Cat]
				rows = append(rows, []string{d.Path, strconv.FormatUint(d.Bytes, 10), human(d.Bytes), meta.Label, tierText[meta.Tier]})
			}
		}
		writeCSV(w, "diskmon-dirs", []string{"path", "bytes", "size", "category", "tier"}, rows)

	case "files":
		rows := [][]string{}
		if sc := st.GetScan(); sc != nil {
			for _, f := range sc.TopFiles {
				rows = append(rows, []string{f.Path, strconv.FormatUint(f.Bytes, 10), human(f.Bytes)})
			}
		}
		writeCSV(w, "diskmon-files", []string{"path", "bytes", "size"}, rows)

	case "apps":
		rows := [][]string{}
		for _, g := range appGroups(cfg, st) {
			rows = append(rows, []string{g.Name, strconv.FormatUint(g.Bytes, 10), human(g.Bytes), strconv.Itoa(len(g.Dirs))})
		}
		writeCSV(w, "diskmon-apps", []string{"app", "bytes", "size", "folders"}, rows)

	case "dups":
		rows := [][]string{}
		if r := loadDups(cfg); r != nil {
			for _, g := range r.Groups {
				for i, p := range g.Paths {
					rows = append(rows, []string{g.Hash, strconv.FormatUint(g.Size, 10), human(g.Size), strconv.Itoa(len(g.Paths)), strconv.Itoa(i + 1), p})
				}
			}
		}
		writeCSV(w, "diskmon-dups", []string{"hash", "bytes_each", "size_each", "copies", "index", "path"}, rows)

	case "media":
		rows := [][]string{}
		if r := mediaCached(cfg, st); r != nil {
			for _, d := range r.Dirs {
				for _, mb := range mediaBuckets {
					if v := d.Bytes[mb.Key]; v > 0 {
						rows = append(rows, []string{d.Dir, mb.Label, strconv.FormatUint(v, 10), human(v), strconv.Itoa(d.Files[mb.Key])})
					}
				}
			}
		}
		writeCSV(w, "diskmon-media", []string{"dir", "type", "bytes", "size", "files"}, rows)

	case "alerts":
		rows := [][]string{}
		for _, a := range st.Alerts() {
			rows = append(rows, []string{strconv.FormatInt(a.TS, 10), a.Kind, a.Text})
		}
		writeCSV(w, "diskmon-alerts", []string{"ts", "kind", "text"}, rows)

	case "quarantine":
		rows := [][]string{}
		for _, e := range q.entries() {
			restored := "0"
			if e.Restored {
				restored = "1"
			}
			rows = append(rows, []string{e.ID, e.Original, strconv.FormatUint(e.Bytes, 10), human(e.Bytes),
				strconv.Itoa(e.Files), strconv.FormatInt(e.TS, 10), restored, e.Reason})
		}
		writeCSV(w, "diskmon-quarantine", []string{"id", "original", "bytes", "size", "files", "ts", "restored", "reason"}, rows)

	default:
		http.Redirect(w, r, "/export", http.StatusSeeOther)
	}
}
