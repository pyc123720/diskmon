package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------- 后台隔离操作（带进度） ----------

type opState struct {
	Running  bool
	Kind     string
	Label    string
	Total    int
	Done     int
	Bytes    uint64
	Files    int
	Skipped  int
	Current  string
	Started  time.Time
	Finished time.Time
	IDs      []string
	Errors   []string
	Restore  RestorePointOutcome // 本次清理前的系统还原点结论
}

var (
	opMu  sync.Mutex
	opCur opState
)

func opNow() opState {
	opMu.Lock()
	defer opMu.Unlock()
	s := opCur
	s.IDs = append([]string(nil), opCur.IDs...)
	s.Errors = append([]string(nil), opCur.Errors...)
	return s
}

func opIsRunning() bool {
	opMu.Lock()
	defer opMu.Unlock()
	return opCur.Running
}

// startQuarantineOp 在后台把一组目录移进隔离区（默认 reason=web）
func startQuarantineOp(cfg Config, st *Store, paths []string, label string) bool {
	return startQuarantineOpReason(cfg, st, paths, label, "web")
}

// startQuarantineOpReason 同上，可指定隔离原因（dup:/media: 等）
func startQuarantineOpReason(cfg Config, st *Store, paths []string, label, reason string) bool {
	opMu.Lock()
	if opCur.Running {
		opMu.Unlock()
		return false
	}
	opCur = opState{Running: true, Kind: "quarantine", Label: label, Total: len(paths), Started: time.Now()}
	opMu.Unlock()

	go func() {
		// 一次隔离操作 = 一次清理：批次边界在这里标出。几十个目录分批移动，
		// 还原点只在第一个真删之前落一次——它比这几十次移动都慢，重复落不划算。
		endRun := beginCleanupRun(st, fmt.Sprintf("%s：%d 个目录", label, len(paths)))
		defer endRun()
		q := openQuarantine(cfg)
		var ids []string
		var errs []string
		var bytes uint64
		var files, skipped int
		for i, p := range paths {
			e, err := q.applyWithProgress(cfg, p, reason, func(m uint64, f, s int, cur string) {
				opMu.Lock()
				opCur.Bytes = bytes + m
				opCur.Files = files + f
				opCur.Skipped = skipped + s
				opCur.Current = cur
				opMu.Unlock()
			}, nil)
			opMu.Lock()
			opCur.Done = i + 1
			if err != nil {
				errs = append(errs, p+"："+err.Error())
				opCur.Errors = append([]string(nil), errs...)
			} else {
				ids = append(ids, e.ID)
				bytes += e.Bytes
				files += e.Files
				skipped += e.Skipped
				opCur.Bytes = bytes
				opCur.Files = files
				opCur.Skipped = skipped
			}
			opMu.Unlock()
		}
		opMu.Lock()
		opCur.Running = false
		opCur.Finished = time.Now()
		opCur.IDs = ids
		opCur.Bytes = bytes
		opCur.Files = files
		opCur.Skipped = skipped
		opCur.Restore = restorePointSnapshot()
		opMu.Unlock()
		if len(ids) > 0 {
			st.AddAlert("quarantine", fmt.Sprintf("%s：隔离 %d 项，%s（%d 个占用文件跳过）",
				label, len(ids), human(bytes), skipped))
			notify("diskmon 清理完成", fmt.Sprintf("%s：%s，可撤销", label, human(bytes)), false)
		}
	}()
	return true
}

