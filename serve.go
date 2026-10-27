package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var scanMu sync.Mutex

// ---------- 热区兜底扫描（非管理员时用） ----------

func hotRoots() []string {
	var roots []string
	for _, k := range []string{"TEMP", "LOCALAPPDATA", "APPDATA"} {
		if v := os.Getenv(k); v != "" {
			roots = append(roots, v)
		}
	}
	for _, p := range []string{`C:\ProgramData`, `C:\Windows\Temp`} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			roots = append(roots, p)
		}
	}
	return roots
}

func hotScan() map[string]uint64 {
	out := map[string]uint64{}
	for _, root := range hotRoots() {
		baseDepth := strings.Count(root, `\`)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if info, e := d.Info(); e == nil && isReparse(info) {
					return fs.SkipDir
				}
				if strings.Count(path, `\`)-baseDepth >= 2 {
					return fs.SkipDir
				}
				return nil
			}
			info, e := d.Info()
			if e != nil || !info.Mode().IsRegular() {
				return nil
			}
			out[filepath.Dir(path)] += uint64(info.Size())
			return nil
		})
	}
	return out
}

// ---------- 实时监测 ----------

func watchLoop(cfg Config, st *Store) {
	threshold := uint64(cfg.AlertMinDeltaMB) << 20
	if isElevated() {
		for attempt := 1; attempt <= 3; attempt++ {
			tailer, err := newUsnTailer(rootVolume(cfg.Root))
			if err != nil {
				logf("USN 打开失败（%d/3）：%v", attempt, err)
				break
			}
			st.SetEngine("usn", true)
			usnActive.Store(true)
			logf("engine: USN (elevated, attempt %d)", attempt)
			if err := usnLoop(cfg, st, tailer, threshold); err == nil {
				return
			} else {
				usnActive.Store(false)
				inc(&usnRestarts)
				logf("USN 中断（%d/3）：%v", attempt, err)
				st.AddAlert("engine", fmt.Sprintf("USN 中断，第 %d 次重试：%v", attempt, err))
			}
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
		logf("USN 连续失败，降级为重扫模式")
		st.AddAlert("engine", "USN 连续失败，已降级为重扫模式（精度下降，功能不中断）")
	} else {
		logf("非管理员，使用重扫模式")
	}
	st.SetEngine("rescan", true)
	rescanLoop(cfg, st, threshold)
}

func mustVolume(root string) Volume {
	v, _ := volumeStats(root)
	return v
}

// 阈值档位变化时告警（只在进入更差档位时提醒一次）
func checkThreshold(cfg Config, st *Store, v Volume, last string) string {
	lvl := levelOf(v, cfg)
	if lvl != last && lvl != "ok" {
		msg := fmt.Sprintf("剩余空间进入 %s 档：%.1f GB", levelText(lvl), gb(v.Free))
		st.AddAlert("threshold", msg)
		notify("diskmon 空间告警", msg, true)
	}
	return lvl
}

func usnLoop(cfg Config, st *Store, tailer *UsnTailer, threshold uint64) error {
	usnErrStreak := 0
	defer tailer.Close()
	windowStart := time.Now()
	var winTotal uint64
	winDirs := map[string]uint64{}
	var winEvents []UsnEvent
	lastLevel := levelOf(mustVolume(cfg.Root), cfg)

	for {
		time.Sleep(time.Second)
		if paused.Load() {
			windowStart = time.Now()
			winTotal, winDirs, winEvents = 0, map[string]uint64{}, nil
			continue
		}
		recs, dirs, events, err := tailer.PollWithErr(8 << 20)
		if recs > 0 {
			usnSeq.Add(int64(recs))
		}
		if err != nil {
			usnErrStreak++
			if usnErrStreak >= 10 {
				return fmt.Errorf("USN 连续失败 %d 次：%v", usnErrStreak, err)
			}
			continue
		}
		usnErrStreak = 0
		_ = recs
		for d, b := range dirs {
			winDirs[d] += b
			winTotal += b
		}
		if len(events) > 0 {
			winEvents = append(winEvents, events...)
		}
		if time.Since(windowStart) < time.Duration(cfg.IntervalSec)*time.Second {
			continue
		}
		v, err := volumeStats(cfg.Root)
		if err == nil {
			st.AddSample(v.Free)
			lastLevel = checkThreshold(cfg, st, v, lastLevel)
		}
		if winTotal > threshold {
			text := growthText(winTotal, winDirs, winEvents)
			if pr := procRanks(time.Minute, 3); len(pr) > 0 {
				var ps []string
				for _, x := range pr {
					ps = append(ps, fmt.Sprintf("%s %s", x.Name, human(x.Bytes)))
				}
				text += "；写入进程：" + strings.Join(ps, "、") + "（近似，按 I/O 计数）"
			}
			noisy := isSystemNoiseDirs(winDirs)
			if noisy {
				text += "（系统自身写入，不弹窗）"
			}
			st.AddAlert("growth", text)
			if !noisy && shouldNotifyGrowth(cfg, winTotal, winDirs) {
				notify("diskmon 增长告警", text, true)
			}
		}
		windowStart = time.Now()
		winTotal, winDirs, winEvents = 0, map[string]uint64{}, nil
	}
}

func rescanLoop(cfg Config, st *Store, threshold uint64) {
	prev := hotScan()
	lastFree := uint64(0)
	lastLevel := levelOf(mustVolume(cfg.Root), cfg)
	for {
		time.Sleep(time.Duration(cfg.IntervalSec) * time.Second)
		if paused.Load() {
			continue
		}
		v, err := volumeStats(cfg.Root)
		if err != nil {
			continue
		}
		st.AddSample(v.Free)
		lastLevel = checkThreshold(cfg, st, v, lastLevel)
		if lastFree > 0 && lastFree > v.Free && lastFree-v.Free > threshold {
			cur := hotScan()
			var deltas []struct {
				path string
				diff uint64
			}
			for p, b := range cur {
				if b > prev[p] {
					deltas = append(deltas, struct {
						path string
						diff uint64
					}{p, b - prev[p]})
				}
			}
			sort.Slice(deltas, func(i, j int) bool { return deltas[i].diff > deltas[j].diff })
			parts := make([]string, 0, 3)
			for i := 0; i < len(deltas) && i < 3; i++ {
				parts = append(parts, fmt.Sprintf("%s (%s)", deltas[i].path, human(deltas[i].diff)))
			}
			text := fmt.Sprintf("增长 %s，主要来源：%s", human(lastFree-v.Free), strings.Join(parts, "；"))
			dirs := map[string]uint64{}
			for i := 0; i < len(deltas) && i < 3; i++ {
				dirs[deltas[i].path] = deltas[i].diff
			}
			noisy := isSystemNoiseDirs(dirs)
			if noisy {
				text += "（系统自身写入，不弹窗）"
			}
			st.AddAlert("growth", text)
			if !noisy && shouldNotifyGrowth(cfg, lastFree-v.Free, dirs) {
				notify("diskmon 增长告警", text, true)
			}
			prev = cur
		}
		lastFree = v.Free
	}
}

func growthText(total uint64, dirs map[string]uint64, events []UsnEvent) string {
	type kv struct {
		k string
		v uint64
	}
	var arr []kv
	for k, v := range dirs {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
	parts := make([]string, 0, 3)
	for i := 0; i < len(arr) && i < 3; i++ {
		parts = append(parts, fmt.Sprintf("%s (%s)", arr[i].k, human(arr[i].v)))
	}
	text := fmt.Sprintf("秒级增长 %s，来源：%s", human(total), strings.Join(parts, "；"))
	if len(events) > 0 {
		ev := make([]string, 0, 3)
		for i := 0; i < len(events) && i < 3; i++ {
			ev = append(ev, fmt.Sprintf("%s %s", filepath.Base(events[i].Path), human(events[i].Bytes)))
		}
		text += "（" + strings.Join(ev, "、") + "）"
	}
	return text
}

// ---------- HTTP ----------

// ---------- 隔离区：预览票据 ----------

type ticket struct {
	value string
	kind  string
	exp   time.Time
}

var (
	ticketMu sync.Mutex
	tickets  = map[string]ticket{}
)

func issueTicketKind(kind, value string) string {
	id := newID()
	ticketMu.Lock()
	tickets[id] = ticket{value: value, kind: kind, exp: time.Now().Add(5 * time.Minute)}
	ticketMu.Unlock()
	return id
}

func takeTicketKind(id string) (string, string, bool) {
	ticketMu.Lock()
	defer ticketMu.Unlock()
	t, ok := tickets[id]
	if !ok || time.Now().After(t.exp) {
		delete(tickets, id)
		return "", "", false
	}
	delete(tickets, id)
	return t.kind, t.value, true
}

func issueTicket(path string) string { return issueTicketKind("path", path) }

func takeTicket(id string) (string, bool) {
	kind, value, ok := takeTicketKind(id)
	if !ok || kind != "path" {
		return "", false
	}
	return value, true
}

func pageShell(title, body string) string {
	active := ""
	switch title {
	case "重复文件":
		active = "dup"
	case "卸载残留":
		active = "leftovers"
	case "自动清理规则", "保存规则失败", "删除规则", "规则不存在", "需要先启用", "确认执行", "试运行结果", "执行结果":
		active = "rules"
	case "一键清理演练", "隔离区已满", "一键清理结果":
		active = "clean"
	}
	return appShell(title, active, body)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func quarantineRoutes(cfg Config, st *Store, q *Quarantine, w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/q/preview":
		path := r.URL.Query().Get("path")
		reh := rehearse(cfg, path)
		var b strings.Builder
		b.WriteString("<h2>删除演练</h2><p><code>" + htmlEscape(path) + "</code></p>")
		if reh.Refuse != "" {
			b.WriteString(`<div class="warn">拒绝处理：` + htmlEscape(reh.Refuse) + `</div>`)
		} else {
			b.WriteString(fmt.Sprintf("<p>类别：%s（%s）· 文件 <b>%d</b> 个 · 合计 <b>%s</b></p>",
				cats[reh.Cat].Label, tierText[reh.Tier], reh.Files, human(reh.Bytes)))
			if len(reh.Locked) > 0 {
				b.WriteString(fmt.Sprintf(`<div class="warn">有 %d 个文件正被占用，隔离时可能失败：<br>`, len(reh.Locked)))
				for i, l := range reh.Locked {
					if i >= 5 {
						break
					}
					b.WriteString("<code>" + htmlEscape(l) + "</code><br>")
				}
				b.WriteString("</div>")
			}
			b.WriteString("<p>隔离 = 移动到隔离区，不删除；7 天内可一键还原。</p>")
			b.WriteString("<table><tr><th>文件</th><th style='text-align:right'>大小</th></tr>")
			for _, f := range reh.TopFiles {
				b.WriteString("<tr><td><code>" + htmlEscape(f.Path) + "</code></td><td style='text-align:right'>" + human(f.Bytes) + "</td></tr>")
			}
			b.WriteString("</table>")
			tid := issueTicket(path)
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/q/apply?n=` + tid + `">确认隔离（可还原）</a>`)
			b.WriteString(`<a class="btn" href="/">取消</a>`)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("删除演练", b.String())))
		return true
	case "/q/apply":
		path, ok := takeTicket(r.URL.Query().Get("n"))
		var b strings.Builder
		if !ok {
			b.WriteString(`<div class="warn">票据无效或已过期，请重新演练。</div><a class="btn" href="/">返回</a>`)
		} else {
			e, err := q.apply(cfg, path, "web")
			if err != nil {
				b.WriteString(`<div class="warn">隔离失败：` + htmlEscape(err.Error()) + `</div>`)
			} else {
				st.AddAlert("quarantine", fmt.Sprintf("已隔离 %s（%s，%d 个文件），可还原", filepath.Base(path), human(e.Bytes), e.Files))
				b.WriteString(fmt.Sprintf(`<div class="ok">已隔离 <b>%s</b>：%s，%d 个文件。7 天内可在看板里还原。<br>ID：<code>%s</code></div>`,
					htmlEscape(filepath.Base(path)), human(e.Bytes), e.Files, e.ID))
				notify("diskmon 已隔离", fmt.Sprintf("%s（%s）已移到隔离区，可还原", filepath.Base(path), human(e.Bytes)), false)
			}
			b.WriteString(`<a class="btn" href="/">返回看板</a>`)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("隔离结果", b.String())))
		return true
	case "/q/rebuild":
		rec, orph, skip := q.rebuild()
		var b strings.Builder
		b.WriteString(fmt.Sprintf(`<div class="ok">索引重建完成：恢复 <b>%d</b> 条，孤儿目录 %d，失效条目 %d</div>`, rec, orph, skip))
		b.WriteString(`<a class="btn" href="/clean">返回清理中心</a>`)
		_, _ = w.Write([]byte(appShell("重建隔离索引", "clean", b.String())))
		return true
	case "/q/restore":
		id := r.URL.Query().Get("id")
		e, err := q.restore(id)
		var b strings.Builder
		if err != nil {
			b.WriteString(`<div class="warn">还原失败：` + htmlEscape(err.Error()) + `</div>`)
		} else {
			st.AddAlert("restore", fmt.Sprintf("已还原 %s", e.Original))
			b.WriteString(`<div class="ok">已还原到 <code>` + htmlEscape(e.Original) + `</code></div>`)
		}
		b.WriteString(`<a class="btn" href="/">返回看板</a>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("还原", b.String())))
		return true
	case "/q/purge":
		n, bytes := q.purge(cfg.QuarantineDays)
		st.AddAlert("purge", fmt.Sprintf("清理隔离区 %d 项，释放 %s", n, human(bytes)))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return true
	}
	return false
}

func rulesRoutes(cfg Config, st *Store, w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/rules":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(rulesPage(getConfig(), st, r.URL.Query().Get("edit"))))
		return true
	case "/rules/save":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		_ = r.ParseForm()
		editing := r.PostForm.Get("editing")
		if err := saveRuleFromValues(r.PostForm, editing); err != nil {
			var b strings.Builder
			b.WriteString(`<div class="warn">保存失败：` + htmlEscape(err.Error()) + `</div>`)
			b.WriteString(`<a class="btn" href="/rules">返回规则</a>`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("保存规则失败", b.String())))
			return true
		}
		st.AddAlert("rule-save", "规则已保存："+r.PostForm.Get("name"))
		http.Redirect(w, r, "/rules", http.StatusSeeOther)
		return true
	case "/rules/run":
		name := r.URL.Query().Get("name")
		mode := r.URL.Query().Get("mode")
		cfgNow := getConfig()
		rule, ok := findRule(cfgNow, name)
		var b strings.Builder
		if !ok {
			b.WriteString(`<div class="warn">找不到规则：` + htmlEscape(name) + `</div>`)
			b.WriteString(`<a class="btn" href="/rules">返回规则</a>`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("规则不存在", b.String())))
			return true
		}
		if mode == "real" {
			if rule.DryRun || !rule.Enabled {
				b.WriteString(`<div class="warn">这条规则当前是「试运行」或「停用」，不能直接永久删除。<br>先点「确认启用（真实删除）」。` +
					`</div><a class="btn" href="/rules">返回规则</a>`)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(pageShell("需要先启用", b.String())))
				return true
			}
			if r.URL.Query().Get("go") != "1" {
				b.WriteString("<h2>确认永久删除？</h2>")
				b.WriteString("<p><code>" + htmlEscape(rule.Path) + "</code></p>")
				b.WriteString(fmt.Sprintf("<p>将删除该目录下超过 <b>%d</b> 天、且总量超过 <b>%d MB</b> 的文件。</p>", rule.KeepDays, rule.MaxSizeMB))
				b.WriteString(`<div class="warn">这一步不可恢复（不进隔离区）。</div>`)
				b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/rules/run?name=` + urlQuery(name) + `&mode=real&go=1">确认执行</a>`)
				b.WriteString(`<a class="btn" href="/rules">取消</a>`)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(pageShell("确认执行", b.String())))
				return true
			}
			c := rule
			c.DryRun = false
			res := runRule(cfgNow, st, c, false)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(ruleRunPage(res, true)))
			return true
		}
		res := runRule(cfgNow, st, rule, true)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(ruleRunPage(res, false)))
		return true
	case "/rules/delete":
		name := r.URL.Query().Get("name")
		if r.URL.Query().Get("go") != "1" {
			var b strings.Builder
			b.WriteString("<h2>删除规则？</h2><p><b>" + htmlEscape(name) + "</b></p>")
			b.WriteString(`<div class="warn">只删除规则本身，已经清理过的内容在隔离区里，不受影响。</div>`)
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/rules/delete?name=` + urlQuery(name) + `&go=1">确认删除</a>`)
			b.WriteString(`<a class="btn" href="/rules">取消</a>`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("删除规则", b.String())))
			return true
		}
		if err := deleteRule(name); err != nil {
			st.AddAlert("rule-delete", "删除规则失败："+err.Error())
		} else {
			st.AddAlert("rule-delete", "已删除规则："+name)
		}
		http.Redirect(w, r, "/rules", http.StatusSeeOther)
		return true
	case "/rules/toggle":
		name := r.URL.Query().Get("name")
		mode := r.URL.Query().Get("to")
		if mode == "on" && r.URL.Query().Get("go") != "1" {
			// 真实删除需要二次确认
			var b strings.Builder
			b.WriteString("<h2>确认启用真实删除？</h2>")
			b.WriteString("<p>规则：<b>" + htmlEscape(name) + "</b></p>")
			b.WriteString(`<div class="warn">启用后，每次命中阈值都会<b>永久删除</b>该目录下超过保留期的文件（不进隔离区）。</div>`)
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/rules/toggle?name=` +
				urlQuery(name) + `&to=on&go=1">确认启用</a>`)
			b.WriteString(`<a class="btn" href="/rules">取消</a>`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("确认启用", b.String())))
			return true
		}
		setRuleMode(name, mode)
		st.AddAlert("rule-mode", fmt.Sprintf("规则 %s 切换为 %s", name, mode))
		http.Redirect(w, r, "/rules", http.StatusSeeOther)
		return true
	}
	return false
}

