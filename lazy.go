package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- 懒人清理：一个按钮，跑一轮最安全的清理 ----------
//
// 只做「保守方案」：clean 层、≥100MB、白名单/黑名单之外。
// 全部进隔离区（可撤销），不会永久删除任何东西。
//
// 两件容易被误解的事，在这里如实处理：
//  1. 「计划」不是凭空编的数。扫描结果可能是几小时前的，所以开始清理前会按当前
//     大小重新量一遍；清理过程中这些目录还会继续长（临时文件/缓存），所以结果页
//     逐项给出计划、实际和差异，并说明差异来源。
//  2. 清理可以暂停 / 继续 / 停止。暂停会停在下一个文件之前；停止会等当前文件
//     收尾，已经移走的部分照常进隔离区、照常可撤销。

type lazyDirResult struct {
	Path    string
	Planned uint64
	Actual  uint64
	Files   int
	Skipped int
	Err     string
}

type lazyRun struct {
	Running      bool
	Paused       bool
	Stopping     bool // 已请求停止，正在等当前文件/目录收尾
	Stopped      bool // 本轮是被用户停止的
	Measuring    bool // 正在重新测量计划大小
	Started      time.Time
	Finished     time.Time
	Total        int
	Done         int
	Remaining    int
	Planned      uint64 // 开始时的实测计划（个别目录超时则沿用扫描快照）
	Snapshot     uint64 // 上次扫描快照的计划，仅用于解释差异
	PlanPartial  bool   // 计划里是否有目录沿用了旧快照
	Freed        uint64
	Files        int
	SkippedFiles int
	Current      string
	Failed       int
	IDs          []string
	Results      []lazyDirResult
	Err          string
}

var (
	lazyMu   sync.Mutex
	lazyCond = sync.NewCond(&lazyMu)
	lazyCur  = lazyRun{}
)

func lazyState() lazyRun {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return lazyCur
}

// lazyGate 是清理线程的闸门：暂停时在这里等待，要求停止时返回 false。
// 返回 true 表示可以继续移动文件。
func lazyGate() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	for lazyCur.Running && lazyCur.Paused && !lazyCur.Stopping {
		lazyCond.Wait()
	}
	return !lazyCur.Stopping
}

// lazyPause 请求暂停；已经在暂停/停止中则返回 false。
func lazyPause() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	if !lazyCur.Running || lazyCur.Paused || lazyCur.Stopping || lazyCur.Measuring {
		return false
	}
	lazyCur.Paused = true
	logf("lazy: paused (done=%d/%d freed=%d)", lazyCur.Done, lazyCur.Total, lazyCur.Freed)
	return true
}

// lazyResume 继续被暂停的清理。
func lazyResume() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	if !lazyCur.Running || !lazyCur.Paused {
		return false
	}
	lazyCur.Paused = false
	logf("lazy: resumed")
	lazyCond.Broadcast()
	return true
}

// lazyStop 请求停止；当前文件移动完成后停下，剩余目录不再处理。
func lazyStop() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	if !lazyCur.Running || lazyCur.Stopping {
		return false
	}
	lazyCur.Stopping = true
	lazyCur.Paused = false
	logf("lazy: stop requested (done=%d/%d freed=%d)", lazyCur.Done, lazyCur.Total, lazyCur.Freed)
	lazyCond.Broadcast() // 暂停中的线程也要被叫醒，看到停止标志
	return true
}

func lazyStopping() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return lazyCur.Stopping
}

