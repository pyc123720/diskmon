package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- 媒体分类（图片 / 视频 / 文档 / 音频 / 压缩包 / 程序 / 其它） ----------

var mediaBuckets = []struct{ Key, Label, Color string }{
	{"image", "图片", "#33c179"},
	{"video", "视频", "#4c8dff"},
	{"doc", "文档", "#c9a24a"},
	{"audio", "音频", "#a78bfa"},
	{"archive", "压缩包", "#f0883e"},
	{"program", "程序", "#8b94a7"},
	{"other", "其它", "#5b6474"},
}

var mediaExt = map[string]string{
	"image": "jpg jpeg png gif webp bmp heic heif tif tiff raw cr2 nef arw dng psd svg ico avif",
	"video": "mp4 mkv avi mov wmv flv webm m4v mpg mpeg 3gp rmvb mts m2ts",
	"audio": "mp3 flac wav aac m4a ogg wma amr opus",
	"doc":   "pdf doc docx xls xlsx ppt pptx txt md csv rtf epub odt ods mobi chm",
	"archive": "zip rar 7z tar gz bz2 xz iso cab tgz",
	"program": "exe dll msi sys node pyd so lib jar apk deb rpm",
}

var mediaExtIndex = func() map[string]string {
	m := map[string]string{}
	for k, list := range mediaExt {
		for _, e := range strings.Fields(list) {
			m[e] = k
		}
	}
	return m
}()

func bucketOf(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if b, ok := mediaExtIndex[ext]; ok {
		return b
	}
	return "other"
}

type MediaDir struct {
	Dir     string             `json:"dir"`
	Bytes   map[string]uint64  `json:"bytes"`
	Files   map[string]int     `json:"files"`
}

type MediaResult struct {
	TS     int64             `json:"ts"`
	Totals map[string]uint64 `json:"totals"`
	Files  map[string]int    `json:"files"`
	Dirs   []MediaDir        `json:"dirs"`
	Scanned int              `json:"scanned"`
}

func mediaPath(cfg Config) string { return filepath.Join(cfg.DataDir, "media.json") }

