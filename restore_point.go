package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ---------- 清理前的系统还原点（C2：SRSetRestorePoint + 隔离区 = 双层回滚） ----------
//
// 设计判断（每一条都是为了「宁可少做，也不许假装做到」）：
//
//  1. 只在真删前落点。dry-run / 预览 / 规则试运行一律不落——落点本身有代价：
//     占卷影空间、要等系统把改动刷进快照，快则一两秒，慢则十几秒。
//  2. 落点失败不许静默继续。系统保护关掉、域策略禁用、非管理员，这三件事
//     在 Windows 上都是常态，而且 API 从「不报错」到「返回 0」的表现各不相同。
//     所以失败时必须把结论落到结果里（页面 + 日志 + 告警流），让人看到
//     「本次没有系统级回滚点」，只剩隔离区这一层可退。
//  3. 一次清理只落一次。清理往往分多批（一个方案几十个目录），每批都落既慢又
//     浪费空间，而且对同一批改动来说第二个点毫无意义。批次边界由 beginCleanupRun
//     显式标出；没标的零散调用（单文件直删）用静默窗口合并。
//  4. 落点失败不阻断清理。还原点是「多一层保险」，不是前置条件；它挂了，
//     隔离区仍然是可用的回滚路径，把清理也砍掉等于让人无处可退。
//
// 关于这一层的诚实边界：系统还原点保护的是注册表与系统文件。本工具清掉的
// 主要是用户/临时目录里的文件，那些文件不在还原点的覆盖面里——真要靠还原点
// 找回它们是不现实的。还原点的意义在于「清理过程中万一带崩了系统状态」，
// 文件级的回滚仍然只有隔离区那一层。文案里不能让人误以为点完还原就万事大吉。

// RestorePointState 是一次落点尝试的结论。
type RestorePointState string

const (
	// RestorePointNone：还没尝试过（dry-run、预览、没有真实删除）。
	RestorePointNone RestorePointState = ""
	// RestorePointCreated：系统还原点已创建。
	RestorePointCreated RestorePointState = "created"
	// RestorePointDisabled：系统保护/还原点服务被关闭（含组策略禁用）。
	RestorePointDisabled RestorePointState = "disabled"
	// RestorePointDenied：当前进程没有权限（最常见：非管理员）。
	RestorePointDenied RestorePointState = "denied"
	// RestorePointUnsupported：系统不提供 SRSetRestorePoint（非 Windows 或 API 缺失）。
	RestorePointUnsupported RestorePointState = "unsupported"
	// RestorePointFailed：其它失败。
	RestorePointFailed RestorePointState = "failed"
)

// RestorePointOutcome 是「这次清理有没有系统级回滚点」的完整答案。
type RestorePointOutcome struct {
	State RestorePointState
	Code  uint32 // Win32 错误码；0 表示成功
	Seq   int64  // 还原点序号（成功时有效）
	Desc  string // 写进还原点的描述，方便用户在系统还原界面里认出来
	At    time.Time
	Msg   string // 给用户看的一句话
}

// HasPoint 表示这次清理确实拿到了一个系统还原点。
func (o RestorePointOutcome) HasPoint() bool { return o.State == RestorePointCreated }

// Missing 表示尝试过、但没落成——这时必须让用户看见。
func (o RestorePointOutcome) Missing() bool {
	return o.State != RestorePointNone && o.State != RestorePointCreated
}

// attemptRestorePointFn 是底层调用的注入点。测试通过替换它覆盖
// 「成功」「被策略禁用」「无权限」三条路径——不伪造真实系统调用结果。
var attemptRestorePointFn = callSRSetRestorePoint

// SRSetRestorePointW 需要用到的结构体。字段布局必须和 Windows 头文件一致：
//
//	RESTOREPOINTINFOW { DWORD dwEventType; DWORD dwRestorePtType; INT64 llSequenceNumber;
//	                    WCHAR szDescription[MAX_DESC]; }
//	STATEMGRSTATUS    { DWORD nStatus; INT64 llSequenceNumber; }
const (
	srBeginSystemChange = 100 // BEGIN_SYSTEM_CHANGE
	srEndSystemChange   = 101 // END_SYSTEM_CHANGE
	srModifySettings    = 0   // MODIFY_SETTINGS
	srMaxDesc           = 256 // MAX_DESC（含结尾 0）

	errSuccess          = 0
	errAccessDenied     = 5
	errCallNotImplement = 120
	errProcNotFound     = 127
	errServiceDisabled  = 1058
	errServiceNotActive = 1062
	errResourceDisabled = 1815 // 系统保护关闭时 SR 会报这个
)

type restorePtInfo struct {
	EventType     uint32
	RestorePtType uint32
	Sequence      int64
	Description   [srMaxDesc]uint16
}

type stateMgrStatus struct {
	Status   uint32
	Sequence int64
}

// restorePointRaw 是底层 API 的原始返回。
type restorePointRaw struct {
	OK          bool
	Unsupported bool
	Code        uint32
	Seq         int64
}