// opPage 进行中显示进度，结束后显示结果
func opPage(cfg Config, st *Store) string {
	s := opNow()
	var b strings.Builder
	if s.Running {
		pct := 0
		if s.Total > 0 {
			pct = s.Done * 100 / s.Total
		}
		verb, word := "正在移动文件", "已移动"
		if s.Kind == "restore" {
			verb, word = "正在还原", "已还原"
		}
		b.WriteString(`<div class="card"><h2>` + htmlEscape(s.Label) + ` · ` + verb + `…</h2>`)
		b.WriteString(fmt.Sprintf(`<div style="display:flex;align-items:baseline;gap:10px"><span class="big" style="font-size:34px;font-weight:700">%s</span><span class="muted">%s · %d 个文件</span></div>`,
			human(s.Bytes), word, s.Files))
		b.WriteString(fmt.Sprintf(`<div style="height:10px;background:#1c212b;border-radius:6px;overflow:hidden;margin:14px 0"><div style="height:100%%;width:%d%%;background:#33c179;transition:width .4s"></div></div>`, pct))
		b.WriteString(fmt.Sprintf(`<div class="muted">已完成 %d / %d 个目录%s</div>`, s.Done, s.Total, func() string {
			if s.Skipped > 0 {
				return fmt.Sprintf("，跳过 %d 个占用中的文件", s.Skipped)
			}
			return ""
		}()))
		if s.Current != "" {
			b.WriteString(`<div class="muted" style="margin-top:8px;font-family:ui-monospace,Consolas,monospace;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">` + htmlEscape(s.Current) + `</div>`)
		}
		b.WriteString(`<div class="muted" style="margin-top:10px">页面每秒刷新；操作在后台继续，可以切走。</div>`)
		b.WriteString(`</div><meta http-equiv="refresh" content="1">`)
		return appShell("正在清理", "clean", b.String())
	}
	b.WriteString(`<div class="card"><h2>` + htmlEscape(s.Label) + ` · 完成</h2>`)
	if s.Total == 0 {
		b.WriteString(`<div class="muted">没有需要移动的内容。</div>`)
	} else {
		b.WriteString(fmt.Sprintf(`<div class="ok">已隔离 <b>%d</b> 项，移动 <b>%s</b>（%d 个文件）`, len(s.IDs), human(s.Bytes), s.Files))
		if s.Skipped > 0 {
			b.WriteString(fmt.Sprintf(`，跳过 %d 个占用中的文件`, s.Skipped))
		}
		b.WriteString(`</div>`)
	}
	if len(s.Errors) > 0 {
		b.WriteString(fmt.Sprintf(`<div class="warn" style="margin-top:8px">%d 项失败：<br>`, len(s.Errors)))
		for i, e := range s.Errors {
			if i >= 5 {
				break
			}
			b.WriteString(`<code>` + htmlEscape(truncate(e, 120)) + `</code><br>`)
		}
		b.WriteString(`</div>`)
	}
	// 系统级回滚点的结论放在结果页上：落成了就写序号，没落成必须写清
	// 「本次没有系统级回滚点」，不能让人以为还有两层保险其实只有一层。
	b.WriteString(restorePointHTML(s.Restore))
	b.WriteString(`<div class="muted" style="margin-top:8px">隔离项默认保留 7 天，可在看板或隔离区还原。</div>`)
	b.WriteString(`<div style="margin-top:14px"><a class="btn" href="/q">去隔离区</a><a class="btn" href="/">返回总览</a></div>`)
	b.WriteString(`</div>`)
	return appShell("清理结果", "clean", b.String())
}

// startRestoreOp 在后台把隔离项还原，进度写入 opCur
func startRestoreOp(cfg Config, st *Store, ids []string, label string) bool {
	opMu.Lock()
	if opCur.Running {
		opMu.Unlock()
		return false
	}
	opCur = opState{Running: true, Kind: "restore", Label: label, Total: len(ids), Started: time.Now()}
	opMu.Unlock()

	go func() {
		q := openQuarantine(cfg)
		var bytes uint64
		var files int
		var done int
		var errs []string
		for _, id := range ids {
			e, err := q.restore(id)
			opMu.Lock()
			done++
			opCur.Done = done
			if err != nil {
				errs = append(errs, err.Error())
				opCur.Errors = append([]string(nil), errs...)
			} else {
				bytes += e.Bytes
				files += e.Files
				opCur.Bytes = bytes
				opCur.Files = files
				opCur.Current = e.Original
			}
			opMu.Unlock()
		}
		opMu.Lock()
		opCur.Running = false
		opCur.Finished = time.Now()
		opCur.Bytes = bytes
		opCur.Files = files
		opMu.Unlock()
		if done-len(errs) > 0 {
			st.AddAlert("restore", fmt.Sprintf("%s：还原 %d 项，%s", label, done-len(errs), human(bytes)))
		}
	}()
	return true
}