func loadMedia(cfg Config) *MediaResult {
	b, err := os.ReadFile(mediaPath(cfg))
	if err != nil {
		return nil
	}
	var r MediaResult
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func saveMedia(cfg Config, r *MediaResult) {
	if b, err := json.Marshal(r); err == nil {
		_ = writeFileAtomic(mediaPath(cfg), b)
	}
}

// 扫描候选目录（clean/confirm 层、≥50MB），按扩展名分桶
func scanMedia(cfg Config, st *Store) *MediaResult {
	t0 := time.Now()
	res := &MediaResult{TS: t0.Unix(), Totals: map[string]uint64{}, Files: map[string]int{}}
	sc := st.GetScan()
	if sc == nil {
		return res
	}
	wl := make([]string, len(cfg.Whitelist))
	for i, w := range cfg.Whitelist {
		wl[i] = strings.ToLower(w)
	}
	for _, d := range sc.Deep {
		meta, ok := cats[d.Cat]
		if !ok || (meta.Tier != "clean" && meta.Tier != "confirm") || d.Bytes < 50<<20 {
			continue
		}
		if deniedForCleanup(d.Path) {
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
		if skip {
			continue
		}
		if info, e := os.Stat(d.Path); e != nil || !info.IsDir() {
			continue
		}
		md := MediaDir{Dir: d.Path, Bytes: map[string]uint64{}, Files: map[string]int{}}
		_ = filepath.WalkDir(d.Path, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			info, ierr := e.Info()
			if ierr != nil {
				return nil
			}
			b := bucketOf(p)
			md.Bytes[b] += uint64(info.Size())
			md.Files[b]++
			res.Totals[b] += uint64(info.Size())
			res.Files[b]++
			return nil
		})
		if len(md.Bytes) > 0 {
			res.Dirs = append(res.Dirs, md)
		}
		res.Scanned++
	}
	sort.Slice(res.Dirs, func(i, j int) bool {
		return mediaDirTotal(res.Dirs[i]) > mediaDirTotal(res.Dirs[j])
	})
	if len(res.Dirs) > 200 {
		res.Dirs = res.Dirs[:200]
	}
	return res
}

func mediaDirTotal(d MediaDir) uint64 {
	var t uint64
	for _, v := range d.Bytes {
		t += v
	}
	return t
}

// 后台缓存（复用 leftovers 的模式）
var (
	mdMu    sync.Mutex
	mdCache *MediaResult
	mdAt    time.Time
	mdBusy  bool
)

const mdTTL = 30 * time.Minute

func mediaCached(cfg Config, st *Store) *MediaResult {
	mdMu.Lock()
	if mdCache != nil && time.Since(mdAt) < mdTTL {
		r := mdCache
		mdMu.Unlock()
		return r
	}
	kick := !mdBusy
	if kick {
		mdBusy = true
	}
	r := mdCache
	mdMu.Unlock()
	if kick {
		go func() {
			res := scanMedia(cfg, st)
			saveMedia(cfg, res)
			mdMu.Lock()
			mdCache = res
			mdAt = time.Now()
			mdBusy = false
			mdMu.Unlock()
			logf("media: 后台分类完成，%d 个目录", len(res.Dirs))
		}()
	}
	return r
}

func mediaInvalidate() {
	mdMu.Lock()
	mdAt = time.Time{}
	mdMu.Unlock()
}

func mediaState() (time.Time, bool) {
	mdMu.Lock()
	defer mdMu.Unlock()
	return mdAt, mdBusy
}

// ---------- 按类型清理（文件级，进隔离区） ----------

type mediaBatch struct {
	ID      string
	Running bool
	Total   int
	Done    int
	Freed   uint64
	Failed  int
	Kind    string
	Dir     string
	Days    int
}

var (
	mbMu      sync.Mutex
	mbBatches = map[string]*mediaBatch{}
)

func mediaBatchState(id string) mediaBatch {
	mbMu.Lock()
	defer mbMu.Unlock()
	if b, ok := mbBatches[id]; ok {
		return *b
	}
	return mediaBatch{ID: id}
}

// 收集某目录下某类型、早于 days 天的文件
func collectMediaFiles(dir, kind string, days int) []string {
	cut := time.Now().AddDate(0, 0, -days).Unix()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if bucketOf(p) != kind {
			return nil
		}
		info, ierr := e.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().Unix() > cut {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if len(out) > 5000 {
		out = out[:5000]
	}
	return out
}

func startMediaClean(cfg Config, st *Store, q *Quarantine, dir, kind string, days int) string {
	files := collectMediaFiles(dir, kind, days)
	if len(files) == 0 {
		return ""
	}
	if len(files) > 500 {
		files = files[:500]
	}
	id := fmt.Sprintf("m%d", time.Now().UnixNano()/1e6)
	b := &mediaBatch{ID: id, Running: true, Total: len(files), Kind: kind, Dir: dir, Days: days}
	mbMu.Lock()
	mbBatches[id] = b
	mbMu.Unlock()

	go func() {
		var freed uint64
		var failed int
		for i, f := range files {
			e, err := q.apply(cfg, f, "media:"+id)
			mbMu.Lock()
			b.Done = i + 1
			if err != nil {
				failed++
				b.Failed = failed
			} else {
				freed += e.Bytes
				b.Freed = freed
			}
			mbMu.Unlock()
		}
		mbMu.Lock()
		b.Running = false
		mbMu.Unlock()
		label := kind
		for _, d := range mediaBuckets {
			if d.Key == kind {
				label = d.Label
			}
		}
		st.AddAlert("media-clean", fmt.Sprintf("媒体清理：%s %d 个文件，释放 %s（可撤销）", label, len(files)-failed, human(freed)))
		notify("diskmon 媒体清理完成", fmt.Sprintf("%s：释放 %s", label, human(freed)), false)
	}()
	return id
}

func undoMediaBatch(q *Quarantine, st *Store, id string) (int, uint64) {
	var n int
	var bytes uint64
	for _, e := range q.entries() {
		if e.Reason != "media:"+id || e.Restored {
			continue
		}
		r, err := q.restore(e.ID)
		if err != nil {
			continue
		}
		n++
		bytes += r.Bytes
	}
	if n > 0 {
		st.AddAlert("media-undo", fmt.Sprintf("媒体清理已撤销：还原 %d 个文件，%s", n, human(bytes)))
	}
	return n, bytes
}

// 一次遍历得到该目录各类型的文件数与字节数
func collectMediaSummary(dir string, days int) map[string][2]uint64 {
	cut := time.Now().AddDate(0, 0, -days).Unix()
	out := map[string][2]uint64{}
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().Unix() > cut {
			return nil
		}
		b := bucketOf(p)
		v := out[b]
		v[0]++
		v[1] += uint64(info.Size())
		out[b] = v
		return nil
	})
	return out
}

