package main

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	pCreateFileW                  = kernel32.NewProc("CreateFileW")
	pDeviceIoControl              = kernel32.NewProc("DeviceIoControl")
	pOpenFileById                 = kernel32.NewProc("OpenFileById")
	pGetFinalPathNameByHandleW    = kernel32.NewProc("GetFinalPathNameByHandleW")
	pGetFileInformationByHandleEx = kernel32.NewProc("GetFileInformationByHandleEx")
	pCloseHandle                  = kernel32.NewProc("CloseHandle")
)

const (
	fsctlQueryUsnJournal  = 0x000900F4
	fsctlReadUsnJournal   = 0x000900BB
	fsctlCreateUsnJournal = 0x000900E4

	genericRead      = 0x80000000
	shareAll         = 0x7
	openExisting     = 3
	flagBackupSem    = 0x02000000
	fileReadAttrs    = 0x80
	invalidHandle    = ^uintptr(0)

	reasonDataOverwrite = 0x00000001
	reasonDataExtend    = 0x00000002
	reasonDataTruncate  = 0x00000004
	reasonFileCreate    = 0x00000100
	reasonFileDelete    = 0x00000200
	reasonRenameOld     = 0x00001000
	reasonRenameNew     = 0x00002000
	reasonClose         = 0x80000000

	writeMask = reasonDataOverwrite | reasonDataExtend | reasonFileCreate | reasonDataTruncate
)

type usnJournalData struct {
	ID              uint64
	First           int64
	Next            int64
	LowestValid     int64
	Max             int64
	MaximumSize     uint64
	AllocationDelta uint64
}

type readUsnData struct {
	Start            int64
	ReasonMask       uint32
	ReturnOnlyOnClose uint32
	Timeout          uint64
	BytesToWaitFor   uint64
	JournalID        uint64
}

type fileIDDesc struct {
	Size  uint32
	Type  int32
	Union [16]byte
}

type fileStandardInfo struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  uint8
	Directory      uint8
	_              [2]byte
}

type UsnEvent struct {
	Path   string
	Bytes  uint64
	Reason string
}

type UsnStats struct {
	Records  int
	WithPath int
	SizeOK   int
	SizeFail int
	Growth   uint64
}

type UsnTailer struct {
	h         syscall.Handle
	letter    string
	journalID uint64
	nextUSN   int64
	mu        sync.Mutex
	pathCache map[uint64]string
	sizes     map[string]uint64
	lastErr   error
}

func isElevated() bool {
	shell := syscall.NewLazyDLL("shell32.dll")
	proc := shell.NewProc("IsUserAnAdmin")
	r, _, _ := proc.Call()
	return r != 0
}