// ---------- 应用聚合路由 ----------

func appRoutes(cfg Config, st *Store, q *Quarantine, w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/app":
		name := r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(appPage(cfg, st, name)))
		return true
	case "/app/preview":
		name := r.URL.Query().Get("name")
		var total uint64
		var n int
		for _, g := range appGroups(cfg, st) {
			if g.Name == name {
				total = g.Bytes
				n = len(g.Dirs)
			}
		}
		var b strings.Builder
		b.WriteString("<h2>整组隔离演练：" + htmlEscape(name) + "</h2>")
		if n == 0 {
			b.WriteString(`<div class="warn">没有可隔离的目录</div>`)
		} else {
			b.WriteString(fmt.Sprintf("<p>共 <b>%d</b> 个目录，合计 <b>%s</b>。全部移动到隔离区，7 天内可逐个还原。</p>",
				n, human(total)))
			b.WriteString(`<div class="warn">隔离后该软件可能需要重新登录或重新下载缓存，首次启动会变慢。建议先看下面的逐项列表再决定。</div>`)
			tid := issueTicketKind("app", name)
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/app/apply?n=` + tid + `">确认隔离全部</a>`)
		}
		b.WriteString(`<a class="btn" href="/app?name=` + url.QueryEscape(name) + `">返回明细</a>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("整组隔离演练", b.String())))
		return true
	case "/app/apply":
		kind, value, ok := takeTicketKind(r.URL.Query().Get("n"))
		var b strings.Builder
		if !ok || kind != "app" {
			b.WriteString(`<div class="warn">票据无效或已过期，请重新演练。</div>`)
		} else {
			n, freed, failures := appApply(cfg, st, q, value)
			st.AddAlert("app-quarantine", fmt.Sprintf("已隔离 %s 的 %d 个目录，共 %s", value, n, human(freed)))
			b.WriteString(fmt.Sprintf(`<div class="ok">已隔离 <b>%s</b> 的 <b>%d</b> 个目录，释放 <b>%s</b>（可逐个还原）</div>`,
				htmlEscape(value), n, human(freed)))
			if len(failures) > 0 {
				b.WriteString(fmt.Sprintf(`<div class="warn">%d 个失败：<br>`, len(failures)))
				for i, f := range failures {
					if i >= 5 {
						break
					}
					b.WriteString("<code>" + htmlEscape(f) + "</code><br>")
				}
				b.WriteString("</div>")
			}
			notify("diskmon 已隔离", fmt.Sprintf("%s：%d 个目录，%s", value, n, human(freed)), false)
		}
		b.WriteString(`<a class="btn" href="/">返回看板</a>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("整组隔离结果", b.String())))
		return true
	}
	return false
}

