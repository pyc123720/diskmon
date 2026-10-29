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
// 判据只有两条是硬要求（journalID 相同、Next 相同），另外两条是把
// 「日志被换过 / 坐标已经不可解释」这两种情况提前挡掉。
func anchorStillClean(prev, cur usnAnchor) (bool, string) {
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
	if cur.Next != prev.Next {
		return false, "锚点之后有新的 USN 记录"
	}
	// 到这里 Next 相同 —— 日志里没有新记录，也没有被清理过（清理本身由写入触发）。
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
