package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// WinSxS 页面：把 DISM 的权威口径接到页面上（t10 留下的那半截）。
//
// 三条纪律，写在这里也写在页面上：
//  1. **页面加载绝不自动跑 DISM**。它要 1–2 分钟、要管理员、明显吃 CPU，
//     用户打开页面看一眼就走的情况下不该付这个代价。只有点按钮（POST）才跑。
//  2. 非管理员必须是**立刻**给结论，不是转圈 1–2 分钟再失败。
//     analyzeComponentStore 里已经在 spawn 之前挡住了，这里只是把它的话显示出来。
//  3. 页面要能看出四种状态：没跑过 / 跑着呢 / 跑成功 / 跑失败（带原因原文）。

// componentStoreRun 是测试缝：默认就是真的去跑 DISM，测试里换掉。
var componentStoreRun = analyzeComponentStore

type componentStoreState struct {
	Running bool
	Done    bool
	Result  ComponentStore
	StartAt int64
	EndAt   int64
}

var (
	componentStoreMu    sync.Mutex
	componentStoreCurrent = componentStoreState{}
)

// componentStoreSnapshot 返一份当前状态的拷贝（页面渲染用，别拿锁外的东西）。
func componentStoreSnapshot() componentStoreState {
	componentStoreMu.Lock()
	defer componentStoreMu.Unlock()
	return componentStoreCurrent
}

// componentStoreStart 起一次分析。已经在跑就不重复起。
// 返回 false 表示这次没起（已在跑）。
func componentStoreStart() bool {
	componentStoreMu.Lock()
	if componentStoreCurrent.Running {
		componentStoreMu.Unlock()
		return false
	}
	componentStoreCurrent = componentStoreState{Running: true, StartAt: time.Now().Unix()}
	componentStoreMu.Unlock()

	go func() {
		cs := componentStoreRun()
		componentStoreMu.Lock()
		componentStoreCurrent.Running = false
		componentStoreCurrent.Done = true
		componentStoreCurrent.Result = cs
		componentStoreCurrent.EndAt = time.Now().Unix()
		componentStoreMu.Unlock()
	}()
	return true
}

// componentStoreReset 把状态清回「没跑过」。测试用。
func componentStoreReset() {
	componentStoreMu.Lock()
	componentStoreCurrent = componentStoreState{}
	componentStoreMu.Unlock()
}

// winsxsPageHTML 渲染整页。注意它**不调用**任何会跑 DISM 的东西。
func winsxsPageHTML() string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">WinSxS 组件库 · DISM 权威口径</h2>`)
	b.WriteString(`<p class="muted">本工具扫出来的 <code>C:\Windows\WinSxS</code> 是「按路径求和」，` +
		`硬链接会让它虚高——微软自己的文档也这么说。这里的数字来自 ` +
		`<code>DISM /Online /Cleanup-Image /AnalyzeComponentStore</code>，是系统自己算的，` +
		`不是我们的估算。</p>`)

	// 成本 + 权限，先摆在按钮上面。别让人点了才发现要等两分钟。
	b.WriteString(`<div class="card"><h2>跑之前先知道</h2>`)
	b.WriteString(`<div class="warn">约 1–2 分钟 · 需要管理员 · 期间会明显占 CPU。不会在打开页面时自动跑。</div>`)
	if isElevated() {
		b.WriteString(`<div class="muted" style="margin-top:8px">当前进程是管理员权限，点下去会直接开始。</div>`)
	} else {
		b.WriteString(`<div class="muted" style="margin-top:8px">` +
			`当前<b>不是</b>管理员：点下去会立刻告诉你「需要管理员」，不会白等 1–2 分钟。</div>`)
	}
	b.WriteString(`<form method="post" action="/winsxs/run" style="margin-top:10px">` +
		`<button class="btn" type="submit">开始分析（约 1–2 分钟，需要管理员）</button></form>`)
	b.WriteString(`</div>`)

	st := componentStoreSnapshot()
	switch {
	case st.Running:
		waited := time.Now().Unix() - st.StartAt
		if waited < 0 {
			waited = 0
		}
		b.WriteString(`<div class="card"><h2>正在分析…</h2>`)
		b.WriteString(fmt.Sprintf(`<div class="muted">已经等了 %d 秒，通常 60–120 秒。这个页面每 5 秒自己刷新一次。</div>`, waited))
		b.WriteString(`<div class="muted" style="margin-top:6px">可以关掉这一页，跑完不影响结果。</div></div>`)
		b.WriteString(`<meta http-equiv="refresh" content="5">`)
	case st.Done && st.Result.OK():
		b.WriteString(`<div class="card"><h2>结果</h2>` + componentStoreHTML(st.Result) + `</div>`)
	case st.Done:
		b.WriteString(`<div class="card"><h2>没拿到可信结果</h2>`)
		b.WriteString(`<div class="warn">` + htmlEscape(st.Result.Err) + `</div>`)
		if raw := strings.TrimSpace(st.Result.Raw); raw != "" {
			b.WriteString(`<div class="muted" style="margin-top:8px">DISM 原始输出：</div>`)
			b.WriteString(`<pre class="path" style="white-space:pre-wrap">` + htmlEscape(raw) + `</pre>`)
		}
		b.WriteString(`<div class="muted" style="margin-top:8px">` +
			`拿不到权威口径时，WinSxS 那一类仍然显示本工具扫出来的数（可能虚高），不会给你一个编出来的数。</div>`)
		b.WriteString(`</div>`)
	default:
		b.WriteString(`<div class="card"><div class="muted">还没跑过。上面那个按钮点一下才会开始。</div></div>`)
	}
	return appShell("WinSxS 权威口径", "clean", b.String())
}
