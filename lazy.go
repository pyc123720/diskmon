package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------- 懒人清理：一个按钮，跑一轮最安全的清理 ----------
//
// 只做「保守方案」：clean 层、≥100MB、白名单/黑名单之外。
// 全部进隔离区（可撤销），不会永久删除任何东西。

type lazyDirResult struct {
	Path    string
	Planned uint64
	Actual  uint64
	Skipped int
	Err     string
}

type lazyRun struct {
	Running  bool
	Started  time.Time
	Finished time.Time
	Total    int
	Done     int
	Planned  uint64
	Freed    uint64
	Files    int
	SkippedFiles int
	Current  string
	Failed   int
	IDs      []string
	Results  []lazyDirResult
	Err      string
}

var (
	lazyMu  sync.Mutex
	lazyCur = lazyRun{}
)

func lazyState() lazyRun {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return lazyCur
}

func lazyCanStart() bool {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return !lazyCur.Running
}

func startLazy(cfg Config, st *Store, q *Quarantine) bool {
	if !lazyCanStart() {
		return false
	}
	p := buildPlan(cfg, st, "conservative")
	if len(p.Dirs) == 0 {
		return false
	}
	var planned uint64
	for _, d := range p.Dirs {
		planned += d.Bytes
	}
	qUsed := q.totalBytes() // 先取隔离区占用，避免在 lazyMu 里再去拿 q.mu
	if cfg.QuarantineMaxGB > 0 && qUsed+planned > uint64(cfg.QuarantineMaxGB)<<30 {
		lazyMu.Lock()
		lazyCur = lazyRun{Err: fmt.Sprintf("计划清理 %s，会超过隔离区上限 %d GB（当前 %s）。先还原或清理隔离区，或把保留天数调小。",
			human(planned), cfg.QuarantineMaxGB, human(qUsed))}
		lazyMu.Unlock()
		return false
	}
	lazyMu.Lock()
	lazyCur = lazyRun{Running: true, Started: time.Now(), Total: len(p.Dirs), Planned: planned}
	lazyMu.Unlock()

	go func() {
		var freed uint64
		var ids []string
		var failed int
		results := make([]lazyDirResult, 0, len(p.Dirs))
		for i, d := range p.Dirs {
			baseFreed, baseFiles, baseSkipped := lazyProgressBase()
			e, err := q.applyWithProgress(cfg, d.Path, "lazy", func(m uint64, f, s int, cur string) {
				lazyMu.Lock()
				lazyCur.Freed = baseFreed + m
				lazyCur.Files = baseFiles + f
				lazyCur.SkippedFiles = baseSkipped + s
				lazyCur.Current = cur
				lazyMu.Unlock()
			})
			res := lazyDirResult{Path: d.Path, Planned: d.Bytes}
			lazyMu.Lock()
			lazyCur.Done = i + 1
			if err != nil {
				failed++
				lazyCur.Failed = failed
				res.Err = err.Error()
			} else {
				freed += e.Bytes
				res.Actual = e.Bytes
				res.Skipped = e.Skipped
				ids = append(ids, e.ID)
				lazyCur.Freed = freed
				lazyCur.IDs = append(lazyCur.IDs, e.ID)
			}
			results = append(results, res)
			lazyCur.Results = append([]lazyDirResult(nil), results...)
			lazyMu.Unlock()
		}
		lazyMu.Lock()
		lazyCur.Running = false
		lazyCur.Freed = freed
		lazyCur.IDs = ids
		lazyCur.Failed = failed
		lazyCur.Finished = time.Now()
		lazyMu.Unlock()
		if len(ids) > 0 {
			st.AddAlert("lazy-clean", fmt.Sprintf("懒人清理：隔离 %d 个目录，计划 %s，实际 %s（%d 个跳过）",
				len(ids), human(planned), human(freed), failed))
			notify("diskmon 懒人清理完成", fmt.Sprintf("实际释放 %s（计划 %s，占用中的文件已跳过）", human(freed), human(planned)), false)
		}
	}()
	return true
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

// resultsTable 展示"计划 vs 实际"，解释差异
func lazyResultsTable(results []lazyDirResult) string {
	var b strings.Builder
	b.WriteString(`<table><tr><th>目录</th><th style="text-align:right">计划</th><th style="text-align:right">实际</th><th>结果</th></tr>`)
	shown := 0
	for _, r := range results {
		if shown >= 8 {
			b.WriteString(fmt.Sprintf(`<tr><td colspan="4" class="muted">… 还有 %d 个</td></tr>`, len(results)-shown))
			break
		}
		shown++
		status := "已隔离"
		switch {
		case r.Err != "":
			status = `跳过：` + htmlEscape(truncate(r.Err, 40))
		case r.Skipped > 0:
			status = fmt.Sprintf("已隔离，跳过 %d 个占用中文件", r.Skipped)
		case r.Actual < r.Planned*9/10:
			status = "已隔离（部分文件被占用）"
		}
		b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td class="r">%s</td><td class="muted">%s</td></tr>`,
			htmlEscape(r.Path), human(r.Planned), human(r.Actual), status))
	}
	b.WriteString(`</table>`)
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
		if pct > 99 {
			pct = 99
		}
		b.WriteString(`<div class="card"><h2>正在清理…</h2>`)
		b.WriteString(fmt.Sprintf(`<p>已移动 <b>%s</b> / 计划 <b>%s</b> · %d 个文件 · 跳过 %d 个</p>`,
			human(s.Freed), human(s.Planned), s.Files, s.SkippedFiles))
		b.WriteString(fmt.Sprintf(`<div style="height:10px;background:#1c212b;border-radius:6px;overflow:hidden;margin:12px 0"><div style="height:100%%;width:%d%%;background:#33c179;transition:width .4s"></div></div>`, pct))
		if s.Current != "" {
			b.WriteString(`<div class="muted" style="font-family:ui-monospace,Consolas,monospace;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">` + htmlEscape(s.Current) + `</div>`)
		}
		b.WriteString(`<div class="muted">页面每 2 秒自动刷新；关闭也没关系，清理会在后台继续。其它页面右上角会显示进度。</div>`)
		b.WriteString(`</div><meta http-equiv="refresh" content="2">`)
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
	b.WriteString(`<div class="muted" style="margin:-4px 0 10px">这个数字来自上次扫描；正在被占用的文件会被跳过，所以实际释放可能略少。清理完成后会显示「计划 vs 实际」。</div>`)
	b.WriteString(`<table><tr><th>目录</th><th style="text-align:right">大小</th></tr>`)
	for i, d := range p.Dirs {
		if i >= 12 {
			b.WriteString(fmt.Sprintf(enFormat(`<tr><td colspan="2" class="muted">… 另外 %d 个</td></tr>`, `<tr><td colspan="2" class="muted">… plus %d more</td></tr>`), len(p.Dirs)-12))
			break
		}
		b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td></tr>`, htmlEscape(d.Path), human(d.Bytes)))
	}
	b.WriteString(`</table>`)
	b.WriteString(`<div style="margin-top:16px"><a class="btn" style="background:#1f6f4a;border-color:#33c179;font-size:15px;padding:11px 22px" href="/lazy/start">开始清理（可撤销）</a>`)
	b.WriteString(`<a class="btn" href="/clean">看其它清理方式</a></div>`)

	// 上一次结果：计划 vs 实际
	if !s.Finished.IsZero() {
		skipNote := ""
		if s.SkippedFiles > 0 {
			skipNote = fmt.Sprintf("，跳过 %d 个占用中的文件", s.SkippedFiles)
		}
		failNote := ""
		if s.Failed > 0 {
			failNote = fmt.Sprintf("，%d 个目录无法移动", s.Failed)
		}
		b.WriteString(fmt.Sprintf(`<div class="ok" style="margin-top:16px">上一次清理：计划 %s → 实际 <b>%s</b>（%d 个目录%s%s）</div>`,
			human(s.Planned), human(s.Freed), len(s.IDs), skipNote, failNote))
		if len(s.Results) > 0 {
			b.WriteString(`<details style="margin-top:8px"><summary class="muted" style="cursor:pointer">看计划 / 实际明细</summary>`)
			b.WriteString(lazyResultsTable(s.Results))
			b.WriteString(`</details>`)
		}
		if len(s.IDs) > 0 {
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/lazy/undo">一键撤销上一次清理</a>`)
		}
	}
	b.WriteString(`</div>`)
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
		b.WriteString(`<div class="warn">没有可清理的目录，或已经在清理中。</div><a class="btn" href="/">返回</a>`)
		return appShell("懒人清理", "clean", b.String())
	}
	b.WriteString(`<div class="card"><h2>清理进行中</h2>`)
	b.WriteString(fmt.Sprintf(`<p>共 %d 个目录（计划 %s），已处理 %d 个，实际释放 %s。</p>`, s.Total, human(s.Planned), s.Done, human(s.Freed)))
	b.WriteString(`<div class="muted">离开这个页面也没关系：其它页面右上角会显示进度，清理在后台继续。</div>`)
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