func startLazy(cfg Config, st *Store, q *Quarantine) bool {
	// 先占位，避免两个人几乎同时点「开始清理」时各跑一轮。
	lazyMu.Lock()
	if lazyCur.Running {
		lazyMu.Unlock()
		return false
	}
	lazyCur = lazyRun{Running: true, Measuring: true, Started: time.Now(), Current: "正在按当前大小重新测量可清目录…"}
	lazyMu.Unlock()

	reset := func(r lazyRun) {
		lazyMu.Lock()
		lazyCur = r
		lazyMu.Unlock()
	}

	p := buildPlan(cfg, st, "conservative")
	if len(p.Dirs) == 0 {
		reset(lazyRun{})
		return false
	}
	dirs, measured := measureDirs(p.Dirs, 4*time.Second)
	if measured {
		kept := dirs[:0]
		for _, d := range dirs {
			if d.Bytes > 0 {
				kept = append(kept, d)
			}
		}
		dirs = kept
	}
	if len(dirs) == 0 {
		reset(lazyRun{})
		return false
	}
	var planned uint64
	for _, d := range dirs {
		planned += d.Bytes
	}
	if planned == 0 {
		reset(lazyRun{})
		return false
	}
	qUsed := q.totalBytes() // 先取隔离区占用，避免在 lazyMu 里再去拿 q.mu
	if cfg.QuarantineMaxGB > 0 && qUsed+planned > uint64(cfg.QuarantineMaxGB)<<30 {
		reset(lazyRun{Err: fmt.Sprintf("计划清理 %s，会超过隔离区上限 %d GB（当前 %s）。先还原或清理隔离区，或把保留天数调小。",
			human(planned), cfg.QuarantineMaxGB, human(qUsed))})
		return false
	}

	started := time.Now()
	lazyMu.Lock()
	if lazyCur.Stopping { // 测量期间用户点了停止：直接结束，不开始搬运
		lazyCur = lazyRun{}
		lazyMu.Unlock()
		return false
	}
	lazyCur = lazyRun{Running: true, Started: started, Total: len(dirs), Planned: planned,
		Snapshot: p.Bytes, PlanPartial: !measured}
	lazyMu.Unlock()

	go runLazy(cfg, st, q, dirs, planned)
	return true
}

func runLazy(cfg Config, st *Store, q *Quarantine, dirs []DirEntry, planned uint64) {
	var freed uint64
	var totalFiles, totalSkipped, failed int
	ids := make([]string, 0, len(dirs))
	results := make([]lazyDirResult, 0, len(dirs))
	stopped := false

	for i, d := range dirs {
		if !lazyGate() {
			stopped = true
			break
		}
		logf("lazy: dir %d/%d start %s", i+1, len(dirs), d.Path)
		baseFreed, baseFiles, baseSkipped := lazyProgressBase()
		e, err := q.applyWithProgress(cfg, d.Path, "lazy", func(m uint64, f, s int, cur string) {
			lazyMu.Lock()
			lazyCur.Freed = baseFreed + m
			lazyCur.Files = baseFiles + f
			lazyCur.SkippedFiles = baseSkipped + s
			lazyCur.Current = cur
			lazyMu.Unlock()
		}, lazyGate)

		res := lazyDirResult{Path: d.Path, Planned: d.Bytes}
		if e.ID != "" {
			freed += e.Bytes
			totalFiles += e.Files
			totalSkipped += e.Skipped
			ids = append(ids, e.ID)
			res.Actual = e.Bytes
			res.Files = e.Files
			res.Skipped = e.Skipped
		}
		if err != nil && !errors.Is(err, errStopped) {
			failed++
			res.Err = err.Error()
		}

		lazyMu.Lock()
		if !errors.Is(err, errStopped) || e.ID != "" {
			lazyCur.Done = i + 1
		}
		lazyCur.Failed = failed
		lazyCur.Freed = freed
		lazyCur.Files = totalFiles
		lazyCur.SkippedFiles = totalSkipped
		results = append(results, res)
		lazyCur.Results = append([]lazyDirResult(nil), results...)
		if e.ID == "" && err == nil {
			lazyCur.Current = ""
		}
		lazyMu.Unlock()

		if errors.Is(err, errStopped) || lazyStopping() {
			stopped = true
			break
		}
	}

	lazyMu.Lock()
	remaining := lazyCur.Total - lazyCur.Done
	if remaining < 0 {
		remaining = 0
	}
	lazyCur.Running = false
	lazyCur.Measuring = false
	lazyCur.Paused = false
	lazyCur.Stopping = false
	lazyCur.Stopped = stopped
	lazyCur.Remaining = remaining
	lazyCur.Freed = freed
	lazyCur.Files = totalFiles
	lazyCur.SkippedFiles = totalSkipped
	lazyCur.Failed = failed
	lazyCur.IDs = ids
	lazyCur.Finished = time.Now()
	lazyCur.Current = ""
	lazyMu.Unlock()

	skipText := ""
	if totalSkipped > 0 {
		skipText = fmt.Sprintf("，跳过 %d 个占用中的文件", totalSkipped)
	}
	stopText := ""
	if stopped {
		stopText = fmt.Sprintf("；已手动停止，剩余 %d 个目录未处理", remaining)
	}
	if len(ids) > 0 || stopped {
		msg := fmt.Sprintf("懒人清理：隔离 %d 个目录，计划 %s，实际 %s%s%s",
			len(ids), human(planned), human(freed), skipText, stopText)
		st.AddAlert("lazy-clean", msg)
		if len(ids) > 0 {
			notify("diskmon 懒人清理完成", fmt.Sprintf("实际移入隔离区 %s（计划 %s，占用中的文件已跳过）", human(freed), human(planned)), false)
		}
	}
}