func serveHTTP(cfg Config, st *Store) {
	q := openQuarantine(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		inc(&httpReqs)
		uiTheme.Store(themeFromRequest(r))
		cfg = getConfig() // 每次请求都取最新配置，否则改设置后页面仍用启动时的快照
		defer func() {
			if rec := recover(); rec != nil {
				inc(&httpPanics)
				logf("HTTP panic: %v", rec)
			}
		}()
		if r.URL.Path == "/api/health" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(healthSnapshot(cfg, st).JSON()))
			return
		}
		if r.URL.Path == "/tool" {
			name := r.URL.Query().Get("name")
			if err := launchTool(name); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if quarantineRoutes(cfg, st, q, w, r) {
			return
		}
		if appRoutes(cfg, st, q, w, r) {
			return
		}
		if rulesRoutes(cfg, st, w, r) {
			return
		}
		if planRoutes(cfg, st, q, w, r) {
			return
		}
		if dupLeftRoutes(cfg, st, q, w, r) {
			return
		}
		if r.URL.Path == "/scan" {
			startScanAsync(cfg, st)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(scanPageHTML()))
			return
		}
		if r.URL.Path == "/scan/cancel" {
			cancelScan()
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/api/scan/progress" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			b, _ := json.Marshal(scanProgressNow())
			_, _ = w.Write(b)
			return
		}
		if r.URL.Path == "/api/status" {
			v, _ := volumeStats(cfg.Root)
			eng := st.Engine()
			payload := map[string]interface{}{
				"free_gb":        gb(v.Free),
				"used_gb":        gb(v.Used),
				"total_gb":       gb(v.Total),
				"free_pct":       float64(v.Free) / float64(v.Total) * 100,
				"level":          levelOf(v, cfg),
				"engine":         eng.Engine,
				"engine_running": eng.Running,
				"last_alert":     st.LastAlert(),
				"muted":          muted(),
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		if r.URL.Path == "/favicon.ico" {
			if b, err := os.ReadFile(filepath.Join(exeDir(), "icon.ico")); err == nil {
				w.Header().Set("Content-Type", "image/x-icon")
				_, _ = w.Write(b)
				return
			}
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/detail/") {
			key := strings.TrimPrefix(r.URL.Path, "/detail/")
			if page, ok := sectionPage(cfg, st, key); ok {
				_, _ = w.Write([]byte(page))
			} else {
				w.WriteHeader(404)
				_, _ = w.Write([]byte("未知分区：" + htmlEscape(key) + "　<a href=\"/detail\">返回</a>"))
			}
			return
		}
		switch r.URL.Path {
		case "/detail":
			_, _ = w.Write([]byte(renderPage(cfg, st)))
		case "/mute":
			switch r.URL.Query().Get("mode") {
			case "1h":
				setMute(time.Hour)
			case "forever":
				muteForever()
			default:
				clearMute()
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		case "/theme":
			mode := r.URL.Query().Get("mode")
			if mode != "light" {
				mode = "dark"
			}
			http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: mode, Path: "/", MaxAge: 86400 * 365})
			http.Redirect(w, r, safeBack(r.URL.Query().Get("back")), http.StatusSeeOther)
			return
		case "/export":
			if r.URL.Query().Get("what") == "" {
				_, _ = w.Write([]byte(exportPage(cfg)))
				return
			}
			handleExport(cfg, st, q, w, r)
			return
		case "/inspect":
			_, _ = w.Write([]byte(inspectPage(cfg, st, r.URL.Query().Get("path"))))
		case "/settings":
			_, _ = w.Write([]byte(settingsPage(getConfig(), r.URL.Query().Get("notice"))))
		case "/settings/save":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_ = r.ParseForm()
			notice, err := saveSettingsFromForm(r.PostForm)
			if err != nil {
				var b strings.Builder
				b.WriteString(`<div class="warn">保存失败：` + htmlEscape(err.Error()) + `</div>`)
				b.WriteString(`<a class="btn" href="/settings">返回设置</a>`)
				_, _ = w.Write([]byte(appShell("保存设置失败", "settings", b.String())))
				return
			}
			st.AddAlert("settings", "设置已保存")
			if notice == "" {
				notice = "设置已保存"
			}
			http.Redirect(w, r, "/settings?notice="+urlQuery(notice), http.StatusSeeOther)
			return
		case "/settings/reset":
			if r.URL.Query().Get("go") != "1" {
				var b strings.Builder
				b.WriteString(`<h2>恢复默认设置？</h2><p class="muted">会重置监测参数、阈值、白名单、隔离区设置；<b>规则和已有数据不受影响</b>。</p>`)
				b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/settings/reset?go=1">确认恢复</a>`)
				b.WriteString(`<a class="btn" href="/settings">取消</a>`)
				_, _ = w.Write([]byte(appShell("恢复默认", "settings", b.String())))
				return
			}
			cfgMu.Lock()
			cur := liveConfig
			cfgMu.Unlock()
			d := defaultConfig()
			d.DataDir = cur.DataDir
			d.Rules = cur.Rules
			cfgMu.Lock()
			liveConfig = d
			cfgMu.Unlock()
			saveConfig(d)
			st.AddAlert("settings", "已恢复默认设置")
			http.Redirect(w, r, "/settings?notice="+urlQuery("已恢复默认设置"), http.StatusSeeOther)
			return
		case "/clean":
			_, _ = w.Write([]byte(cleanPage(cfg, st)))
		case "/media":
			if r.URL.Query().Get("refresh") == "1" {
				mediaInvalidate()
				mediaCached(cfg, st)
				http.Redirect(w, r, "/media", http.StatusSeeOther)
				return
			}
			_, _ = w.Write([]byte(mediaPage(cfg, st)))
		case "/media/kind":
			_, _ = w.Write([]byte(mediaKindPage(cfg, st, r.URL.Query().Get("kind"))))
		case "/media/dir":
			dir := r.URL.Query().Get("dir")
			allowed := false
			if mr := mediaCached(cfg, st); mr != nil {
				for _, d := range mr.Dirs {
					if strings.EqualFold(d.Dir, dir) {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				_, _ = w.Write([]byte(appShell("媒体清理", "media",
					`<div class="warn">该目录不在媒体分类结果里，请先从 <a href="/media">媒体分类</a> 进入。</div>`)))
				return
			}
			_, _ = w.Write([]byte(mediaDirPage(cfg, st, dir)))
		case "/media/apply":
			kind, val, ok := takeTicketKind(r.URL.Query().Get("n"))
			if !ok || kind != "media" {
				_, _ = w.Write([]byte(appShell("媒体清理", "media", `<div class="warn">票据无效或已过期，请重新进入目录页。</div>`)))
				return
			}
			parts := strings.SplitN(val, "|", 3)
			if len(parts) != 3 {
				_, _ = w.Write([]byte(appShell("媒体清理", "media", `<div class="warn">参数不完整。</div>`)))
				return
			}
			days := 30
			fmt.Sscanf(parts[2], "%d", &days)
			q := openQuarantine(cfg)
			id := startMediaClean(cfg, st, q, parts[0], parts[1], days)
			if id == "" {
				_, _ = w.Write([]byte(appShell("媒体清理", "media", `<div class="ok">没有符合条件的文件。</div><a class="btn" href="/media">返回</a>`)))
				return
			}
			http.Redirect(w, r, "/media/batch?id="+id, http.StatusSeeOther)
			return
		case "/media/batch":
			_, _ = w.Write([]byte(mediaBatchPage(r.URL.Query().Get("id"))))
		case "/media/undo":
			q := openQuarantine(cfg)
			n, freed := undoMediaBatch(q, st, r.URL.Query().Get("id"))
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<div class="ok">已撤销：还原 %d 个文件，%s</div>`, n, human(freed)))
			b.WriteString(`<a class="btn" href="/media">返回媒体分类</a>`)
			_, _ = w.Write([]byte(appShell("撤销媒体清理", "media", b.String())))
		case "/lazy":
			_, _ = w.Write([]byte(lazyPage(cfg, st)))
		case "/lazy/start":
			q := openQuarantine(cfg)
			ok := startLazy(cfg, st, q)
			_, _ = w.Write([]byte(lazyResultPage(cfg, st, ok)))
			return
		case "/lazy/undo":
			q := openQuarantine(cfg)
			n, freed := lazyUndo(cfg, q, st)
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<div class="ok">已撤销：还原 <b>%d</b> 个目录，<b>%s</b></div>`, n, human(freed)))
			b.WriteString(`<a class="btn" href="/lazy">返回懒人清理</a><a class="btn" href="/">回总览</a>`)
			_, _ = w.Write([]byte(appShell("撤销清理", "clean", b.String())))
			return
		case "/trend":
			_, _ = w.Write([]byte(trendPage(cfg, st)))
		default:
			_, _ = w.Write([]byte(overviewPage(cfg, st)))
		}
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", cfg.Port), Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		logf("http: %v", err)
	}
}