// ---------- 页面 ----------

func mediaPage(cfg Config, st *Store) string {
	r := mediaCached(cfg, st)
	at, busy := mediaState()
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">媒体分类</h2>`)
	b.WriteString(`<p class="muted">把「缓存/聊天媒体」按类型拆开：图片、视频、文档、音频、压缩包、程序。只统计可清/需确认类目录。</p>`)
	if at.IsZero() || busy {
		b.WriteString(`<div class="warn">正在后台分类（首次约 10–30 秒），稍后<a href="/media">刷新</a>。</div>`)
	}
	if r == nil {
		return appShell("媒体分类", "media", b.String())
	}
	// 总计
	b.WriteString(`<div class="card"><h2>总量</h2><table><tr><th>类型</th><th style="text-align:right">大小</th><th style="text-align:right">文件数</th><th></th></tr>`)
	for _, mb := range mediaBuckets {
		sz := r.Totals[mb.Key]
		if sz == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf(`<tr><td><span style="color:%s">■</span> %s</td><td class="r">%s</td><td class="r">%d</td><td><a class="act" href="/media/kind?kind=%s">看目录</a></td></tr>`,
			mb.Color, mb.Label, human(sz), r.Files[mb.Key], mb.Key))
	}
	b.WriteString(`</table><div class="muted">计算于 ` + at.Format("15:04:05") + ` · <a href="/media?refresh=1">重新分类</a></div></div>`)

	// 目录 × 类型
	b.WriteString(`<div class="card"><h2>按目录拆分</h2><table><tr><th>目录</th>`)
	for _, mb := range mediaBuckets {
		if r.Totals[mb.Key] > 0 {
			b.WriteString(`<th style="text-align:right">` + mb.Label + `</th>`)
		}
	}
	b.WriteString(`<th></th></tr>`)
	for i, d := range r.Dirs {
		if i >= 40 {
			b.WriteString(fmt.Sprintf(enFormat(`<tr><td colspan="9" class="muted">… 另外 %d 个目录</td></tr>`, `<tr><td colspan="9" class="muted">… plus %d more folders</td></tr>`), len(r.Dirs)-40))
			break
		}
		b.WriteString(`<tr><td><code>` + htmlEscape(shortDir(d.Dir)) + `</code></td>`)
		for _, mb := range mediaBuckets {
			if r.Totals[mb.Key] == 0 {
				continue
			}
			v := d.Bytes[mb.Key]
			if v == 0 {
				b.WriteString(`<td class="r muted">—</td>`)
			} else {
				b.WriteString(fmt.Sprintf(`<td class="r">%s</td>`, human(v)))
			}
		}
		b.WriteString(`<td><a class="act" href="/media/dir?dir=` + urlQuery(d.Dir) + `">处理</a></td></tr>`)
	}
	b.WriteString(`</table></div>`)
	return appShell("媒体分类", "media", b.String())
}