func lazyUndo(cfg Config, q *Quarantine, st *Store) (int, uint64) {
	lazyMu.Lock()
	ids := append([]string(nil), lazyCur.IDs...)
	lazyCur.IDs = nil
	lazyMu.Unlock()
	var n int
	var bytes uint64
	for _, id := range ids {
		e, err := q.restore(id)
		if err != nil {
			continue
		}
		n++
		bytes += e.Bytes
	}
	if n > 0 {
		st.AddAlert("lazy-undo", fmt.Sprintf("懒人清理已撤销：还原 %d 个目录，%s", n, human(bytes)))
	}
	return n, bytes
}

func lazyPreview(cfg Config, st *Store) Plan {
	return buildPlan(cfg, st, "conservative")
}

// measureDirs 在开始清理前按当前大小重新量一遍计划目录。
// 扫描快照可能是几小时前的，临时文件/缓存一直在长，直接拿旧数字当「计划」，
// 结果就必然对不上账。预算内量不完的目录沿用扫描值，并在页面上标注。
func measureDirs(dirs []DirEntry, budget time.Duration) ([]DirEntry, bool) {
	deadline := time.Now().Add(budget)
	out := make([]DirEntry, len(dirs))
	copy(out, dirs)
	complete := true
	for i := range out {
		sz, ok := measureDirSize(out[i].Path, deadline)
		if !ok {
			complete = false
			break
		}
		out[i].Bytes = sz
	}
	return out, complete
}