func startScanAsync(cfg Config, st *Store) {
	if !scanMu.TryLock() {
		return
	}
	go func() {
		defer scanMu.Unlock()
		runScan(cfg, st)
	}()
}

// ---------- 工具 ----------

func createAndFill(path string, size int64) (*os.File, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1<<20)
	for written := int64(0); written < size; {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		if _, err := f.Write(buf[:n]); err != nil {
			f.Close()
			return nil, err
		}
		written += n
	}
	_ = f.Close()
	return f, nil
}

func removeFile(p string) error { return os.Remove(p) }

func writeReport(cfg Config, st *Store) string {
	p := filepath.Join(cfg.DataDir, "report.html")
	_ = os.WriteFile(p, []byte(renderPage(cfg, st)), 0o644)
	return p
}

func openDBDir(cfg Config) {
	cmd := exec.Command("explorer.exe", cfg.DataDir)
	_ = cmd.Start()
}

func planRoutes(cfg Config, st *Store, q *Quarantine, w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/plan/preview":
		key := r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(planPreviewPage(cfg, st, q, key)))
		return true
	case "/plan/apply":
		kind, key, ok := takeTicketKind(r.URL.Query().Get("n"))
		var b strings.Builder
		if !ok || kind != "plan" {
			b.WriteString(`<div class="warn">票据无效或已过期，请重新演练。</div>`)
		} else {
			n, freed, fails := planApply(cfg, st, q, key)
			label := key
			if i, ok := planByKey(key); ok {
				label = planDefs[i].Label
			}
			if n > 0 {
				st.AddAlert("plan-run", fmt.Sprintf("一键清理[%s]：隔离 %d 个目录，%s", label, n, human(freed)))
				notify("diskmon 清理完成", fmt.Sprintf("%s方案：%d 个目录，%s", label, n, human(freed)), false)
			}
			b.WriteString(fmt.Sprintf(`<div class="ok">已隔离 <b>%d</b> 个目录，释放 <b>%s</b>（可在看板还原）</div>`, n, human(freed)))
			if len(fails) > 0 {
				b.WriteString(fmt.Sprintf(`<div class="warn">%d 个失败：<br>`, len(fails)))
				for i, f := range fails {
					if i >= 6 {
						break
					}
					b.WriteString("<code>" + htmlEscape(f) + "</code><br>")
				}
				b.WriteString("</div>")
			}
		}
		b.WriteString(`<a class="btn" href="/">返回看板</a>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("一键清理结果", b.String())))
		return true
	}
	return false
}