// SRSetRestorePointW 的宿主 DLL 在不同文档/系统上说法不一：权威头文件
// srrestoreptapi.h 配套的是 sfc.dll；旧资料常写 powrprof.dll。
// 实测（本机 Windows 11，非管理员）：powrprof.dll 里没有这个导出（错误 127），
// 能导出它的是 sfc.dll。所以按候选顺序找，找到就用——找不到才算「系统不支持」。
var srDLLCandidates = []string{"sfc.dll", "powrprof.dll"}

var (
	srProcOnce sync.Once
	srProc     *syscall.LazyProc
)

func srSetRestorePointProc() *syscall.LazyProc {
	srProcOnce.Do(func() {
		for _, dll := range srDLLCandidates {
			p := syscall.NewLazyDLL(dll).NewProc("SRSetRestorePointW")
			if p.Find() == nil {
				srProc = p
				return
			}
		}
	})
	return srProc
}

// callSRSetRestorePoint 走公开的 SRSetRestorePointW。返回 TRUE 且 nStatus==0 才是真的成了；
// 官方文档里两种失败形态都要认：返回 FALSE（看 nStatus，其次 GetLastError），
// 以及返回 TRUE 但 nStatus 里带错误码。
func callSRSetRestorePoint(desc string) restorePointRaw {
	proc := srSetRestorePointProc()
	if proc == nil {
		return restorePointRaw{Unsupported: true, Code: errProcNotFound}
	}
	info := restorePtInfo{EventType: srBeginSystemChange, RestorePtType: srModifySettings}
	u, err := syscall.UTF16FromString(clipDesc(desc))
	if err != nil {
		return restorePointRaw{Code: errCallNotImplement}
	}
	copy(info.Description[:srMaxDesc-1], u)

	var status stateMgrStatus
	// 注意：失败时 info.Sequence 与 status 都可能被系统改写，这是 API 约定，不用回滚。
	r, _, lastErr := proc.Call(
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Pointer(&status)),
	)
	if r == 0 {
		code := status.Status
		if code == 0 {
			if errno, ok := lastErr.(syscall.Errno); ok {
				code = uint32(errno)
			}
		}
		return restorePointRaw{Code: code}
	}
	if status.Status != errSuccess {
		return restorePointRaw{Code: status.Status}
	}
	// 系统接受委托后，真正的快照是异步刷盘的；这里只确认「委托被接受」。
	// 不要在描述里承诺还原点已经可以还原——那要等 System Restore 自己完成。
	return restorePointRaw{OK: true, Seq: status.Sequence}
}

// clipDesc 把描述压到 MAX_DESC 以内。按 rune 截断，别把中文切成半个。
func clipDesc(s string) string {
	r := []rune(s)
	if len(r) > srMaxDesc-1 {
		r = r[:srMaxDesc-1]
	}
	return string(r)
}

// classifyRestorePoint 把 Win32 错误码翻译成「用户能看懂且能据此行动」的结论。
func classifyRestorePoint(raw restorePointRaw) RestorePointOutcome {
	out := RestorePointOutcome{Code: raw.Code, Seq: raw.Seq, At: time.Now()}
	switch {
	case raw.Unsupported:
		out.State = RestorePointUnsupported
		out.Msg = "本次没有系统级回滚点：这台系统不提供系统还原（SRSetRestorePoint 不可用）。文件仍进了隔离区，可以还原。"
		return out
	case raw.OK:
		out.State = RestorePointCreated
		out.Msg = fmt.Sprintf("已创建系统还原点（序号 %d），描述里带 diskmon-go 字样，可在「系统还原」界面认出来。", raw.Seq)
		return out
	}
	switch raw.Code {
	case errAccessDenied:
		out.State = RestorePointDenied
		out.Msg = fmt.Sprintf("本次没有系统级回滚点：当前进程没有创建还原点的权限（错误 %d，通常需要以管理员身份运行）。文件仍进了隔离区，可以还原。", raw.Code)
	case errServiceDisabled, errServiceNotActive, errResourceDisabled:
		out.State = RestorePointDisabled
		out.Msg = fmt.Sprintf("本次没有系统级回滚点：系统保护/还原点服务处于关闭状态（错误 %d）。文件仍进了隔离区，可以还原。", raw.Code)
	default:
		out.State = RestorePointFailed
		out.Msg = fmt.Sprintf("本次没有系统级回滚点：创建还原点失败（错误 %d）。文件仍进了隔离区，可以还原。", raw.Code)
	}
	return out
}

// restorePointDesc 写进还原点描述：谁创建的、什么时候、这次清理要动什么。
// 用户日后翻「系统还原」列表时，就靠这一段认出这一条是自己清的哪一次。
func restorePointDesc(subject string) string {
	sub := strings.TrimSpace(subject)
	if sub == "" {
		sub = "本次清理"
	}
	sub = clipDesc(sub)
	// 整体再截一次：前缀（工具名 + 时间）也是描述的一部分，
	// 只截 subject 会让总长超过 MAX_DESC，系统会自己截成什么样就不可控了。
	return clipDesc(fmt.Sprintf("diskmon-go 清理前自动还原点 · %s · %s", time.Now().Format("2006-01-02 15:04"), sub))
}

