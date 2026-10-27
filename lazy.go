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

type lazyRun struct {
	Running bool
	Started time.Time
	Total   int
	Done    int
	Freed   uint64
	Failed  int
	IDs     []string
	DirList []string
	Err     string
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
	dirs := make([]DirEntry, 0, len(p.Dirs))
	for _, d := range p.Dirs {
		dirs = append(dirs, d)
	}
	lazyMu.Lock()
	lazyCur = lazyRun{Running: true, Started: time.Now(), Total: len(dirs)}
	lazyMu.Unlock()

	go func() {
		var freed uint64
		var ids []string
		var failed int
		for i, d := range dirs {
			e, err := q.apply(cfg, d.Path, "lazy")
			lazyMu.Lock()
			lazyCur.Done = i + 1
			if err != nil {
				failed++
				lazyCur.Failed = failed
			} else {
				freed += e.Bytes
				lazyCur.Freed = freed
				ids = append(ids, e.ID)
				lazyCur.IDs = append(lazyCur.IDs, e.ID)
			}
			lazyMu.Unlock()
		}
		lazyMu.Lock()
		lazyCur.Running = false
		lazyCur.Freed = freed
		lazyCur.IDs = ids
		lazyCur.Failed = failed
		lazyMu.Unlock()
		if len(ids) > 0 {
			st.AddAlert("lazy-clean", fmt.Sprintf("懒人清理：隔离 %d 个目录，释放 %s（可撤销）", len(ids), human(freed)))
			notify("diskmon 懒人清理完成", fmt.Sprintf("释放 %s，7 天内可在看板一键撤销", human(freed)), false)
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

// 懒人页
func lazyPage(cfg Config, st *Store) string {
	s := lazyState()
	p := lazyPreview(cfg, st)
	var b strings.Builder

	if s.Running {
		pct := 0
		if s.Total > 0 {
			pct = s.Done * 100 / s.Total
		}
		b.WriteString(`<div class="card"><h2>正在清理…</h2>`)
		b.WriteString(fmt.Sprintf(`<p>已完成 <b>%d / %d</b> 个目录，释放 <b>%s</b></p>`, s.Done, s.Total, human(s.Freed)))
		b.WriteString(fmt.Sprintf(`<div style="height:10px;background:#1c212b;border-radius:6px;overflow:hidden;margin:12px 0"><div style="height:100%%;width:%d%%;background:#33c179"></div></div>`, pct))
		b.WriteString(`<div class="muted">页面每 2 秒自动刷新；关闭也没关系，清理会在后台继续。</div>`)
		b.WriteString(`</div><meta http-equiv="refresh" content="2">`)
		return appShell("懒人清理", "clean", b.String())
	}

	b.WriteString(`<div class="card"><h2>懒人清理</h2>`)
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

	// 上一次结果
	if s.Freed > 0 && !s.Running {
		b.WriteString(fmt.Sprintf(`<div class="ok" style="margin-top:16px">上一次清理：%d 个目录，释放 %s（可撤销）</div>`, len(s.IDs), human(s.Freed)))
		if len(s.IDs) > 0 {
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/lazy/undo">一键撤销上一次清理</a>`)
		}
	}
	if s.Failed > 0 {
		b.WriteString(fmt.Sprintf(`<div class="muted">上次有 %d 个目录因被占用而跳过（不会影响其它目录）。</div>`, s.Failed))
	}
	b.WriteString(`</div>`)
	return appShell("懒人清理", "clean", b.String())
}

func lazyResultPage(cfg Config, st *Store, ok bool) string {
	s := lazyState()
	var b strings.Builder
	if !ok {
		b.WriteString(`<div class="warn">没有可清理的目录，或已经在清理中。</div><a class="btn" href="/">返回</a>`)
		return appShell("懒人清理", "clean", b.String())
	}
	b.WriteString(`<div class="card"><h2>清理进行中</h2>`)
	b.WriteString(fmt.Sprintf(`<p>共 %d 个目录，已处理 %d 个，释放 %s。</p>`, s.Total, s.Done, human(s.Freed)))
	b.WriteString(`<a class="btn" href="/lazy">看进度</a></div>`)
	return appShell("懒人清理", "clean", b.String())
}

func lazySeenIDs() []string {
	lazyMu.Lock()
	defer lazyMu.Unlock()
	return append([]string(nil), lazyCur.IDs...)
}