func mediaKindPage(cfg Config, st *Store, kind string) string {
	r := mediaCached(cfg, st)
	label := kind
	for _, mb := range mediaBuckets {
		if mb.Key == kind {
			label = mb.Label
		}
	}
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">` + htmlEscape(label) + ` · 按目录</h2>`)
	if r == nil {
		b.WriteString(`<div class="warn">还没分类结果，先去<a href="/media">媒体分类</a>刷新。</div>`)
		return appShell(label, "media", b.String())
	}
	type row struct {
		Dir   string
		Bytes uint64
		Files int
	}
	var rows []row
	for _, d := range r.Dirs {
		if v := d.Bytes[kind]; v > 0 {
			rows = append(rows, row{d.Dir, v, d.Files[kind]})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Bytes > rows[j].Bytes })
	b.WriteString(`<table><tr><th>目录</th><th style="text-align:right">大小</th><th style="text-align:right">文件</th><th></th></tr>`)
	for i, x := range rows {
		if i >= 50 {
			break
		}
		b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td class="r">%d</td><td><a class="act" href="/media/dir?dir=%s">清理</a></td></tr>`,
			htmlEscape(x.Dir), human(x.Bytes), x.Files, urlQuery(x.Dir)))
	}
	b.WriteString(`</table>`)
	return appShell(label, "media", b.String())
}

func mediaDirPage(cfg Config, st *Store, dir string) string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">按类型清理</h2>`)
	b.WriteString(`<p><code>` + htmlEscape(dir) + `</code></p>`)
	b.WriteString(`<p class="muted">选择类型和"只清 N 天前"，点清理会把这些文件逐个移进隔离区（可撤销）。</p>`)
	summary := collectMediaSummary(dir, 30)
	for _, mb := range mediaBuckets {
		v := summary[mb.Key]
		if v[0] == 0 {
			continue
		}
		files, bytes := v[0], v[1]
		b.WriteString(`<div class="card" style="margin-top:12px"><h3 style="margin:0 0 6px">` + mb.Label + `</h3>`)
		b.WriteString(fmt.Sprintf(`<div class="muted">30 天前的 %s 文件：%d 个 · %s</div>`, mb.Label, files, human(bytes)))
		tid := issueTicketKind("media", dir+"|"+mb.Key+"|30")
		b.WriteString(fmt.Sprintf(`<div style="margin-top:8px"><a class="btn" href="/media/apply?n=%s">清理这批（进隔离区）</a></div>`, tid))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<a class="btn" href="/media">返回媒体分类</a>`)
	return appShell("按类型清理", "media", b.String())
}

func mediaBatchPage(id string) string {
	b := mediaBatchState(id)
	var s strings.Builder
	s.WriteString(`<div class="card"><h2>媒体清理</h2>`)
	if b.Running {
		pct := 0
		if b.Total > 0 {
			pct = b.Done * 100 / b.Total
		}
		s.WriteString(fmt.Sprintf(`<p>已处理 <b>%d / %d</b>，释放 <b>%s</b>，失败 %d</p>`, b.Done, b.Total, human(b.Freed), b.Failed))
		s.WriteString(fmt.Sprintf(`<div style="height:10px;background:#1c212b;border-radius:6px;overflow:hidden;margin:12px 0"><div style="height:100%%;width:%d%%;background:#33c179"></div></div>`, pct))
		s.WriteString(`<meta http-equiv="refresh" content="2">`)
	} else {
		s.WriteString(fmt.Sprintf(`<div class="ok">完成：%d 个文件，释放 %s，失败 %d</div>`, b.Total-b.Failed, human(b.Freed), b.Failed))
		if b.Freed > 0 {
			s.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/media/undo?id=` + id + `">撤销这批</a>`)
		}
		s.WriteString(`<a class="btn" href="/media">返回媒体分类</a>`)
	}
	s.WriteString(`</div>`)
	return appShell("媒体清理", "media", s.String())
}

func shortDir(p string) string {
	if i := strings.LastIndex(p, `\`); i > 0 {
		j := strings.LastIndex(p[:i], `\`)
		if j > 0 {
			return p[j+1:]
		}
	}
	return p
}