// ---------- 一次清理只落一次 ----------

type restoreLatch struct {
	mu      sync.Mutex
	depth   int   // 清理批次嵌套深度；0 表示当前没有显式批次
	subject string // 这次清理要动什么（写进描述）
	store   *Store // 留痕用；可为 nil（命令行/测试）

	attempted bool              // 本次批次已经尝试过（成功或失败都算）
	outcome   RestorePointOutcome
	lastTry   time.Time // 零散调用时的窗口合并依据
	batchGap  time.Duration
}

var restoreRun = &restoreLatch{batchGap: 2 * time.Minute}

// beginCleanupRun 标出一次真实清理的开始，返回的函数用于收尾。
//
// 批次边界必须由调用方标：只有调用方知道「这几十个目录是一次清理」还是
// 「用户点了两次」。嵌套调用（自动规则一轮里逐条执行）只最外层生效，
// 保证同一批里不会重复落点。
func beginCleanupRun(st *Store, subject string) func() {
	if restoreRun == nil {
		return func() {}
	}
	restoreRun.mu.Lock()
	restoreRun.depth++
	if restoreRun.depth == 1 {
		restoreRun.subject = subject
		restoreRun.store = st
		restoreRun.attempted = false
		restoreRun.outcome = RestorePointOutcome{}
	} else if restoreRun.subject == "" {
		restoreRun.subject = subject
	}
	restoreRun.mu.Unlock()
	return func() {
		restoreRun.mu.Lock()
		if restoreRun.depth > 0 {
			restoreRun.depth--
		}
		restoreRun.mu.Unlock()
	}
}

// ensureRestorePoint 在真实删除之前落一次点，并把结论如实返回。
//
// 调用契约：只允许在「马上就要动文件了」的那一刻调用；dry-run 与预览不许调。
// 持锁跨过系统调用是有意的：SRSetRestorePoint 会卡住几秒，若并发进去两批
// 清理，就可能落下两个点（浪费卷影空间），或者一个点在另一批已经开删之后才返回。
func ensureRestorePoint(subject string) RestorePointOutcome {
	l := restoreRun
	if l == nil {
		return RestorePointOutcome{State: RestorePointUnsupported, Msg: "本次没有系统级回滚点：还原点模块未初始化。"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.attempted {
		// 这一批已经落过（或已经失败过）：复用结论，不再重复调用系统。
		return l.outcome
	}
	if l.depth == 0 && !l.lastTry.IsZero() && time.Since(l.lastTry) < l.batchGap {
		// 调用方没标批次边界（单文件直删流程）：用静默窗口把这串连续删除并成一次，
		// 免得每个文件都去落一个点。窗口过期后重新落点，不会永久跳过。
		return l.outcome
	}

	// 批次主题优先：它知道「这次清理要动什么」的全貌（方案名 + 目录数），
	// 单个目录名只能退而求其次。
	subj := strings.TrimSpace(l.subject)
	if subj == "" {
		subj = strings.TrimSpace(subject)
	}
	out := attemptRestorePoint(subj)
	out.Desc = restorePointDesc(subj)

	l.outcome = out
	l.lastTry = time.Now()
	if l.depth > 0 {
		l.attempted = true // 零散调用不置位，靠窗口合并
	}
	st := l.store

	// 留痕：成功也要写日志；失败额外进告警流，这样页面之外也查得到。
	logf("restore point: state=%s code=%d seq=%d subject=%s", out.State, out.Code, out.Seq, subj)
	if st != nil && out.Missing() {
		st.AddAlert("restore-point", out.Msg)
	}
	return out
}

// attemptRestorePoint 只负责「调一次、翻译一次」，不碰状态机。
func attemptRestorePoint(subject string) RestorePointOutcome {
	return classifyRestorePoint(attemptRestorePointFn(restorePointDesc(subject)))
}

// restorePointSnapshot 返回本批清理目前的结论，供结果页展示。
func restorePointSnapshot() RestorePointOutcome {
	l := restoreRun
	if l == nil {
		return RestorePointOutcome{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.outcome
}

// restorePointHTML 把结论渲染成结果页上的一段。没有尝试过就什么都不渲染——
// 「没落点」只在真的清了东西之后才有意义，没清理却提示会变成噪音。
func restorePointHTML(o RestorePointOutcome) string {
	if o.State == RestorePointNone {
		return ""
	}
	if o.HasPoint() {
		return `<div class="ok" style="margin-top:8px">` + htmlEscape(o.Msg) + `</div>`
	}
	var b strings.Builder
	b.WriteString(`<div class="warn" style="margin-top:8px">` + htmlEscape(o.Msg))
	b.WriteString(`<br><span style="color:#8b94a7">回滚能力：只剩隔离区这一层（文件可还原，系统状态没有还原点可退）。` +
		`要系统级回滚：以管理员身份运行并以「系统保护」为系统盘开启还原点。</span>`)
	b.WriteString(`</div>`)
	return b.String()
}