func openVolume(letter string) (syscall.Handle, error) {
	p, _ := syscall.UTF16PtrFromString(`\\.\` + letter)
	h, _, err := pCreateFileW.Call(uintptr(unsafe.Pointer(p)), genericRead, shareAll, 0,
		openExisting, flagBackupSem, 0)
	if h == invalidHandle || h == 0 {
		return 0, fmt.Errorf("打开卷失败：%v（需要管理员权限）", err)
	}
	return syscall.Handle(h), nil
}

func queryJournal(h syscall.Handle) (usnJournalData, error) {
	var jd usnJournalData
	var ret uint32
	r, _, err := pDeviceIoControl.Call(uintptr(h), fsctlQueryUsnJournal, 0, 0,
		uintptr(unsafe.Pointer(&jd)), unsafe.Sizeof(jd),
		uintptr(unsafe.Pointer(&ret)), 0)
	if r == 0 {
		return jd, err
	}
	return jd, nil
}

func createJournal(h syscall.Handle) error {
	type createData struct{ MaximumSize, AllocationDelta uint64 }
	c := createData{32 << 20, 4 << 20}
	var ret uint32
	r, _, err := pDeviceIoControl.Call(uintptr(h), fsctlCreateUsnJournal,
		uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c), 0, 0,
		uintptr(unsafe.Pointer(&ret)), 0)
	if r == 0 {
		return err
	}
	return nil
}

// ---------- 跨进程增量锚点 ----------
//
// usnEventsSince 用的是**进程内**的序号差，出了这个进程就没意义。
// 所以「这期间到底有没有写入」要做跨进程判断，只能拿卷上 USN 日志自己的坐标去比。
//
// 纪律：这里每一个「拒绝」都是**不确定**，不是「大概没变」。
// 这份数字是给人拿去删东西的，宁可多扫一遍，也不许报一份陈旧账。

// usnAnchor 是某一时刻该卷 USN 日志的坐标。它是纯数值，不是句柄，可以落盘。
type usnAnchor struct {
	JournalID uint64 `json:"journal_id"` // 日志的身份：被删掉重建后会变
	First     int64  `json:"first"`      // 当时日志里最老一条记录的 USN
	Next      int64  `json:"next"`       // 当时「下一条记录会拿到的」USN
}

// valid 判断这份坐标本身是否自洽。0 / 负数只可能是没读到或者是假的。
func (a usnAnchor) valid() bool {
	return a.JournalID != 0 && a.First > 0 && a.Next > 0 && a.First <= a.Next
}

// currentUsnAnchor 读一次卷的 USN 日志坐标。需要管理员权限。
// 读不到就是读不到——调用方必须当成「不确定」，退回全量重扫，不许猜。
func currentUsnAnchor(letter string) (usnAnchor, error) {
	h, err := openVolume(letter)
	if err != nil {
		return usnAnchor{}, err
	}
	defer pCloseHandle.Call(uintptr(h))
	jd, err := queryJournal(h)
	if err != nil {
		return usnAnchor{}, fmt.Errorf("查询 USN 日志失败：%w", err)
	}
	a := usnAnchor{JournalID: jd.ID, First: jd.First, Next: jd.Next}
	if !a.valid() {
		return usnAnchor{}, fmt.Errorf("USN 日志坐标不可信（id=%d first=%d next=%d）", a.JournalID, a.First, a.Next)
	}
	return a, nil
}

// usnAnchorReader 是 currentUsnAnchor 的间接层。抽出来只有一个理由：
// 复用判据的单测不该真的去碰一块 NTFS 卷，也不该要求测试进程有管理员权限。
var usnAnchorReader = currentUsnAnchor

// anchorStillClean 判断「从 prev 到 cur 这段里，这条卷上一条新记录都没有」。
// 纯函数：不碰系统调用，方便直接测。返回 false 时给出原因，每条都是「不确定」。
//
// 它 = 结构性判据（usnAnchorVerdict）+ 「Next 必须没动」。
// t5 的那批拒绝分支一条没少，只是挪进了 usnAnchorVerdict 里，让 t7 的 delta 判定
// 能复用同一套结构性检查，而不是复制一遍。
func anchorStillClean(prev, cur usnAnchor) (bool, string) {
	if ok, why := usnAnchorVerdict(prev, cur); !ok {
		return false, why
	}
	if cur.Next != prev.Next {
		return false, "锚点之后有新的 USN 记录"
	}
	return true, ""
}

// ---------- USN 记录的 reason 位判定（t7：把「零条记录」放宽到「零条改动」） ----------

// usnHarmlessReasons 是「这条记录不会让扫描数字变样」的白名单。逐位说明理由：
//
//	reasonClose 0x80000000 —— 句柄关闭。Windows 的 USN 只记「发生过什么改动」：
//	  读文件、查询属性根本不会产生记录。一条记录如果只带 CLOSE，说明这个文件在
//	  这一段里除了被关掉没有别的事可记。合并记录的 reason 是 OR 累积的，
//	  写位不会被 CLOSE 吃掉。
//
// 其余每一位都不在白名单里，包括看着最像「只是属性」的那几个：
//
//	BASIC_INFO_CHANGE    —— mtime / 属性变了。mtime 直接进 DayWrites 与 TopFiles。
//	SECURITY_CHANGE      —— ACL / owner 变了，Owners 那张表会跟着失真。
//	HARD_LINK_CHANGE     —— 硬链接数变了，Shared / Unique 的口径就变了。
//	REPARSE_POINT_CHANGE —— junction / 符号链接变了，遍历口径跟着变。
//	INDEXABLE / OBJECT_ID / INTEGRITY / COMPRESSION / ENCRYPTION / STREAM /
//	TRANSACTED / DESIRED_STORAGE_CLASS —— 确实不改体积，但都极罕见，
//	放行收益接近于零、风险不为零，所以同样不放行。
//
// 一句话：这张表只放「确认无害」，判不准的一律当成改动。
const usnHarmlessReasons uint32 = reasonClose

// usnReasonIsModifying 判断一条记录的原因位里有没有白名单以外的东西。
// 不认识的位同样算改动——这就是「判不准就不放行」的落点。
func usnReasonIsModifying(reason uint32) bool {
	return reason&^usnHarmlessReasons != 0
}

// usnReasonRec 是我们从一条 USN 记录里真正需要的东西：位置 + 原因位。
type usnReasonRec struct {
	USN    int64
	Reason uint32
}

const (
	usnRecordHeader = 60 // USN_RECORD_V2 固定头长度，后面跟变长文件名
	usnRecordAlign  = 8
)

// parseUsnReasons 解析 FSCTL_READ_USN_JOURNAL 的返回缓冲：前 8 字节是「下一次从哪读」，
// 后面是变长记录。
//
// 任何结构上的不确定都返回错误，不做「大概是这个意思」的猜测：长度越界 / 小于头长 /
// 不是 8 字节对齐 / 版本不认识 / 名字字段越界 / 末尾剩半条 —— 全部算读不懂。
func parseUsnReasons(buf []byte) ([]usnReasonRec, int64, error) {
	if len(buf) < 8 {
		return nil, 0, fmt.Errorf("USN 返回缓冲太短：%d", len(buf))
	}
	next := int64(binary.LittleEndian.Uint64(buf[:8]))
	var out []usnReasonRec
	i := 8
	for i < len(buf) {
		// 剩下不足 4 字节：连记录长度都读不出来，尾巴是残的。
		if i+4 > len(buf) {
			return nil, next, fmt.Errorf("USN 记录尾部不完整（偏移 %d）", i)
		}
		n := int(binary.LittleEndian.Uint32(buf[i : i+4]))
		if n < usnRecordHeader || n%usnRecordAlign != 0 {
			return nil, next, fmt.Errorf("USN 记录长度非法：%d（偏移 %d）", n, i)
		}
		if i+n > len(buf) {
			return nil, next, fmt.Errorf("USN 记录越界：需要 %d，只剩 %d", n, len(buf)-i)
		}
		rec := buf[i : i+n]
		// 只认版本 2。V1 没有时间戳，V3/V4 是 128 位文件引用与区间跟踪——
		// 字段布局不一样，不认识就别猜。
		if major := binary.LittleEndian.Uint16(rec[4:6]); major != 2 {
			return nil, next, fmt.Errorf("不认识的 USN 记录版本：%d", major)
		}
		nameLen := int(binary.LittleEndian.Uint16(rec[56:58]))
		nameOff := int(binary.LittleEndian.Uint16(rec[58:60]))
		if nameOff < usnRecordHeader || nameOff+nameLen > n {
			return nil, next, fmt.Errorf("USN 记录名字段越界（off=%d len=%d rec=%d）", nameOff, nameLen, n)
		}
		out = append(out, usnReasonRec{
			USN:    int64(binary.LittleEndian.Uint64(rec[24:32])),
			Reason: binary.LittleEndian.Uint32(rec[40:44]),
		})
		i += n
	}
	if i != len(buf) {
		return nil, next, fmt.Errorf("USN 缓冲末尾有 %d 字节说不清", len(buf)-i)
	}
	return out, next, nil
}

// readUsnDelta 读 [from,to) 之间的 USN 记录，只回答两件事：读到几条、
// 以及第一条「改动类」记录的 reason（0 表示全是无改动的）。
// 见到改动类记录立刻返回——结论已经是拒绝，不必读完。
func readUsnDelta(letter string, from, to int64, journalID uint64) (int, uint32, error) {
	if to <= from {
		return 0, 0, nil
	}
	h, err := openVolume(letter)
	if err != nil {
		return 0, 0, err
	}
	defer pCloseHandle.Call(uintptr(h))

	const (
		bufSize    = 1 << 20
		maxRecords = 20000 // 一段里几十万条记录时，判定成本已经超过重扫本身
	)
	buf := make([]byte, bufSize)
	cur := from
	total := 0
	for cur < to {
		rd := readUsnData{Start: cur, ReasonMask: 0xFFFFFFFF, JournalID: journalID}
		var ret uint32
		r, _, callErr := pDeviceIoControl.Call(uintptr(h), fsctlReadUsnJournal,
			uintptr(unsafe.Pointer(&rd)), unsafe.Sizeof(rd),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(bufSize),
			uintptr(unsafe.Pointer(&ret)), 0)
		if r == 0 {
			return total, 0, fmt.Errorf("读 USN 记录失败：%v", callErr)
		}
		n := int(ret)
		if n < 8 || n > bufSize {
			return total, 0, fmt.Errorf("USN 返回长度异常：%d", n)
		}
		recs, next, perr := parseUsnReasons(buf[:n])
		if perr != nil {
			return total, 0, perr
		}
		for _, rec := range recs {
			// 越过 to 的记录属于「现在」之后，不在这次判定范围内。
			if rec.USN >= to {
				return total, 0, nil
			}
			total++
			if usnReasonIsModifying(rec.Reason) {
				return total, rec.Reason, nil
			}
			if total > maxRecords {
				return total, 0, fmt.Errorf("这一段 USN 记录太多（>%d 条），判定成本超过重扫", maxRecords)
			}
		}
		if next <= cur {
			// 没有前进：要么已经读到头，要么内核给了个说不清的值。别在这里死循环。
			return total, 0, fmt.Errorf("USN 读取没有前进（cur=%d to=%d）", cur, to)
		}
		cur = next
	}
	return total, 0, nil
}

// usnDeltaReader 是 readUsnDelta 的间接层：判据的单测不该真的去读一块 NTFS 卷。
var usnDeltaReader = readUsnDelta

// usnAnchorVerdict 只看坐标的**结构性**判据（t5 的那批拒绝分支，一条都没放宽）：
// 坐标本身是否自洽、日志是不是被换过、有没有回退、锚点是不是被挤出了窗口。
// 它不判断「这期间有没有改动」——那是 delta 那层的事，两层互不替代。
func usnAnchorVerdict(prev, cur usnAnchor) (bool, string) {
	if !prev.valid() {
		// 缓存里的坐标不完整（老版本写的、或者写了一半），没法比。
		return false, "缓存里的 USN 锚点不完整"
	}
	if !cur.valid() {
		return false, "当前 USN 日志坐标不可信"
	}
	if cur.JournalID != prev.JournalID {
		// 日志被删除重建过：新日志的 USN 从 0 重新长，旧坐标和它没有可比性。
		return false, "USN 日志已重建（journalID 变了）"
	}
	if cur.Next < prev.Next {
		// USN 只会单调递增。变小说明日志回绕或被截断了，旧坐标不可解释。
		return false, "USN 回退（日志被截断或回绕）"
	}
	if cur.First > prev.Next {
		// 我们记录的那个位置已经被挤出日志窗口：它之后发生了什么已经无从得知。
		return false, "锚点已滚出 USN 日志窗口"
	}
	return true, ""
}

// deltaResult 是「锚点之后那一小段 USN 记录」的判定输入。
// 单独抽成一个值，是为了让整条判定链能离线测：真正读盘只发生在填它的那一层。
type deltaResult struct {
	examined  bool   // 有没有真的去读
	records   int    // 读到的记录数
	modifying uint32 // 第一条改动类记录的 reason；0 表示全是无改动的
	err       error  // 读取 / 解析失败
}

// usnAnchorClean 是跨进程复用的完整判定：先过结构性判据，再看这段记录里有没有改动。
//
// 注意顺序：结构性判据任何一条不过就直接拒绝（t5 的口径，一条没松）。
// 只有「坐标可信、但确实多了些记录」这一种情况，才轮到逐条看 reason——
// 这正是 t7 放宽的那一小步，其余全是拒绝。
func usnAnchorClean(prev, cur usnAnchor, d deltaResult) (bool, string) {
	if ok, why := usnAnchorVerdict(prev, cur); !ok {
		return false, why
	}
	if cur.Next == prev.Next {
		// 一条新记录都没有：连 delta 都不用读，直接放行。
		return true, ""
	}
	if d.err != nil {
		// 读不到 / 读不懂 —— 等于不知道。宁可重扫。
		return false, "读不到 USN 记录：" + d.err.Error()
	}
	if !d.examined {
		// 需要读却没去读，同样算不知道。
		return false, "USN 记录未读取，无法判定"
	}
	if d.modifying != 0 {
		return false, fmt.Sprintf("期间有改动类 USN 记录（reason=0x%08x）", d.modifying)
	}
	return true, ""
}

func newUsnTailer(letter string) (*UsnTailer, error) {
	h, err := openVolume(letter)
	if err != nil {
		return nil, err
	}
	jd, err := queryJournal(h)
	if err != nil {
		if cerr := createJournal(h); cerr != nil {
			pCloseHandle.Call(uintptr(h))
			return nil, fmt.Errorf("查询/创建 USN 日志失败：%v", cerr)
		}
		jd, err = queryJournal(h)
		if err != nil {
			pCloseHandle.Call(uintptr(h))
			return nil, err
		}
	}
	return &UsnTailer{
		h: h, letter: letter, journalID: jd.ID, nextUSN: jd.Next,
		pathCache: map[uint64]string{}, sizes: map[string]uint64{},
	}, nil
}

func (t *UsnTailer) fileNameByID(frn uint64) string {
	if p, ok := t.pathCache[frn]; ok {
		return p
	}
	var desc fileIDDesc
	desc.Size = uint32(unsafe.Sizeof(desc))
	binary.LittleEndian.PutUint64(desc.Union[:8], frn)
	h, _, _ := pOpenFileById.Call(uintptr(t.h), uintptr(unsafe.Pointer(&desc)),
		fileReadAttrs, shareAll, 0, 0)
	if h == invalidHandle || h == 0 {
		return ""
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n, _, _ := pGetFinalPathNameByHandleW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 1024, 0)
	if int32(n) <= 0 {
		return ""
	}
	p := syscall.UTF16ToString(buf[:n])
	p = strings.TrimPrefix(p, `\\?\`)
	p = strings.TrimPrefix(p, `\??\`)
	if !strings.HasPrefix(strings.ToUpper(p), strings.ToUpper(t.letter)) {
		return ""
	}
	if len(t.pathCache) > 5000 {
		t.pathCache = map[uint64]string{}
	}
	t.pathCache[frn] = p
	return p
}

func (t *UsnTailer) sizeOf(frn uint64) int64 {
	var desc fileIDDesc
	desc.Size = uint32(unsafe.Sizeof(desc))
	binary.LittleEndian.PutUint64(desc.Union[:8], frn)
	h, _, _ := pOpenFileById.Call(uintptr(t.h), uintptr(unsafe.Pointer(&desc)),
		fileReadAttrs, shareAll, 0, 0)
	if h == invalidHandle || h == 0 {
		return -1
	}
	defer pCloseHandle.Call(h)
	var info fileStandardInfo
	ok, _, _ := pGetFileInformationByHandleEx.Call(h, 1, uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info))
	if ok == 0 {
		return -1
	}
	return info.EndOfFile
}

func reasonText(r uint32) string {
	names := []struct {
		bit  uint32
		name string
	}{
		{reasonDataOverwrite, "改数据"}, {reasonDataExtend, "追加"}, {reasonDataTruncate, "截断"},
		{reasonFileCreate, "新建"}, {reasonFileDelete, "删除"}, {reasonRenameOld, "改名旧"},
		{reasonRenameNew, "改名新"}, {reasonClose, "关闭"}, {0x0080, "权限改"},
		{0x0040, "EA改"}, {0x00040000, "重解析"}, {0x00080000, "流改"},
	}
	var out []string
	for _, n := range names {
		if r&n.bit != 0 {
			out = append(out, n.name)
		}
	}
	if len(out) == 0 {
		return fmt.Sprintf("0x%x", r)
	}
	return strings.Join(out, "/")
}

// PollWithErr 与 Poll 相同，但会把读取错误返回给调用方（供看门狗判断是否重启）
func (t *UsnTailer) PollWithErr(minDelta uint64) (int, map[string]uint64, []UsnEvent, error) {
	t.mu.Lock()
	t.lastErr = nil
	t.mu.Unlock()
	recs, dirs, events, _ := t.Poll(minDelta)
	t.mu.Lock()
	err := t.lastErr
	t.mu.Unlock()
	return recs, dirs, events, err
}

// Poll 读一轮变更；返回按目录聚合的字节增长与 Top 事件
func (t *UsnTailer) Poll(minDelta uint64) (int, map[string]uint64, []UsnEvent, UsnStats) {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := UsnStats{}
	dirs := map[string]uint64{}
	var events []UsnEvent

	req := readUsnData{
		Start: t.nextUSN, ReasonMask: 0xFFFFFFFF, ReturnOnlyOnClose: 0,
		Timeout: 0, BytesToWaitFor: 0, JournalID: t.journalID,
	}
	buf := make([]byte, 256*1024)
	var ret uint32
	r, _, _ := pDeviceIoControl.Call(uintptr(t.h), fsctlReadUsnJournal,
		uintptr(unsafe.Pointer(&req)), unsafe.Sizeof(req),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&ret)), 0)
	if r == 0 {
		t.mu.Lock()
		t.lastErr = fmt.Errorf("DeviceIoControl 读取失败")
		t.mu.Unlock()
		return 0, dirs, events, stats
	}
	data := buf[:ret]
	if len(data) < 8 {
		return 0, dirs, events, stats
	}
	t.nextUSN = int64(binary.LittleEndian.Uint64(data[:8]))
	data = data[8:]
	off := 0
	opened := 0
	for off+60 <= len(data) {
		recLen := int(binary.LittleEndian.Uint32(data[off : off+4]))
		if recLen < 60 || off+recLen > len(data) {
			break
		}
		major := binary.LittleEndian.Uint16(data[off+4 : off+6])
		var frn, parent uint64
		var reason uint32
		var nameLen, nameOff int
		if major == 2 {
			frn = binary.LittleEndian.Uint64(data[off+8 : off+16])
			parent = binary.LittleEndian.Uint64(data[off+16 : off+24])
			reason = binary.LittleEndian.Uint32(data[off+40 : off+44])
			nameLen = int(binary.LittleEndian.Uint16(data[off+56 : off+58]))
			nameOff = int(binary.LittleEndian.Uint16(data[off+58 : off+60]))
		} else if major == 3 {
			frn = binary.LittleEndian.Uint64(data[off+8 : off+16])
			parent = binary.LittleEndian.Uint64(data[off+24 : off+32])
			reason = binary.LittleEndian.Uint32(data[off+56 : off+60])
			nameLen = int(binary.LittleEndian.Uint16(data[off+72 : off+74]))
			nameOff = int(binary.LittleEndian.Uint16(data[off+74 : off+76]))
		} else {
			off += recLen
			continue
		}
		stats.Records++
		var name string
		if nameLen > 0 && off+nameOff+nameLen <= len(data) {
			u := make([]uint16, nameLen/2)
			for i := 0; i < len(u); i++ {
				u[i] = binary.LittleEndian.Uint16(data[off+nameOff+i*2 : off+nameOff+i*2+2])
			}
			name = syscall.UTF16ToString(u)
		}
		off += recLen

		parentPath := t.fileNameByID(parent)
		path := ""
		if parentPath != "" && name != "" {
			path = filepath.Join(parentPath, name)
		} else {
			path = t.fileNameByID(frn)
		}
		opened++
		if path == "" {
			continue
		}
		stats.WithPath++
		var growth uint64
		if reason&writeMask != 0 && opened <= 1200 {
			cur := t.sizeOf(frn)
			if cur >= 0 {
				stats.SizeOK++
				prev := t.sizes[path]
				if uint64(cur) > prev {
					growth = uint64(cur) - prev
				}
				t.sizes[path] = uint64(cur)
				if len(t.sizes) > 20000 {
					t.sizes = map[string]uint64{}
				}
			} else {
				stats.SizeFail++
			}
		}
		if reason&reasonFileDelete != 0 {
			delete(t.sizes, path)
		}
		if growth > 0 {
			stats.Growth += growth
			dir := filepath.Dir(path)
			dirs[dir] += growth
			if growth >= minDelta {
				events = append(events, UsnEvent{Path: path, Bytes: growth, Reason: reasonText(reason)})
			}
		}
	}
	// 事件按大小排序取前 5
	for i := 0; i < len(events); i++ {
		for j := i + 1; j < len(events); j++ {
			if events[j].Bytes > events[i].Bytes {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
	if len(events) > 5 {
		events = events[:5]
	}
	return stats.Records, dirs, events, stats
}

func (t *UsnTailer) Close() {
	if t.h != 0 {
		pCloseHandle.Call(uintptr(t.h))
		t.h = 0
	}
}

// 自检：写 32MB → 看多久能捕获
func usnSelftest(cfg Config) int {
	if !isElevated() {
		fmt.Println("需要管理员权限运行 usn-selftest")
		return 2
	}
	t, err := newUsnTailer(rootVolume(getConfig().Root))
	if err != nil {
		fmt.Println("USN 打开失败:", err)
		return 2
	}
	defer t.Close()
	fmt.Printf("USN 已打开：NextUsn=%d\n", t.nextUSN)
	tmp := filepath.Join(cfg.DataDir, "usn-test.bin")
	f, err := createAndFill(tmp, 32<<20)
	if err != nil {
		fmt.Println("写测试文件失败:", err)
		return 2
	}
	_ = f
	fmt.Printf("已写入 32 MB：%s\n", tmp)
	start := time.Now()
	var found bool
	for i := 0; i < 20 && !found; i++ {
		_, _, events, st := t.Poll(1 << 20)
		fmt.Printf("轮询#%d 记录=%d 有路径=%d 增长=%s\n", i+1, st.Records, st.WithPath, human(st.Growth))
		for _, e := range events {
			if strings.Contains(e.Path, "usn-test.bin") {
				found = true
				fmt.Printf("[OK] %.1fs 内捕获：%s +%s [%s]\n",
					time.Since(start).Seconds(), e.Path, human(e.Bytes), e.Reason)
			}
		}
		time.Sleep(time.Second)
	}
	_ = removeFile(tmp)
	if !found {
		fmt.Println("[FAIL] 20 秒内未捕获")
		return 1
	}
	return 0
}
