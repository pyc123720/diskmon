package main

import (
	"sync"
	"sync/atomic"
	"syscall"
)

// 硬链接去重。
//
// 背景：NTFS 的硬链接让同一份数据有多个路径，按路径求和会把一份数据算多次。
// 资源管理器在 C:\Windows\WinSxS 上就是这么虚高的——微软自己在 WinSxS 文档里
// 举的例子是「4.98 GB vs 实际开销 507 MB」。WizTree / WinDirStat / du -u /
// czkawka / Dism++ 都会去重，我们以前不会，于是 18.46 GB 这种数字既吓人又不真。
//
// 做法：记下「卷序列号 + 文件索引」这组唯一身份，第二次遇到同一身份时，
// 这份字节记入 Shared（被重复计入），不计入 Unique。
//
// 代价与边界：探测一个文件要一次元数据打开（FILE_READ_ATTRIBUTES，不读内容）。
// 为了不让全盘扫描慢一倍，默认只探测 >= hardlinkProbeMin 的文件——WinSxS 的膨胀
// 主要来自大二进制。因此 Shared 是【下限】：小文件里的硬链接没能发现。
// 报告层必须把这句话带上，否则就是拿一个新的不精确去换旧的不精确。

const hardlinkProbeMinDefault = 8 << 20 // 8 MB

// hardlinkProbeMin 是探测阈值（字节）。设为 0 表示全查——最准，但每文件一次打开。
var hardlinkProbeMin uint64 = hardlinkProbeMinDefault

type fileKey struct {
	vol  uint32
	high uint32
	low  uint32
}

// fileIdentity 返回文件在卷上的唯一身份与硬链接数。
// 任何一步失败都返回 ok=false：探测不了就当没探测到，绝不影响扫描本身。
func fileIdentity(path string) (key fileKey, links int, ok bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileKey{}, 0, false
	}
	const (
		fileReadAttributes = 0x0080
		shareAll           = 0x00000007 // READ | WRITE | DELETE
	)
	h, err := syscall.CreateFile(p, fileReadAttributes, shareAll, nil,
		syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fileKey{}, 0, false
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return fileKey{}, 0, false
	}
	return fileKey{
		vol:  info.VolumeSerialNumber,
		high: info.FileIndexHigh,
		low:  info.FileIndexLow,
	}, int(info.NumberOfLinks), true
}

// hardlinkTracker 是「本次扫描见过的文件身份」集合。
//
// 关键点：它必须在【整次扫描】范围内共享，而不是每个扫描分片各记一份。
// 并行拆分之后，C:\Windows\WinSxS 和 C:\Windows\System32 会落在不同任务是常态，
// 各记一份的话跨目录的硬链接就永远发现不了——本机实测会漏掉 2.37 GB。
type hardlinkTracker struct {
	mu   sync.Mutex
	seen map[fileKey]struct{}
}

func newHardlinkTracker() *hardlinkTracker {
	return &hardlinkTracker{seen: make(map[fileKey]struct{}, 4096)}
}

// scanHardlinks 是当前扫描作用域内的共享身份表。
var scanHardlinks atomic.Pointer[hardlinkTracker]

// beginHardlinkScope 开一个扫描级别的身份表，返回用于恢复的收尾函数。
func beginHardlinkScope() func() {
	prev := scanHardlinks.Swap(newHardlinkTracker())
	return func() { scanHardlinks.Store(prev) }
}

// activeHardlinks 取当前扫描的身份表；不在扫描作用域内时临时造一个
// （单次 scanVolume 调用内仍然有效，所以直接调用场景的去重行为不变）。
func activeHardlinks() *hardlinkTracker {
	if t := scanHardlinks.Load(); t != nil {
		return t
	}
	return newHardlinkTracker()
}

// observe 判断这个文件是不是「同一份数据的第二个名字」。
// 只做判重；重复字节由调用方记到自己那一份账上——这样并行时每个分片
// 各自贡献自己遇到的那部分，加起来正好等于全局 Shared。
func (t *hardlinkTracker) observe(path string, size uint64) bool {
	key, links, ok := fileIdentity(path)
	if !ok {
		return false
	}
	if links < 2 {
		// 单链接：同一份数据只有一个名字，不可能是重复计入
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, dup := t.seen[key]; dup {
		return true
	}
	t.seen[key] = struct{}{}
	return false
}

// uniqueBytes 返回「去掉已发现重复之后的占用」。
// 用减法而不是另外累加，保证 Unique + Shared == Total 这条恒等式永远成立。
func uniqueBytes(total, shared uint64) uint64 {
	if shared >= total {
		return 0
	}
	return total - shared
}