func measureDirSize(root string, deadline time.Time) (uint64, bool) {
	var total uint64
	ok := true
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if time.Now().After(deadline) {
			ok = false
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if info, e := d.Info(); e == nil && isReparse(info) {
				return filepath.SkipDir
			}
			return nil
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() {
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	if !ok {
		return 0, false
	}
	return total, true
}

// lazyDeltaText 用一句话解释「计划 vs 实际」的差异来源，而不是只给两个数字。
func lazyDeltaText(s lazyRun) string {
	partial := ""
	if s.PlanPartial {
		partial = "计划里部分目录沿用了旧扫描（重新测量超时），差异中有一部分是快照误差。"
	}
	switch {
	case s.Freed > s.Planned:
		more := fmt.Sprintf("实际比计划多 %s：计划是开始清理时实测的，这些目录在清理过程中仍在写入新文件（临时文件/缓存一直在长），多出来的部分也一并进了隔离区，可撤销。",
			human(s.Freed-s.Planned))
		if s.Stopped {
			more += fmt.Sprintf("本轮是手动停止的，剩余 %d 个目录没有处理。", s.Remaining)
		}
		return more + partial
	case s.Freed < s.Planned:
		if s.Stopped {
			return fmt.Sprintf("实际比计划少 %s：本轮是手动停止的，剩余 %d 个目录没有处理——不是失败，已移走的都在隔离区里，可撤销。%s",
				human(s.Planned-s.Freed), s.Remaining, partial)
		}
		extra := ""
		if s.Failed > 0 {
			extra = fmt.Sprintf("，另有 %d 个目录移动失败", s.Failed)
		}
		return fmt.Sprintf("实际比计划少 %s：%d 个文件正被程序占用、没能移走%s。关掉相关程序后再清一次，通常能补上。%s",
			human(s.Planned-s.Freed), s.SkippedFiles, extra, partial)
	}
	if partial != "" {
		return "实际与计划一致。" + partial
	}
	return "实际与计划一致。"
}

// resultsTable 展示"计划 vs 实际"，逐项解释差异
func lazyResultsTable(results []lazyDirResult, stopped bool) string {
	var b strings.Builder
	b.WriteString(`<table><tr><th>目录</th><th style="text-align:right">计划</th><th style="text-align:right">实际</th><th style="text-align:right">差异</th><th>结果</th></tr>`)
	shown := 0
	for _, r := range results {
		if shown >= 8 {
			b.WriteString(fmt.Sprintf(`<tr><td colspan="5" class="muted">… 还有 %d 个</td></tr>`, len(results)-shown))
			break
		}
		shown++
		diff := int64(r.Actual) - int64(r.Planned)
		diffText := "—"
		switch {
		case diff > 0:
			diffText = "+" + human(uint64(diff))
		case diff < 0:
			diffText = "−" + human(uint64(-diff))
		}
		status := "已隔离"
		switch {
		case r.Err != "":
			status = `跳过：` + htmlEscape(truncate(r.Err, 40))
		case r.Skipped > 0:
			status = fmt.Sprintf("已隔离，跳过 %d 个占用中文件", r.Skipped)
		case r.Actual == 0 && r.Planned > 0:
			status = "未移动（停止或目录已空）"
		case stopped && r.Actual < r.Planned:
			status = "已隔离（停止时未搬完）"
		case r.Actual < r.Planned*9/10:
			status = "已隔离（部分文件被占用）"
		}
		b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td class="r">%s</td><td class="r">%s</td><td class="muted">%s</td></tr>`,
			htmlEscape(r.Path), human(r.Planned), human(r.Actual), diffText, status))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// lazyRunningButtons 是进度卡片里的暂停/继续/停止按钮。
func lazyRunningButtons(s lazyRun) string {
	if s.Stopping {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div style="margin-top:14px;display:flex;gap:8px;flex-wrap:wrap">`)
	if s.Paused {
		b.WriteString(`<a class="btn" style="background:#1f6f4a;border-color:#33c179" href="/lazy/resume">继续清理</a>`)
	} else {
		b.WriteString(`<a class="btn" href="/lazy/pause">暂停</a>`)
	}
	b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/lazy/stop">停止清理</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

// 懒人页
func lazyPage(cfg Config, st *Store) string {
	s := lazyState()
	p := lazyPreview(cfg, st)
	var b strings.Builder

	if s.Running {
		pct := 0
		if s.Planned > 0 {
			pct = int(float64(s.Freed) / float64(s.Planned) * 100)
		}
		if pct > 100 {
			pct = 100
		}
		head := "正在清理…"
		switch {
		case s.Stopping:
			head = "正在停止…"
		case s.Paused:
			head = "已暂停"
		case s.Measuring:
			head = "正在重新测量…"
		}
		b.WriteString(`<div class="card"><h2>` + head + `</h2>`)
		if s.Measuring {
			b.WriteString(`<p class="muted">先按当前大小重新量一遍这些目录（上次扫描的数字可能已经过时），马上开始。页面每 2 秒刷新。</p>`)
			b.WriteString(`</div><meta http-equiv="refresh" content="2">`)
			return appShell("懒人清理", "clean", b.String())
		}
		switch {
		case s.Stopping:
			b.WriteString(`<div class="warn" style="margin-top:6px">正在停止：当前文件收尾后结束本轮，剩余目录不再处理。已经移走的内容都在隔离区里，可以撤销。</div>`)
		case s.Paused:
			b.WriteString(fmt.Sprintf(`<div class="warn" style="margin-top:6px">已暂停：进度停在这里，不会继续移动文件。点「继续清理」接着做，或点「停止清理」结束本轮（已移走的仍可撤销）。</div>`))
		}
		b.WriteString(fmt.Sprintf(`<p>已移入隔离区 <b>%s</b> / 计划 <b>%s</b> · %d 个文件 · 跳过 %d 个 · 目录 %d/%d</p>`,
			human(s.Freed), human(s.Planned), s.Files, s.SkippedFiles, s.Done, s.Total))
		barColor := "#33c179"
		if s.Paused {
			barColor = "#f5a524"
		}
		b.WriteString(fmt.Sprintf(`<div style="height:10px;background:#1c212b;border-radius:6px;overflow:hidden;margin:12px 0"><div style="height:100%%;width:%d%%;background:%s;transition:width .4s"></div></div>`,
			pct, barColor))
		if s.Freed > s.Planned && s.Planned > 0 {
			b.WriteString(fmt.Sprintf(`<div class="muted">已经超过开始时实测的计划 %s：这些目录在清理过程中还在写入新文件（临时文件/缓存一直在长），多出来的部分也一并进了隔离区。</div>`, human(s.Freed-s.Planned)))
		}
		if s.Current != "" {
			b.WriteString(`<div class="muted" style="font-family:ui-monospace,Consolas,monospace;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">当前文件：` + htmlEscape(s.Current) + `</div>`)
		}
		b.WriteString(lazyRunningButtons(s))
		if !s.Stopping {
			b.WriteString(`<div class="muted" style="margin-top:8px">「暂停」只是停住，随时可以继续；「停止清理」会结束本轮，已移走的仍在隔离区、可撤销。</div>`)
		}
		b.WriteString(`<div class="muted" style="margin-top:6px">页面每秒刷新；关闭也没关系，清理会在后台继续。</div>`)
		b.WriteString(`</div>`)
		if s.Stopping {
			// 停止是很快的：轮询到本轮结束就立刻切到结果页，不用等整页刷新
			b.WriteString(`<script>setTimeout(function(){fetch('/api/actions').then(function(r){return r.json()}).then(function(a){if(!a.lazy||!a.lazy.running){location.reload()}}).catch(function(){})},600)</script>`)
		}
		b.WriteString(`<meta http-equiv="refresh" content="1">`)
		return appShell("懒人清理", "clean", b.String())
	}

	b.WriteString(`<div class="card"><h2>懒人清理</h2>`)
	if s.Err != "" {
		b.WriteString(`<div class="warn">` + htmlEscape(s.Err) + `</div>`)
	}
	b.WriteString(`<p class="muted">只清「可清」类（临时文件、开发缓存、更新缓存、日志）；全部进隔离区，7 天内可撤销。不会永久删除。</p>`)
	if len(p.Dirs) == 0 {
		if st.GetScan() == nil {
			b.WriteString(`<div class="warn">还没有扫描结果。先扫一次，才能知道哪些目录可以清。</div>`)
			b.WriteString(`<a class="btn" href="/scan">开始扫描</a>`)
		} else {
			b.WriteString(`<div class="ok">当前没有值得清理的目录（都很小，或已被排除）。</div>`)
			b.WriteString(`<a class="btn" href="/">返回总览</a>`)
		}
		b.WriteString(`</div>`)
		return appShell("懒人清理", "clean", b.String())
	}
	b.WriteString(fmt.Sprintf(`<div class="kpi"><div><span class="big">%s</span><span class="unit"> 预计释放 · %d 个目录</span></div></div>`,
		human(p.Bytes), len(p.Dirs)))
	if p.ScanTS > 0 {
		b.WriteString(fmt.Sprintf(`<div class="muted" style="margin:-4px 0 10px">以上是 %s 扫描时的数值，不是实时值；点开始后会先按当前大小重新量一遍。正在被占用的文件会被跳过——完成后会逐项对账「计划 / 实际 / 差异」。</div>`,
			time.Unix(p.ScanTS, 0).Format("01-02 15:04")))
	} else {
		b.WriteString(`<div class="muted" style="margin:-4px 0 10px">以上是上次扫描的数值，不是实时值；点开始后会先按当前大小重新量一遍。正在被占用的文件会被跳过——完成后会逐项对账「计划 / 实际 / 差异」。</div>`)
	}
	b.WriteString(`<table><tr><th>目录</th><th style="text-align:right">大小</th><th>占用情况（抽样）</th></tr>`)
	highLock := 0
	for i, d := range p.Dirs {
		if i >= 12 {
			b.WriteString(fmt.Sprintf(enFormat(`<tr><td colspan="3" class="muted">… 另外 %d 个</td></tr>`, `<tr><td colspan="3" class="muted">… plus %d more</td></tr>`), len(p.Dirs)-12))
			break
		}
		locked, total := sampleLocked(d.Path)
		occ := ""
		if total > 0 {
			pct := locked * 100 / total
			occ = fmt.Sprintf("%d%%（%d/%d）", pct, locked, total)
			if pct >= 50 {
				occ = `<span style="color:#f5a524">` + occ + `</span>`
				highLock++
			}
		} else {
			occ = "—"
		}
		b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td class="muted">%s</td></tr>`, htmlEscape(d.Path), human(d.Bytes), occ))
	}
	b.WriteString(`</table>`)
	if highLock > 0 {
		b.WriteString(`<div class="warn" style="margin-top:10px">有 ` + fmt.Sprintf("%d", highLock) + ` 个目录里大部分文件正被程序占用，清理时会被跳过——那部分空间这次腾不出来。关掉正在用的程序（浏览器、聊天工具等）后再清理，效果会明显更好。</div>`)
	}
	b.WriteString(`<div style="margin-top:16px"><a class="btn" style="background:#1f6f4a;border-color:#33c179;font-size:15px;padding:11px 22px" href="/lazy/start">开始清理（可撤销）</a>`)
	b.WriteString(`<a class="btn" href="/clean">看其它清理方式</a></div>`)
	b.WriteString(`</div>`)

	// 上一次结果：计划 vs 实际
	if !s.Finished.IsZero() {
		head := "上一次清理"
		if s.Stopped {
			head = "上一次清理（手动停止）"
		}
		b.WriteString(`<div class="card" style="margin-top:14px"><h2>` + head + `</h2>`)
		b.WriteString(fmt.Sprintf(`<div class="kpi" style="margin-top:0"><div><span class="big">%s</span><span class="unit"> 实际移入隔离区 · 计划 %s</span></div></div>`,
			human(s.Freed), human(s.Planned)))
		b.WriteString(`<p class="muted" style="margin:4px 0 8px">` + lazyDeltaText(s) + `</p>`)
		if s.Stopped {
			b.WriteString(fmt.Sprintf(`<div class="muted">已处理 %d/%d 个目录，剩余 %d 个目录没有动。停止不是失败，已移走的都在隔离区里。</div>`,
				s.Done, s.Total, s.Remaining))
		}
		if len(s.Results) > 0 {
			b.WriteString(`<details style="margin-top:8px"><summary class="muted" style="cursor:pointer">看计划 / 实际明细</summary>`)
			b.WriteString(lazyResultsTable(s.Results, s.Stopped))
			b.WriteString(`</details>`)
		}
		if s.SkippedFiles > 0 {
			b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:6px">跳过的 %d 个文件是因为它们正被程序使用（占用中无法移动）。关闭相关程序后重新清理，通常能移走更多。</div>`, s.SkippedFiles))
		}
		if len(s.IDs) > 0 {
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/lazy/undo">一键撤销上一次清理</a>`)
		}
		b.WriteString(`</div>`)
	}
	return appShell("懒人清理", "clean", b.String())
}

func lazyResultPage(cfg Config, st *Store, ok bool) string {
	s := lazyState()
	var b strings.Builder
	if s.Err != "" {
		b.WriteString(`<div class="warn">` + htmlEscape(s.Err) + `</div><a class="btn" href="/lazy">返回懒人清理</a>`)
		return appShell("懒人清理", "clean", b.String())
	}
	if !ok {
		if s.Running {
			b.WriteString(`<div class="card"><h2>清理进行中</h2>`)
			b.WriteString(fmt.Sprintf(`<p>已处理 %d/%d 个目录，实际移入隔离区 %s（计划 %s）。</p>`, s.Done, s.Total, human(s.Freed), human(s.Planned)))
			b.WriteString(lazyRunningButtons(s))
			b.WriteString(`<div class="muted" style="margin-top:8px">离开这个页面也没关系：其它页面右上角会显示进度，清理在后台继续。</div>`)
			b.WriteString(`<a class="btn" href="/lazy">看进度</a></div>`)
			return appShell("懒人清理", "clean", b.String())
		}
		b.WriteString(`<div class="warn">没有可清理的目录（或已经被清理过）。</div><a class="btn" href="/lazy">返回懒人清理</a>`)
		return appShell("懒人清理", "clean", b.String())
	}
	b.WriteString(`<div class="card"><h2>清理进行中</h2>`)
	b.WriteString(fmt.Sprintf(`<p>共 %d 个目录（计划 %s），已处理 %d 个，实际移入隔离区 %s。</p>`, s.Total, human(s.Planned), s.Done, human(s.Freed)))
	b.WriteString(lazyRunningButtons(s))
	b.WriteString(`<div class="muted" style="margin-top:10px">离开这个页面也没关系：其它页面右上角会显示进度，清理在后台继续。</div>`)
	b.WriteString(`<a class="btn" href="/lazy">看进度</a></div>`)
	return appShell("懒人清理", "clean", b.String())
}

func lazySeenIDs() []string {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return append([]string(nil), lazyCur.IDs...)
}

// lazyProgressBase 返回当前已完成目录累计的字节/文件/跳过数
func lazyProgressBase() (uint64, int, int) {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return lazyCur.Freed, lazyCur.Files, lazyCur.SkippedFiles
}

func lazyClearIDs() {
	lazyMu.Lock()
	lazyCur.IDs = nil
	lazyMu.Unlock()
}

// sampleLocked 抽样判断目录里有多少文件正被占用（打不开 = 被占用）
func sampleLocked(root string) (locked, total int) {
	deadline := time.Now().Add(350 * time.Millisecond)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if time.Now().After(deadline) || total >= 60 {
			return filepath.SkipAll
		}
		if err != nil || d.IsDir() {
			return nil
		}
		total++
		p16, e := syscall.UTF16PtrFromString(p)
		if e != nil {
			return nil
		}
		h, e := syscall.CreateFile(p16, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if e != nil {
			locked++
			return nil
		}
		_ = syscall.CloseHandle(h)
		return nil
	})
	return locked, total
}
