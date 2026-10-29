package main

// NTFS MFT 直读（只读）。
//
// 目的只有一个：把「按目录逐项枚举」换成「顺序读一张表」。
// 目录遍历是 O(目录数) 次系统调用 + 随机 I/O；MFT 是一次顺序大块读 + 纯内存解析。
//
// **这里的纪律**：本文件只提供 `mft-dump` —— 只读地对账工具，不接进扫描路径。
// 先用它和 WalkDir 逐项比，差异为零之前，不把默认路径切过来。
// 需要管理员（卷句柄 \\.\X:），非 NTFS 或没权限一律返回错误，由调用方回落。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	fsctlGetNtfsVolumeData = 0x00090064
	fileRecordMagic        = 0x454C4946 // "FILE"
	attrFileName           = 0x30
	attrData               = 0x80
	attrEnd                = 0xFFFFFFFF

	flagInUse = 0x0001
	flagDir   = 0x0002

	rootRecord = 5 // 根目录固定是 5 号记录
)

var (
	kernel32Dev  = syscall.NewLazyDLL("kernel32.dll")
	procDeviceIo = kernel32Dev.NewProc("DeviceIoControl")
)

// ntfsVolumeData 对应 NTFS_VOLUME_DATA_BUFFER（96 字节）。
type ntfsVolumeData struct {
	VolumeSerialNumber           uint64
	NumberSectors                uint64
	TotalClusters                uint64
	FreeClusters                 uint64
	TotalReserved                uint64
	BytesPerSector               uint32
	BytesPerCluster              uint32
	BytesPerFileRecordSegment    uint32
	ClustersPerFileRecordSegment uint32
	MftValidDataLength           uint64
	MftStartLcn                  uint64
	Mft2StartLcn                 uint64
	MftZoneStart                 uint64
	MftZoneEnd                   uint64
}

func parseVolumeData(b []byte) (ntfsVolumeData, error) {
	var v ntfsVolumeData
	if len(b) < 96 {
		return v, fmt.Errorf("NTFS_VOLUME_DATA_BUFFER 太短：%d", len(b))
	}
	v.VolumeSerialNumber = binary.LittleEndian.Uint64(b[0:])
	v.NumberSectors = binary.LittleEndian.Uint64(b[8:])
	v.TotalClusters = binary.LittleEndian.Uint64(b[16:])
	v.FreeClusters = binary.LittleEndian.Uint64(b[24:])
	v.TotalReserved = binary.LittleEndian.Uint64(b[32:])
	v.BytesPerSector = binary.LittleEndian.Uint32(b[40:])
	v.BytesPerCluster = binary.LittleEndian.Uint32(b[44:])
	v.BytesPerFileRecordSegment = binary.LittleEndian.Uint32(b[48:])
	v.ClustersPerFileRecordSegment = binary.LittleEndian.Uint32(b[52:])
	v.MftValidDataLength = binary.LittleEndian.Uint64(b[56:])
	v.MftStartLcn = binary.LittleEndian.Uint64(b[64:])
	v.Mft2StartLcn = binary.LittleEndian.Uint64(b[72:])
	v.MftZoneStart = binary.LittleEndian.Uint64(b[80:])
	v.MftZoneEnd = binary.LittleEndian.Uint64(b[88:])
	if v.BytesPerCluster == 0 {
		return v, errors.New("簇大小为 0，卷参数不可信")
	}
	if v.BytesPerFileRecordSegment == 0 {
		// 少数卷用「每记录簇数」表示（负数表示 2^n）
		n := v.ClustersPerFileRecordSegment
		if n > 0 && n < v.BytesPerCluster {
			v.BytesPerFileRecordSegment = n
		} else {
			return v, fmt.Errorf("无法确定记录大小（BytesPerFileRecordSegment=0, Clusters=%d）", n)
		}
	}
	return v, nil
}

// mftRun 是 $MFT 自身 $DATA 的一段连续区间。
type mftRun struct {
	vcn      uint64
	lcn      uint64
	clusters uint64
}

// decodeRuns 解析 NTFS data runs：头字节高 4 位是偏移长度，低 4 位是长度字节数。
func decodeRuns(b []byte) ([]mftRun, error) {
	var runs []mftRun
	var vcn uint64
	var lcn int64
	for i := 0; i < len(b); {
		head := b[i]
		if head == 0 {
			break
		}
		i++
		lenSize := int(head & 0x0F)
		offSize := int(head >> 4)
		if lenSize == 0 || lenSize > 8 || offSize > 8 || i+lenSize+offSize > len(b) {
			return nil, fmt.Errorf("data run 越界或字段非法（lenSize=%d, offSize=%d）", lenSize, offSize)
		}
		var clusters uint64
		for k := 0; k < lenSize; k++ {
			clusters |= uint64(b[i+k]) << (8 * k)
		}
		i += lenSize
		var delta int64
		for k := 0; k < offSize; k++ {
			delta |= int64(b[i+k]) << (8 * k)
		}
		// 符号扩展（偏移是有符号的相对量）
		shift := uint(64 - 8*offSize)
		delta = delta << shift >> shift
		i += offSize
		if offSize > 0 {
			lcn += delta
		}
		runs = append(runs, mftRun{vcn: vcn, lcn: uint64(lcn), clusters: clusters})
		vcn += clusters
	}
	if len(runs) == 0 {
		return nil, errors.New("data run 为空")
	}
	return runs, nil
}

// mftFile 是从一条记录里读出来的最小信息。
type mftFile struct {
	Record uint64
	Parent uint64
	Name   string
	IsDir  bool
	Size   uint64
	Links  int
	// 修改时间（$FILE_NAME 里的那份），unix 秒
	MTime int64
	// 尺寸是从 $FILE_NAME 的 RealSize 取的，而非 $DATA 属性。
	// 属性列表把 $DATA 溢出到扩展记录时，主记录里没有 $DATA，只有 $FILE_NAME 还留着尺寸。
	SizeFromName bool
}

// applyFixups 把记录尾部被更新序列号占用的两个字节换回来。
// 换不回来说明这条记录在写入中途被读到——跳过即可，不要崩。
func applyFixups(rec []byte, sectorSize int) bool {
	if len(rec) < 8 || sectorSize <= 0 {
		return false
	}
	usaOff := int(binary.LittleEndian.Uint16(rec[4:6]))
	usaCnt := int(binary.LittleEndian.Uint16(rec[6:8]))
	if usaOff <= 0 || usaCnt < 2 || usaOff+usaCnt*2 > len(rec) {
		return false
	}
	usn := binary.LittleEndian.Uint16(rec[usaOff : usaOff+2])
	for i := 1; i < usaCnt; i++ {
		pos := i*sectorSize - 2
		if pos <= 0 || pos+2 > len(rec) {
			return false
		}
		if binary.LittleEndian.Uint16(rec[pos:pos+2]) != usn {
			return false
		}
		copy(rec[pos:pos+2], rec[usaOff+i*2:usaOff+i*2+2])
	}
	return true
}

// parseRecord 解析一条 FILE 记录。第二个返回值表示「可以用」。
func parseRecord(raw []byte) (mftFile, bool) {
	var f mftFile
	if len(raw) < 48 {
		return f, false
	}
	if binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return f, false
	}
	rec := make([]byte, len(raw))
	copy(rec, raw)
	applyFixups(rec, 512)

	first := int(binary.LittleEndian.Uint16(rec[0x14:0x16]))
	flags := binary.LittleEndian.Uint16(rec[0x16:0x18])
	baseRef := binary.LittleEndian.Uint64(rec[0x20:0x28]) & 0x0000FFFFFFFFFFFF
	links := int(binary.LittleEndian.Uint16(rec[0x12:0x14]))

	if flags&flagInUse == 0 {
		return f, false
	}
	if baseRef != 0 {
		return f, false // 扩展记录，信息归到主记录
	}
	f.IsDir = flags&flagDir != 0
	f.Links = links

	if first <= 0 || first >= len(rec) {
		return f, false
	}

	bestNS := -1
	var bestParent uint64
	var bestName string
	var bestSize uint64
	var bestMTime int64
	var dataSize uint64

	for off := first; off+8 <= len(rec); {
		typ := binary.LittleEndian.Uint32(rec[off:])
		if typ == attrEnd {
			break
		}
		alen := int(binary.LittleEndian.Uint32(rec[off+4:]))
		if alen < 8 || off+alen > len(rec) {
			break
		}
		nonRes := rec[off+8]
		switch typ {
		case attrFileName:
			if nonRes == 0 {
				vlen := int(binary.LittleEndian.Uint32(rec[off+16:]))
				voff := int(binary.LittleEndian.Uint16(rec[off+20:]))
				start := off + voff
				if voff > 0 && vlen >= 0x42 && start+vlen <= len(rec) {
					v := rec[start : start+vlen]
					parent := binary.LittleEndian.Uint64(v[0:8]) & 0x0000FFFFFFFFFFFF
					// $FILE_NAME 里也带一份尺寸：0x28 AllocatedSize、0x30 RealSize。
					// 属性列表把 $DATA 挪进扩展记录时，这份是仅存的尺寸来源。
					realSize := binary.LittleEndian.Uint64(v[0x30:0x38])
					// $FILE_NAME 里也带修改时间（0x10），用于按日期统计写入量
					mt := int64(binary.LittleEndian.Uint64(v[0x10:0x18]))
					nameLen := int(v[0x40])
					ns := int(v[0x41])
					if 0x42+nameLen*2 <= len(v) {
						u := make([]uint16, nameLen)
						for i := 0; i < nameLen; i++ {
							u[i] = binary.LittleEndian.Uint16(v[0x42+i*2:])
						}
						// 命名空间：优先 Win32(1) / Win32&DOS(3)，其次 POSIX(0)，DOS(2) 不要
						rank := map[int]int{1: 3, 3: 3, 0: 2}[ns]
						if ns != 2 && rank > bestNS {
							bestNS = rank
							bestParent = parent
							bestName = string(utf16.Decode(u))
							bestSize = realSize
							bestMTime = filetimeToUnix(mt)
						}
					}
				}
			}
		case attrData:
			if nonRes != 0 && dataSize == 0 {
				// 非驻留：真实大小在 +0x30
				if off+0x38 <= len(rec) {
					real := binary.LittleEndian.Uint64(rec[off+0x30:])
					if real > 0 {
						dataSize = real
					}
				}
			} else if nonRes == 0 && dataSize == 0 {
				if off+0x18 <= len(rec) {
					vlen := uint64(binary.LittleEndian.Uint32(rec[off+16:]))
					if vlen > 0 {
						dataSize = vlen
					}
				}
			}
		}
		off += alen
	}

	if bestNS < 0 && links == 0 {
		return f, false
	}
	f.Parent = bestParent
	f.Name = bestName
	f.MTime = bestMTime
	if !f.IsDir {
		// 优先用 $FILE_NAME 的 RealSize：扩展记录里的 $DATA 我们读不到。
		if bestSize > 0 {
			f.Size = bestSize
			if dataSize == 0 {
				f.SizeFromName = true
			}
		} else {
			f.Size = dataSize
		}
	}
	return f, true
}

// ---------- 卷 I/O ----------

func deviceIoControl(h syscall.Handle, code uint32, out []byte) (uint32, error) {
	var n uint32
	if len(out) == 0 {
		return 0, errors.New("输出缓冲区为空")
	}
	r1, _, e := procDeviceIo.Call(
		uintptr(h), uintptr(code),
		0, 0,
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&n)), 0,
	)
	if r1 == 0 {
		return 0, e
	}
	return n, nil
}

func readAt(h syscall.Handle, off int64, buf []byte) error {
	if _, err := syscall.Seek(h, off, 0); err != nil {
		return err
	}
	var n uint32
	if err := syscall.ReadFile(h, buf, &n, nil); err != nil {
		return err
	}
	if int(n) != len(buf) {
		return fmt.Errorf("短读：期望 %d，读到 %d", len(buf), n)
	}
	return nil
}

// openVolumeHandle 打开卷句柄。需要管理员；失败就是失败，调用方一律回落。
// 注意：usn.go 里已有 openVolume，这里另起名避免撞车。
func openVolumeHandle(letter string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(`\\.\` + letter + ":")
	if err != nil {
		return 0, err
	}
	const genericRead = 0x80000000
	const shareRW = 0x00000003
	h, err := syscall.CreateFile(p, genericRead, shareRW, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("打开卷句柄失败（需要管理员）：%w", err)
	}
	return h, nil
}

// readMftLayout 读出卷参数和 $MFT 自己的 data runs。
func readMftLayout(h syscall.Handle) (ntfsVolumeData, []mftRun, int, bool, error) {
	buf := make([]byte, 512)
	n, err := deviceIoControl(h, fsctlGetNtfsVolumeData, buf)
	if err != nil {
		return ntfsVolumeData{}, nil, 0, false, fmt.Errorf("FSCTL_GET_NTFS_VOLUME_DATA 失败：%w", err)
	}
	vd, err := parseVolumeData(buf[:n])
	if err != nil {
		return vd, nil, 0, false, err
	}

	rec := make([]byte, vd.BytesPerFileRecordSegment)
	off := int64(vd.MftStartLcn) * int64(vd.BytesPerCluster)
	if err := readAt(h, off, rec); err != nil {
		return vd, nil, 0, false, fmt.Errorf("读 $MFT 首记录失败：%w", err)
	}
	runs, segs, hasAttrList, err := extractMftRuns(rec)
	if err != nil {
		return vd, nil, segs, hasAttrList, err
	}
	return vd, runs, segs, hasAttrList, nil
}

// mftSeg 是 $MFT 自身 $DATA 的一个分段（NTFS 会在 runlist 过长时把它拆成多个属性）。
type mftSeg struct {
	startVCN uint64
	runs     []mftRun
}

// extractMftRuns 从 $MFT 的 0 号记录里取出它自己的非驻留 $DATA run list。
//
// 关键：$MFT 有 1 GB 以上，run list 早就超出单条 1 KB 记录能装下的量，
// NTFS 会把它拆成**多个 $DATA 分段**（各自的起始 VCN 不同）。
// 只取第一个分段会静悄悄丢掉卷的后半部分——本机实测在 C:\Windows\WinSxS
// 少算了 3.5 万个目录。所以这里必须把**所有**分段都收下来，按起始 VCN 拼回去。
func extractMftRuns(rec []byte) (runs []mftRun, segs int, hasAttrList bool, err error) {
	c := make([]byte, len(rec))
	copy(c, rec)
	applyFixups(c, 512)
	if len(c) < 48 || binary.LittleEndian.Uint32(c[0:4]) != fileRecordMagic {
		return nil, 0, false, errors.New("0 号记录不是 FILE 记录")
	}
	const attrListType = 0x20
	first := int(binary.LittleEndian.Uint16(c[0x14:0x16]))
	var parts []mftSeg
	for off := first; off+8 <= len(c); {
		typ := binary.LittleEndian.Uint32(c[off:])
		if typ == attrEnd {
			break
		}
		alen := int(binary.LittleEndian.Uint32(c[off+4:]))
		if alen < 8 || off+alen > len(c) {
			break
		}
		if typ == attrListType {
			// 出现属性列表说明还有分段落在扩展记录里，那部分本次读不到，要报出来
			hasAttrList = true
		}
		if typ == attrData && c[off+8] != 0 {
			rlOff := int(binary.LittleEndian.Uint16(c[off+0x20:]))
			if rlOff <= 0 || off+rlOff >= len(c) {
				return nil, len(parts), hasAttrList, errors.New("$MFT 的 run list 偏移非法")
			}
			startVCN := binary.LittleEndian.Uint64(c[off+0x10:])
			part, derr := decodeRuns(c[off+rlOff : off+alen])
			if derr != nil {
				return nil, len(parts), hasAttrList, derr
			}
			parts = append(parts, mftSeg{startVCN: startVCN, runs: part})
		}
		off += alen
	}
	if len(parts) == 0 {
		return nil, 0, hasAttrList, errors.New("$MFT 没有非驻留 $DATA（卷异常？）")
	}
	// 按起始 VCN 排序后首尾相接，避免 attributes 顺序和 VCN 顺序不一致
	sort.Slice(parts, func(i, j int) bool { return parts[i].startVCN < parts[j].startVCN })
	for _, p := range parts {
		for _, r := range p.runs {
			r.vcn += p.startVCN
			runs = append(runs, r)
		}
	}
	return runs, len(parts), hasAttrList, nil
}

// scanRecords 按 run 顺序流式读出所有记录，回调每条。
func scanRecords(h syscall.Handle, vd ntfsVolumeData, runs []mftRun, fn func(recNo uint64, raw []byte)) error {
	recSize := int(vd.BytesPerFileRecordSegment)
	cluster := int64(vd.BytesPerCluster)
	const chunkClusters = 256 // 每次读 256 簇，避免一次吃掉几百 MB

	var recNo uint64
	buf := make([]byte, chunkClusters*int(cluster))
	for _, r := range runs {
		if r.clusters == 0 {
			continue
		}
		base := int64(r.lcn) * cluster
		remain := r.clusters
		for remain > 0 {
			n := remain
			if n > chunkClusters {
				n = chunkClusters
			}
			bytesN := int(n) * int(cluster)
			b := buf[:bytesN]
			if err := readAt(h, base, b); err != nil {
				return err
			}
			for i := 0; i+recSize <= len(b); i += recSize {
				fn(recNo, b[i:i+recSize])
				recNo++
			}
			base += int64(bytesN)
			remain -= n
		}
	}
	return nil
}

// buildPaths 把 record → (parent, name) 还原成绝对路径。
// 顶层名字以 $ 开头的是 NTFS 元文件，一律返回空串：它们不是用户可见文件，
// 而且 $BadClus 这种稀疏文件的逻辑大小等于整个卷，算进去会凭空多出一个盘。
func buildPaths(files map[uint64]mftFile, volume string) map[uint64]string {
	paths := make(map[uint64]string, len(files))
	var resolve func(rec uint64, depth int) string
	resolve = func(rec uint64, depth int) string {
		if p, ok := paths[rec]; ok {
			return p
		}
		if rec == rootRecord || depth > 512 {
			paths[rec] = volume
			return volume
		}
		f, ok := files[rec]
		if !ok || f.Name == "" {
			return ""
		}
		// 元文件判定只走白名单，不要在这里再写一遍「$ 开头」——
		// 曾经两处各写一份，改了一处另一处照旧生效，$Recycle.Bin 就这么丢了两次。
		if f.Parent == rootRecord && ntfsMetaNames[strings.ToLower(f.Name)] {
			paths[rec] = ""
			return ""
		}
		parent := resolve(f.Parent, depth+1)
		if parent == "" {
			paths[rec] = ""
			return ""
		}
		p := filepath.Join(parent, f.Name)
		paths[rec] = p
		return p
	}
	for rec := range files {
		resolve(rec, 0)
	}
	return paths
}

// literalDirFlag 只看记录头里的目录标志，不要求整条记录能解析。
// 用途：一条记录解析失败、父引用也丢了，但它的目录标志还在——那它就是目录，
// 应当被计数，而不是从统计里消失。
func literalDirFlag(raw []byte) bool {
	if len(raw) < 0x18 || binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return false
	}
	flags := binary.LittleEndian.Uint16(raw[0x16:])
	return flags&flagInUse != 0 && flags&flagDir != 0
}

// inUseRecord 只看记录头判断这条槽位是否在用，不做完整解析。
// 有了它才能把「空闲槽位」和「解析失败」分开计——否则「有在用记录解析失败」
// 这个可能性永远排除不掉，而它正是体积少算的候选原因之一。
func inUseRecord(raw []byte) bool {
	if len(raw) < 0x18 {
		return false
	}
	if binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return false
	}
	return binary.LittleEndian.Uint16(raw[0x16:0x18])&flagInUse != 0
}

// topComponent 取路径第一段（C:\Windows\a\b → Windows）；根目录下的散文件归到一处。
func topComponent(volume, p string) string {
	rel := strings.TrimPrefix(p, volume)
	if rel == "" || rel == p {
		return ""
	}
	if i := strings.IndexByte(rel, '\\'); i >= 0 {
		return rel[:i]
	}
	return rootBucket
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func signedGB(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("%s%.2f GB", sign, float64(n)/(1<<30))
}

// classifyReject 给一条被 parseRecord 拒掉的记录归个类。
// 「解析失败」这个数字混着「扩展记录」和「真的坏了」两种完全不同的东西：
// 扩展记录（属性列表溢出）是 NTFS 的正常结构，跳过是对的，不会丢体积；
// 只有后面几类才是真的漏读。不拆开就只能凭一个数字吓自己。
func classifyReject(raw []byte) string {
	if len(raw) < 0x18 {
		return "缓冲过短"
	}
	if binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return "魔数不对"
	}
	if binary.LittleEndian.Uint64(raw[0x20:0x28])&0x0000FFFFFFFFFFFF != 0 {
		return "扩展记录（正常跳过）"
	}
	first := int(binary.LittleEndian.Uint16(raw[0x14:0x16]))
	if first <= 0 || first >= len(raw) {
		return "首属性偏移非法"
	}
	return "其他（无文件名且无链接）"
}

// parseExtData 从一条「扩展记录」里取出它替主记录保管的 $DATA 真实大小。
//
// 属性列表（$ATTRIBUTE_LIST）把 $DATA 溢出到扩展记录时，主记录里就没有 $DATA 了。
// 如果那条记录的 $FILE_NAME 尺寸又恰好是 0，这个文件的体积在我们这里就只能是 0。
// 实测样本：C:\hiberfil.sys —— 6.08 GB，记录 3665，路径还原完全正常，尺寸报 0。
func parseExtData(raw []byte) (base uint64, size uint64, ok bool) {
	if len(raw) < 0x30 {
		return 0, 0, false
	}
	if binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return 0, 0, false
	}
	if !inUseRecord(raw) {
		return 0, 0, false
	}
	base = binary.LittleEndian.Uint64(raw[0x20:0x28]) & 0x0000FFFFFFFFFFFF
	if base == 0 {
		return 0, 0, false
	}
	rec := make([]byte, len(raw))
	copy(rec, raw)
	applyFixups(rec, 512)

	first := int(binary.LittleEndian.Uint16(rec[0x14:0x16]))
	if first <= 0 || first >= len(rec) {
		return 0, 0, false
	}
	for off := first; off+8 <= len(rec); {
		typ := binary.LittleEndian.Uint32(rec[off:])
		if typ == attrEnd {
			break
		}
		alen := int(binary.LittleEndian.Uint32(rec[off+4:]))
		if alen < 8 || off+alen > len(rec) {
			break
		}
		if typ == attrData && rec[off+8] != 0 && off+0x38 <= len(rec) {
			if real := binary.LittleEndian.Uint64(rec[off+0x30:]); real > size {
				size = real
			}
		}
		off += alen
	}
	return base, size, size > 0
}

// parseExtName 从扩展记录里取出它替主记录保管的 $FILE_NAME。
//
// 属性溢出时，名字可能只落在扩展记录上；不捞回来，主记录就没有名字、会被整条丢弃，
// 于是它下面的**整棵子树**都拼不出路径——$Recycle.Bin 和 WindowsApps 就是这样丢的。
func parseExtName(raw []byte) (base, parent uint64, name string, isDir bool, ok bool) {
	if len(raw) < 0x30 || binary.LittleEndian.Uint32(raw[0:4]) != fileRecordMagic {
		return 0, 0, "", false, false
	}
	if !inUseRecord(raw) {
		return 0, 0, "", false, false
	}
	base = binary.LittleEndian.Uint64(raw[0x20:0x28]) & 0x0000FFFFFFFFFFFF
	if base == 0 {
		return 0, 0, "", false, false
	}
	rec := make([]byte, len(raw))
	copy(rec, raw)
	applyFixups(rec, 512)
	isDir = binary.LittleEndian.Uint16(rec[0x16:])&flagDir != 0
	first := int(binary.LittleEndian.Uint16(rec[0x14:0x16]))
	if first <= 0 || first >= len(rec) {
		return 0, 0, "", false, false
	}
	for off := first; off+8 <= len(rec); {
		typ := binary.LittleEndian.Uint32(rec[off:])
		if typ == attrEnd {
			break
		}
		alen := int(binary.LittleEndian.Uint32(rec[off+4:]))
		if alen < 8 || off+alen > len(rec) {
			break
		}
		if typ == attrFileName {
			valOff := int(binary.LittleEndian.Uint16(rec[off+0x14:]))
			vs := off + valOff
			if vs+0x42 <= len(rec) {
				nlen := int(rec[vs+0x40])
				ns := rec[vs+0x41]
				if nlen > 0 && vs+0x42+nlen*2 <= len(rec) && ns != 2 {
					u := make([]uint16, nlen)
					for i := 0; i < nlen; i++ {
						u[i] = binary.LittleEndian.Uint16(rec[vs+0x42+i*2:])
					}
					return base, binary.LittleEndian.Uint64(rec[vs:]), string(utf16.Decode(u)), isDir, true
				}
			}
		}
		off += alen
	}
	return 0, 0, "", false, false
}

// twoComponent 取「卷下的前两级目录」，例如 Windows\WinSxS。
// 只在顶层目录内部继续定位时才用得到；层级不足两级返回空串。
func twoComponent(volume, p string) string {
	rel := strings.Trim(strings.TrimPrefix(p, volume), `\`)
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, `\`)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + `\` + parts[1]
}

// filetimeToUnix 把 NTFS 的 FILETIME（1601 起的 100ns）换成 unix 秒。
// 0 或明显越界的值返回 0，让调用方自己决定怎么处理。
func filetimeToUnix(ft int64) int64 {
	if ft <= 0 {
		return 0
	}
	// 11644473600 = 1601-01-01 到 1970-01-01 的秒数
	u := ft/10000000 - 11644473600
	if u < 0 {
		return 0
	}
	return u
}

// inspectRejected 检查一条被拒记录里到底装了什么。
//
// 真正的扩展记录（$ATTRIBUTE_LIST 溢出）**只装溢出的属性**，身上不会有 $FILE_NAME。
// 如果一条记录既有 $FILE_NAME 又带目录标志，那它就是个正经目录——被我们判错了。
func inspectRejected(raw []byte) (hasName, isDir bool, parent uint64) {
	if len(raw) < 0x30 {
		return false, false, 0
	}
	c := make([]byte, len(raw))
	copy(c, raw)
	if binary.LittleEndian.Uint32(c[0:4]) != fileRecordMagic {
		return false, false, 0
	}
	applyFixups(c, 512)
	const flagDir = 0x0002 // FILE_NAME_INDEX_PRESENT：置位表示这条记录是目录
	isDir = binary.LittleEndian.Uint16(c[0x16:])&flagDir != 0
	first := int(binary.LittleEndian.Uint16(c[0x14:0x16]))
	for off := first; off+8 <= len(c); {
		typ := binary.LittleEndian.Uint32(c[off:])
		if typ == attrEnd {
			break
		}
		alen := int(binary.LittleEndian.Uint32(c[off+4:]))
		if alen < 8 || off+alen > len(c) {
			break
		}
		if typ == attrFileName {
			hasName = true
			// 只取父记录号，用于把这条记录归到某个顶层目录；名字本身这里用不到
			valOff := int(binary.LittleEndian.Uint16(c[off+0x14:]))
			if valOff > 0 && off+valOff+8 <= len(c) {
				parent = binary.LittleEndian.Uint64(c[off+valOff:])
			}
			break
		}
		off += alen
	}
	return hasName, isDir, parent
}

// ntfsMetaNames 是真正的 NTFS 元文件名。**必须白名单，不能用「$ 开头」判断**：
// $Recycle.Bin、$WinREAgent、$Windows.~BT、$GetCurrent 这些名字以 $ 开头，
// 但它们是普通目录、装着用户数据，按前缀排除会把整棵子树从结果里抹掉。
var ntfsMetaNames = map[string]bool{
	"$mft": true, "$mftmirr": true, "$logfile": true, "$volume": true,
	"$attrdef": true, "$bitmap": true, "$boot": true, "$badclus": true,
	"$secure": true, "$upcase": true, "$extend": true, "$objid": true,
	"$quota": true, "$reparse": true, "$usnjrnl": true, "$rmmetadata": true,
	"$txflog": true, "$deleted": true,
}

// isMetadataRecord 判断一条记录是否落在 NTFS 元文件树里（按白名单，不按前缀）。
func isMetadataRecord(files map[uint64]mftFile, rec uint64) bool {
	for depth := 0; depth < 512; depth++ {
		f, ok := files[rec]
		if !ok {
			return false
		}
		if f.Parent == rootRecord {
			return len(f.Name) > 0 && ntfsMetaNames[strings.ToLower(f.Name)]
		}
		rec = f.Parent
	}
	return false
}

// MftResult 是一次 MFT 直读的统计结果。
type MftResult struct {
	Files      int
	Dirs       int
	Total      uint64
	Records    uint64
	Elapsed    time.Duration
	Volume     string
	RecordSize uint32
	MftBytes   uint64
	// NTFS 元文件（$MFT / $BadClus / $Extend …）单独统计，不混进用户文件
	MetaFiles int
	MetaBytes uint64
	// 路径还原不出来的记录（父引用损坏 / 祖先链断裂）
	Unresolved int
	// 记录账：records = 在用 + 空闲 + 解析失败。三者必须相等。
	// 之前把「空闲槽位」和「解析失败」合成一个 dropped，导致无法排除
	// 「有在用记录解析失败」这一可能——而它正是体积少算的候选原因之一。
	FreeRecords  uint64
	ParseFailed  uint64
	DroppedTotal uint64
	// 尺寸取自 $FILE_NAME 而非 $DATA 的文件（$DATA 溢出到扩展记录的那批）
	ByNameOnly  int
	ByNameBytes uint64
	// 尺寸是从扩展记录的 $DATA 补回来的（主记录里根本没有 $DATA）
	FromExtOnly  int
	FromExtBytes uint64
	// 名字只落在扩展记录上、被从扩展记录救回来的主记录（不救则整棵子树丢失）
	ExtNameRecovered     int
	ExtNameRecoveredDirs int
	// 硬链接数 >1 的记录，以及「若每个名字都算一条路径」会多出的条数。
	// 注意：这**不是**遍历实际多算的字节——大部分链接的另一个名字对遍历不可见
	// （权限、卷影副本），所以实际差值远小于此。真正的实际差值看「文件数差异」那一行。
	MultiLink      int
	MultiLinkExtra int
	// 按顶层目录（C:\Windows、C:\Users …）求体积，用于和目录遍历分桶对账
	Buckets map[string]uint64
	// 目录数也按顶层目录分开记：只看一个总数分不出「谁多算了」，
	// 分桶之后 3.5 万个目录的差会自己指出位置。
	DirBuckets map[string]uint64
	// $MFT 自身 $DATA 的分段情况——分段没读全，卷尾的记录就会整片丢失
	RunsSegments int
	HasAttrList  bool
	Runs         int
	ExpectedRecs uint64
	// 被拒记录的成分：真正扩展记录不带 $FILE_NAME，带了就说明我们判错了
	RejectWithName    uint64
	RejectDir         uint64
	RejectDirWithName uint64
	RejectBuckets     map[string]uint64
	// 路径还原不出来的记录按父目录归类：直接指出这批目录丢在哪棵子树
	UnresolvedBuckets map[string]uint64
	// 被拒记录里「带名字的目录」样本，用来一眼判断那批到底是些什么东西
	RejectDirSamples []string
	// 解析失败、但目录标志明确的记录数：它们是目录，只是定位不到，以前被静默丢弃
	DirUnplaced uint64
	// 试算：被拒记录里「带名字+目录标志」的若被接受，会新增多少目录、落在哪些子树
	TrialDir     uint64
	TrialBuckets map[string]uint64
	// 再往下钻一级（Windows\WinSxS 这种），用于在单个顶层目录内部继续定位
	DirBuckets2 map[string]uint64
	// 解析失败按原因分类，用来区分「扩展记录（正常跳过）」和「真的漏读」
	RejectReasons map[string]uint64
	// 体积最大的几条路径，用来人工核对口径
	Top []mftTop
	// 根目录下的条目（MFT 视角），用来和资源管理器逐个对照
	RootEntries []mftTop
	RootCount   int
	// 定点查询结果（命令行给了文件名才有）
	Probe mftProbe
}

// rootBucket 是「根目录下的散文件」在分桶里用的名字。
const rootBucket = "<根目录文件>"

// mftProbe 是对某个文件名的定点查询结果。
// 它的用处是把两种完全不同的失败分开——「记录根本没读到」和「读到了但路径没还原」，
// 这两种情况查法差得很远，只看总数永远分不清。
type mftProbe struct {
	Name        string
	Matched     int
	Record      uint64
	Parent      uint64
	ParentKnown bool
	Size        uint64
	Links       int
	IsDir       bool
	Path        string
}

// mftTop 是「最大条目」的一行。
type mftTop struct {
	Path string
	Size uint64
}

func readMft(letter, probeName string) (res *MftResult, paths map[uint64]string, files map[uint64]mftFile, err error) {
	h, err := openVolumeHandle(letter)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	vd, runs, segs, hasAttrList, err := readMftLayout(h)
	if err != nil {
		return nil, nil, nil, err
	}

	t0 := time.Now()
	files = make(map[uint64]mftFile, 1<<20)
	var records uint64
	var freeRecords, parseFailed uint64
	reasons := make(map[string]uint64, 8)
	// 扩展记录替主记录保管的 $DATA 尺寸：baseRef → 最大 realSize
	extData := make(map[uint64]uint64, 1<<16)
	// 扩展记录替主记录保管的 $FILE_NAME：baseRef → (父目录, 名字, 是否目录)
	type extNameRec struct {
		parent uint64
		name   string
		isDir  bool
	}
	extName := make(map[uint64]extNameRec, 1<<12)
	// 被拒记录到底是不是真扩展记录：成分统计 + 归属（baseRef → 条数）
	var rejectWithName, rejectDir, rejectDirWithName uint64
	// 解析失败但目录标志置位的记录（父引用丢失、无法定位）
	var unplacedDirs uint64
	rejectBase := make(map[uint64]uint64, 1<<14)
	// 试算：带名字+目录标志的被拒记录，按父记录记数（只统计，不计入结果）
	trialParent := make(map[uint64]uint64, 1<<13)
	// 被拒记录里「带名字的目录」样本——名字一看，性质就定了，不用再猜
	var rejectDirSamples []string
	err = scanRecords(h, vd, runs, func(recNo uint64, raw []byte) {
		records++
		// 先判「在用没有」再解析，才能把「空闲槽位」和「解析失败」分开计。
		if !inUseRecord(raw) {
			freeRecords++
			return
		}
		f, ok := parseRecord(raw)
		if !ok {
			// 扩展记录不要白扔——它身上可能有主记录缺的那个 $DATA 尺寸
			if base, sz, eok := parseExtData(raw); eok {
				if sz > extData[base] {
					extData[base] = sz
				}
				rejectBase[base]++
			}
			if base, par, nm, isd, eok := parseExtName(raw); eok {
				if _, exists := extName[base]; !exists {
					extName[base] = extNameRec{parent: par, name: nm, isDir: isd}
				}
				if isd && len(rejectDirSamples) < 12 {
					rejectDirSamples = append(rejectDirSamples, nm)
				}
			}
			if hn, hd, par := inspectRejected(raw); hn {
				rejectWithName++
				if hd {
					rejectDirWithName++
					if par != 0 {
						trialParent[par]++
					}
				}
			} else if hd {
				rejectDir++
			}
			// 解析失败但**明确带目录标志**的，是目录，只是父引用丢了、定位不到。
			// 以前它们悄无声息地消失，让目录数凭空少了两万多。
			if hd := literalDirFlag(raw); hd {
				unplacedDirs++
			}
			reasons[classifyReject(raw)]++
			parseFailed++
			return
		}
		f.Record = recNo
		files[recNo] = f
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// 名字只落在扩展记录上的主记录，用扩展记录的名字把它救回来。
	// 不救的话它根本不进 files，它下面的整棵子树都还原不出路径。
	var extNameRecovered, extNameRecoveredDirs int
	for base, e := range extName {
		if _, ok := files[base]; ok {
			continue
		}
		files[base] = mftFile{Record: base, Parent: e.parent, Name: e.name, IsDir: e.isDir}
		extNameRecovered++
		if e.isDir {
			extNameRecoveredDirs++
		}
	}

	// 主记录没有 $DATA、$FILE_NAME 又是 0 的，用扩展记录里的尺寸补上。
	// 只补「当前尺寸为 0 且确实存在扩展数据」的那种，不碰其它记录。
	var fromExtOnly int
	var fromExtBytes uint64
	for rec, f := range files {
		if f.IsDir || f.Size != 0 {
			continue
		}
		if ext, ok := extData[rec]; ok && ext > 0 {
			f.Size = ext
			f.SizeFromName = false
			files[rec] = f
			fromExtOnly++
			fromExtBytes += ext
		}
	}

	volume := letter + `:\`
	paths = buildPaths(files, volume)

	// 路径还原不出来的记录，按**父记录**归到顶层目录上。
	// 这一栏就是「MFT 比遍历少的那批目录」的去向，直接指出丢在哪棵子树。
	unresolvedBuckets := make(map[string]uint64, 16)
	var unresolvedTotal uint64
	for rec := range files {
		if paths[rec] != "" {
			continue
		}
		// 元文件的路径是**故意**置空的（它们不算用户数据），
		// 混进这一栏会把真正的信号淹掉——5.4 万条元文件曾经盖住了全部。
		if isMetadataRecord(files, rec) {
			continue
		}
		unresolvedTotal++
		if p := paths[files[rec].Parent]; p != "" {
			unresolvedBuckets[topComponent(volume, p)]++
		} else {
			unresolvedBuckets["<父目录也还原不出>"]++
		}
	}

	// 用 base 记录的路径，把被拒记录归到顶层目录上——一跑就知道它们扎堆在哪。
	rejectBuckets := make(map[string]uint64, 16)
	for base, n := range rejectBase {
		if p := paths[base]; p != "" {
			rejectBuckets[topComponent(volume, p)] += n
		} else {
			rejectBuckets["<主记录也还原不出路径>"] += n
		}
	}

	// 试算：这些「带名字 + 目录标志」的被拒记录如果被接受，会落在哪些子树
	trialBuckets := make(map[string]uint64, 16)
	var trialTotal uint64
	for par, n := range trialParent {
		trialTotal += n
		if p := paths[par]; p != "" {
			trialBuckets[topComponent(volume, p)] += n
		} else {
			trialBuckets["<父目录也还原不出路径>"] += n
		}
	}

	res = &MftResult{
		Records:      records,
		FreeRecords:  freeRecords,
		ParseFailed:  parseFailed,
		DroppedTotal: freeRecords + parseFailed,
		Elapsed:      time.Since(t0),
		Volume:       volume,
		RecordSize:   vd.BytesPerFileRecordSegment,
		MftBytes:     vd.MftValidDataLength,
		RunsSegments: segs,
		HasAttrList:  hasAttrList,
		Runs:         len(runs),
		ExpectedRecs: vd.MftValidDataLength / uint64(vd.BytesPerFileRecordSegment),

		RejectWithName:    rejectWithName,
		RejectDir:         rejectDir,
		RejectDirWithName: rejectDirWithName,
		RejectBuckets:     rejectBuckets,

		ExtNameRecovered:     extNameRecovered,
		ExtNameRecoveredDirs: extNameRecoveredDirs,
		UnresolvedBuckets:    unresolvedBuckets,
		RejectDirSamples:     rejectDirSamples,
		DirUnplaced:          unplacedDirs,

		TrialDir:     trialTotal,
		TrialBuckets: trialBuckets,
	}
	top := make([]mftTop, 0, 8)
	rootAll := make([]mftTop, 0, 64)
	buckets := make(map[string]uint64, 32)
	res.DirBuckets = make(map[string]uint64, 32)
	res.DirBuckets2 = make(map[string]uint64, 4096)
	for rec, f := range files {
		if paths[rec] == "" {
			if isMetadataRecord(files, rec) {
				res.MetaFiles++
				// $BadClus 是稀疏文件，逻辑尺寸等于整个卷的容量——卷多大它多大。
				// 把它加进「元文件占用」，就会得出「205 GB 是系统元数据」这种失真结论。
				// 它实际不占那部分空间，所以只计条数、不计尺寸。
				if !strings.EqualFold(f.Name, "$BadClus") {
					res.MetaBytes += f.Size
				}
			} else {
				res.Unresolved++
			}
			continue
		}
		if f.IsDir {
			res.Dirs++
			res.DirBuckets[topComponent(volume, paths[rec])]++
			if k := twoComponent(volume, paths[rec]); k != "" {
				res.DirBuckets2[k]++
			}
		} else {
			res.Files++
			res.Total += f.Size
			if f.SizeFromName {
				res.ByNameOnly++
				res.ByNameBytes += f.Size
			}
			top = append(top, mftTop{paths[rec], f.Size})
			if f.Links > 1 {
				res.MultiLink++
				res.MultiLinkExtra += f.Links - 1
			}
			bk := topComponent(volume, paths[rec])
			buckets[bk] += f.Size
			if bk == rootBucket {
				rootAll = append(rootAll, mftTop{paths[rec], f.Size})
			}
			for i := len(top) - 1; i > 0 && top[i].Size > top[i-1].Size; i-- {
				top[i], top[i-1] = top[i-1], top[i]
			}
			if len(top) > 5 {
				top = top[:5]
			}
		}
	}
	res.Top = top
	res.FromExtOnly = fromExtOnly
	res.FromExtBytes = fromExtBytes
	res.Buckets = buckets
	res.RejectReasons = reasons

	// 根目录条目：按体积降序，只留前 12 条打印，总数照实报
	sort.Slice(rootAll, func(i, j int) bool { return rootAll[i].Size > rootAll[j].Size })
	res.RootCount = len(rootAll)
	if len(rootAll) > 12 {
		res.RootEntries = rootAll[:12]
	} else {
		res.RootEntries = rootAll
	}

	// 定点查询：按名字在整张表里找一遍，不看父目录是否可还原
	if probeName != "" {
		p := mftProbe{Name: probeName}
		var best uint64
		for rec, f := range files {
			if !strings.EqualFold(f.Name, probeName) {
				continue
			}
			p.Matched++
			if p.Matched == 1 || f.Size > best {
				best = f.Size
				p.Record = rec
				p.Parent = f.Parent
				p.Size = f.Size
				p.Links = f.Links
				p.IsDir = f.IsDir
				p.Path = paths[rec]
				_, ok := files[f.Parent]
				p.ParentKnown = ok || f.Parent == rootRecord
			}
		}
		res.Probe = p
	}
	return res, paths, files, nil
}

// walkStats 用现有遍历法扫一遍，作为对账基准。
// 必须跳过 reparse point：C:\Documents and Settings 是指向 C:\Users 的联接点，
// 走进去等于把整个 Users 再数一遍，基准会凭空多出 20 多 GB。
// walkResult 是一次目录遍历的完整结果。
//
// 单独拆出来，是为了能带上「不可读目录」计数：
// 以前遇到权限不足的目录直接 SkipDir、既不计也不报，遍历因此少报却不自知，
// 却被当成「真值」去对账——这类静默丢数据的地方，必须留痕。
type walkResult struct {
	files       int
	dirs        int
	total       uint64
	elapsed     time.Duration
	buckets     map[string]uint64
	dirBuckets  map[string]uint64
	dirBuckets2 map[string]uint64
	// 因权限不足读不到、整棵子树被跳过的目录数（不含其子孙）
	unreadable int
	// 因是 junction/联接点而被跳过的目录数（也计一次「本该算进去的目录」）
	skippedReparse int
}

func walkStatsFull(root string) walkResult {
	t0 := time.Now()
	r := walkResult{
		buckets:     make(map[string]uint64, 32),
		dirBuckets:  make(map[string]uint64, 32),
		dirBuckets2: make(map[string]uint64, 4096),
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				r.unreadable++
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if isReparse(info) {
			if d.IsDir() {
				// junction/联接点：遍历**不数它**，而 MFT 会数它。
				// 不单独计出来，就会把「遍历比 MFT 少」误当成「MFT 多算了」。
				r.skippedReparse++
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			r.dirs++
			r.dirBuckets[topComponent(root, p)]++
			if k := twoComponent(root, p); k != "" {
				r.dirBuckets2[k]++
			}
			return nil
		}
		if info.Mode().IsRegular() {
			r.files++
			r.total += uint64(info.Size())
			r.buckets[topComponent(root, p)] += uint64(info.Size())
		}
		return nil
	})
	r.elapsed = time.Since(t0)
	return r
}

func walkStats(root string) (files int, dirs int, total uint64, elapsed time.Duration, buckets map[string]uint64, dirBuckets map[string]uint64, dirBuckets2 map[string]uint64) {
	r := walkStatsFull(root)
	return r.files, r.dirs, r.total, r.elapsed, r.buckets, r.dirBuckets, r.dirBuckets2
}

// reportWalkCount 只做目录遍历，把「遍历这一侧自身的盲区」摊开。
//
// 存在的理由很具体：MFT 和目录遍历对不上时，我此前每次都默认是 MFT 少了。
// 可遍历自己也会静默丢东西——不可读的目录整棵 SkipDir、junction 根本不数。
// 不把这两个数摆出来，「谁错了」这个问题就没法问对。
// walkCountLetter 把 walk-count 的入参规范成单个盘符字母。
//
// 这个子命令只统计整个卷。以前它把入参原样当盘符，于是
// `walk-count C:\Users\me\dir` 会去遍历 `C:\Users\me\dir:\` 这种不存在的根，
// 报出 0 目录 0 文件，还打「本次遍历没有漏，可以直接当参照物用」。
// 一个诊断工具给出「可信」的错误结论，比直接报错更坏。
func walkCountLetter(raw string) (string, bool) {
	t := raw
	for len(t) > 0 && (t[len(t)-1] == ':' || t[len(t)-1] == '\\') {
		t = t[:len(t)-1]
	}
	if len(t) != 1 {
		return "", false
	}
	c := t[0]
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
		return strings.ToUpper(t), true
	}
	return "", false
}

// reportWalkCount 返回是否真的测到了东西。
//
// 返回 false 的场合只有一个：那个根根本不存在或读不了。这时绝不能照常打印
// 「都是 0——本次遍历没有漏，可以直接当参照物用」——什么都没遍历到，
// 却给出「可信」的结论，会让后面所有 MFT 对账的差异归因都指向错误方向。
func reportWalkCount(letter string) bool {
	root := letter + `:\`
	fmt.Printf("纯目录遍历计数：%s（不需要管理员权限）\n\n", root)
	if _, err := os.Stat(root); err != nil {
		fmt.Printf("  这个根不存在或不可读：%v\n", err)
		fmt.Println("  ——这次什么都没测到，下面的数字不作数，也不要拿去当参照物。")
		return false
	}
	r := walkStatsFull(root)
	fmt.Printf("  目录          : %d\n", r.dirs)
	fmt.Printf("  文件          : %d\n", r.files)
	fmt.Printf("  合计          : %s\n", human(r.total))
	fmt.Printf("  耗时          : %.2fs\n", r.elapsed.Seconds())
	fmt.Println()
	fmt.Println("  遍历这一侧自己的盲区（这些**不包含**在上面的目录数里）：")
	fmt.Printf("    junction/联接点      : %d 个目录被跳过（MFT 会数它们）\n", r.skippedReparse)
	fmt.Printf("    权限不足不可读        : %d 个目录被跳过，其整棵子树一并丢失\n", r.unreadable)
	if r.skippedReparse == 0 && r.unreadable == 0 {
		fmt.Println("    （都是 0——本次遍历没有漏，可以直接当参照物用）")
	} else {
		fmt.Println()
		fmt.Println("  ⚠ 所以这个目录数只是**下限**。拿它去对账 MFT 之前，先减掉 junction，")
		fmt.Println("    再想清楚那 N 个不可读目录的子树到底有多大——否则差异会被全算到 MFT 头上。")
	}
	return true
}

// reportMftPathDiff 把「MFT 认为存在的目录」和「目录遍历看到的目录」直接做差，
// 打印只在一方出现的**具体路径**。不再靠推断，让路径自己说明问题。
func reportMftPathDiff(letter string) {
	volume := letter + `:\`
	res, paths, files, err := readMft(letter, "")
	if err != nil || res == nil {
		fmt.Printf("MFT 直读不可用：%v\n", err)
		return
	}
	mftSet := make(map[string]struct{}, res.Dirs+1024)
	for rec, f := range files {
		if !f.IsDir {
			continue
		}
		if p := paths[rec]; p != "" {
			mftSet[strings.ToLower(p)] = struct{}{}
		}
	}
	fmt.Printf("MFT 目录 %d 个，去重集合 %d 个\n", res.Dirs, len(mftSet))

	walkSet := make(map[string]struct{}, 300000)
	var walkDirs int
	t0 := time.Now()
	_ = filepath.WalkDir(volume, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if isReparse(info) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			walkDirs++
			walkSet[strings.ToLower(p)] = struct{}{}
		}
		return nil
	})
	fmt.Printf("遍历目录 %d 个，去重集合 %d 个，耗时 %.1fs\n", walkDirs, len(walkSet), time.Since(t0).Seconds())

	onlyWalk := make([]string, 0, 128)
	for p := range walkSet {
		if _, ok := mftSet[p]; !ok {
			onlyWalk = append(onlyWalk, p)
		}
	}
	onlyMft := make([]string, 0, 128)
	for p := range mftSet {
		if _, ok := walkSet[p]; !ok {
			onlyMft = append(onlyMft, p)
		}
	}
	sort.Strings(onlyWalk)
	sort.Strings(onlyMft)
	fmt.Printf("\n只在遍历里有：%d 个        只在 MFT 里有：%d 个\n", len(onlyWalk), len(onlyMft))
	showPaths := func(title string, list []string) {
		fmt.Printf("\n%s（最多 15 条）：\n", title)
		for i, p := range list {
			if i >= 15 {
				break
			}
			fmt.Printf("  %s\n", p)
		}
	}
	showPaths("只在遍历里有", onlyWalk)
	showPaths("只在 MFT 里有", onlyMft)
}

// reportMftDump 是 `diskmon-go.exe mft-dump [X:]` 的实现：只读对账，不删任何东西。
func reportMftDump(letter string, compare bool, probeName string) {
	res, paths, _, err := readMft(letter, probeName)
	if err != nil {
		fmt.Printf("MFT 直读不可用：%v\n", err)
		fmt.Println("（这条路径需要管理员权限；没有就继续用目录遍历，不影响现有功能。）")
		return
	}
	fmt.Printf("MFT 直读 %s\n", res.Volume)
	fmt.Printf("  记录大小 %d 字节，MFT 数据 %s\n", res.RecordSize, human(res.MftBytes))
	fmt.Printf("  扫描记录 %d 条 → 目录 %d、文件 %d、合计 %s，耗时 %.2fs\n",
		res.Records, res.Dirs, res.Files, human(res.Total), res.Elapsed.Seconds())
	fmt.Printf("  已排除 NTFS 元文件 %d 个 / %s（$MFT、$BadClus 等，不是用户文件）\n",
		res.MetaFiles, human(res.MetaBytes))
	if res.Unresolved > 0 {
		fmt.Printf("  ⚠ 另有 %d 条记录路径还原不出来（父引用损坏），未计入\n", res.Unresolved)
	}
	fmt.Printf("  记录 %d 条 = 在用 %d + 空闲 %d + 解析失败 %d\n",
		res.Records, res.Records-res.DroppedTotal, res.FreeRecords, res.ParseFailed)
	if res.ParseFailed > 0 {
		keys := make([]string, 0, len(res.RejectReasons))
		for k := range res.RejectReasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return res.RejectReasons[keys[i]] > res.RejectReasons[keys[j]]
		})
		fmt.Println("  解析失败明细：")
		for _, k := range keys {
			fmt.Printf("      %-22s %d\n", k, res.RejectReasons[k])
		}
		fmt.Println("      注：扩展记录是 NTFS 正常结构（属性溢出到另一条记录），跳过不会丢体积。")
	}
	fmt.Printf("  其中 %d 个文件尺寸取自 $FILE_NAME（$DATA 溢出到扩展记录），合计 %s\n",
		res.ByNameOnly, human(res.ByNameBytes))
	if res.FromExtOnly > 0 {
		fmt.Printf("  另有 %d 个文件从扩展记录里补回尺寸，合计 %s\n",
			res.FromExtOnly, human(res.FromExtBytes))
	}
	// $MFT 分段诊断：三个数一对，就知道记录有没有读全
	fmt.Printf("  $MFT 自身 $DATA：%d 段 runlist、%d 个 run", res.RunsSegments, res.Runs)
	if res.HasAttrList {
		fmt.Printf("（有 $ATTRIBUTE_LIST，可能还有分段在扩展记录里）")
	}
	fmt.Printf("\n  记录数：读到 %d 条，按 MftValidDataLength 应有 %d 条（差 %+d）\n",
		res.Records, res.ExpectedRecs, int64(res.Records)-int64(res.ExpectedRecs))
	if res.ExtNameRecovered > 0 {
		fmt.Printf("  从扩展记录救回的主记录：%d 条（其中目录 %d 条）——不救则整棵子树丢失\n",
			res.ExtNameRecovered, res.ExtNameRecoveredDirs)
	}

	// 被拒记录的成分：真扩展记录不带 $FILE_NAME。带了的，就是被误杀的正经条目。
	fmt.Printf("  被拒记录成分：带 $FILE_NAME %d 条（其中带目录标志 %d 条）、只带目录标志 %d 条\n",
		res.RejectWithName, res.RejectDirWithName, res.RejectDir)
	if len(res.RejectBuckets) > 0 {
		type rb struct {
			k string
			v uint64
		}
		list := make([]rb, 0, len(res.RejectBuckets))
		for k, v := range res.RejectBuckets {
			list = append(list, rb{k, v})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
		fmt.Print("  被拒记录归属（前 5 个顶层目录）：")
		for i, e := range list {
			if i >= 5 {
				break
			}
			fmt.Printf(" %s=%d", e.k, e.v)
		}
		fmt.Println()
	}
	if res.DirUnplaced > 0 {
		fmt.Printf("  其中解析失败、但目录标志明确的：%d 个（父引用已丢失，定位不到）。"+
			"计入后目录总数 = %d\n", res.DirUnplaced, int64(res.Dirs)+int64(res.DirUnplaced))
	}
	if len(res.RejectDirSamples) > 0 {
		fmt.Print("  被拒的「带名字的目录」样本：")
		for i, n := range res.RejectDirSamples {
			if i >= 6 {
				break
			}
			if i > 0 {
				fmt.Print(" | ")
			}
			fmt.Print(n)
		}
		fmt.Println()
	}
	if len(res.UnresolvedBuckets) > 0 {
		type rb3 struct {
			k string
			v uint64
		}
		l3 := make([]rb3, 0, len(res.UnresolvedBuckets))
		for k, v := range res.UnresolvedBuckets {
			l3 = append(l3, rb3{k, v})
		}
		sort.Slice(l3, func(i, j int) bool { return l3[i].v > l3[j].v })
		fmt.Print("  路径还原不出来的记录，按父目录归类（前 5 个顶层目录）：")
		for i, e := range l3 {
			if i >= 5 {
				break
			}
			fmt.Printf(" %s=%d", e.k, e.v)
		}
		fmt.Println()
	}
	if res.TrialDir > 0 {
		type rb2 struct {
			k string
			v uint64
		}
		l2 := make([]rb2, 0, len(res.TrialBuckets))
		for k, v := range res.TrialBuckets {
			l2 = append(l2, rb2{k, v})
		}
		sort.Slice(l2, func(i, j int) bool { return l2[i].v > l2[j].v })
		fmt.Printf("  试算：若接受这些记录，目录数会 +%d。归属：", res.TrialDir)
		for i, e := range l2 {
			if i >= 5 {
				break
			}
			fmt.Printf(" %s=%d", e.k, e.v)
		}
		fmt.Println()
	}

	if res.MultiLink > 0 {
		fmt.Printf("  硬链接数>1 的记录 %d 个（若每个名字都算一条路径，会多出 %d 条）\n",
			res.MultiLink, res.MultiLinkExtra)
	}
	if len(res.Top) > 0 {
		fmt.Printf("  最大的单条：\n")
		for _, e := range res.Top {
			fmt.Printf("    %-56s %s\n", e.Path, human(e.Size))
		}
	}

	// 根目录条目：和资源管理器逐个对照，缺了谁一眼就能看出来
	if res.RootCount > 0 {
		fmt.Printf("\n根目录条目（MFT 视角，共 %d 个文件，列出最大的 %d 个）：\n",
			res.RootCount, len(res.RootEntries))
		for _, e := range res.RootEntries {
			fmt.Printf("    %-44s %s\n", e.Path, human(e.Size))
		}
	}
	if res.Probe.Name != "" {
		p := res.Probe
		fmt.Printf("\n定点查询 %q：", p.Name)
		if p.Matched == 0 {
			fmt.Println("MFT 里没有这个文件名的记录 —— 记录层就没读到，和路径还原无关")
		} else {
			fmt.Printf("命中 %d 条，最大的一条：\n", p.Matched)
			fmt.Printf("    记录号 %d   父记录 %d（父存在=%v）   尺寸 %s   链接数 %d   目录=%v\n",
				p.Record, p.Parent, p.ParentKnown, human(p.Size), p.Links, p.IsDir)
			fmt.Printf("    还原路径 %q\n", p.Path)
		}
	}

	if !compare {
		return
	}
	fmt.Printf("\n对账：用目录遍历再扫一遍（已跳过 junction/联接点）…\n")
	wr := walkStatsFull(res.Volume)
	files, dirs, total, el := wr.files, wr.dirs, wr.total, wr.elapsed
	walkBuckets, walkDirBuckets, walkDirBuckets2 := wr.buckets, wr.dirBuckets, wr.dirBuckets2
	fmt.Printf("  目录遍历      : 目录 %d、文件 %d、合计 %s，耗时 %.2fs\n", dirs, files, human(total), el.Seconds())
	fmt.Printf("  MFT 直读      : 目录 %d、文件 %d、合计 %s，耗时 %.2fs\n", res.Dirs, res.Files, human(res.Total), res.Elapsed.Seconds())
	// 遍历自己也有盲区：权限不足的目录会被 SkipDir，它的整棵子树一起消失。
	// 不把这个数报出来，就会把「遍历漏了」误当成「MFT 多了/少了」——这台机器上它曾是 9,609。
	if wr.unreadable > 0 {
		fmt.Printf("  ⚠ 遍历盲区    : %d 个目录因不可读而整棵子树被跳过（遍历因此**少报**，不是 MFT 的问题）\n", wr.unreadable)
	}
	fmt.Printf("  差异          : 目录 %+d、文件 %+d、合计 %+d 字节\n",
		res.Dirs-dirs, res.Files-files, int64(res.Total)-int64(total))
	if res.Elapsed > 0 && el > 0 {
		fmt.Printf("  速度          : %.0f×\n", float64(el)/float64(res.Elapsed))
	}
	fmt.Println("  说明：MFT 每条记录只算一次，硬链接不会重复计；目录遍历按路径求和，会把硬链接算多次。")
	fmt.Println("        所以两者天然相差约「硬链接重复量」（本机约 6.3 GB），这是对的，不是 bug。")

	// 分桶对账：按顶层目录分别求和，差值最大的排前面。
	// 只有一个总数对不上时无从下手；分桶之后就能直接看出体积差在哪个子树。
	type bucketDiff struct {
		name     string
		mft, wlk uint64
	}
	diffs := make([]bucketDiff, 0, len(res.Buckets)+len(walkBuckets))
	seen := make(map[string]bool, len(res.Buckets))
	for name, m := range res.Buckets {
		diffs = append(diffs, bucketDiff{name, m, walkBuckets[name]})
		seen[name] = true
	}
	for name, w := range walkBuckets {
		if !seen[name] {
			diffs = append(diffs, bucketDiff{name, 0, w})
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		return absDiff(diffs[i].mft, diffs[i].wlk) > absDiff(diffs[j].mft, diffs[j].wlk)
	})
	fmt.Printf("\n分桶对账（差值最大的 8 个顶层目录）：\n")
	fmt.Printf("  %-22s %12s %12s %12s\n", "顶层目录", "MFT", "遍历", "差值")
	for i, d := range diffs {
		if i >= 8 {
			break
		}
		fmt.Printf("  %-22s %12s %12s %12s\n",
			d.name, human(d.mft), human(d.wlk), signedGB(int64(d.mft)-int64(d.wlk)))
	}

	// 目录数分桶对账：字节差说不清的部分，目录个数会直接指出来。
	type dirDiff struct {
		name     string
		mft, wlk uint64
	}
	dd := make([]dirDiff, 0, len(res.DirBuckets)+len(walkDirBuckets))
	dseen := make(map[string]bool, len(res.DirBuckets))
	for name, m := range res.DirBuckets {
		dd = append(dd, dirDiff{name, m, walkDirBuckets[name]})
		dseen[name] = true
	}
	for name, w := range walkDirBuckets {
		if !dseen[name] {
			dd = append(dd, dirDiff{name, 0, w})
		}
	}
	sort.Slice(dd, func(i, j int) bool {
		return absDiff(dd[i].mft, dd[i].wlk) > absDiff(dd[j].mft, dd[j].wlk)
	})
	fmt.Printf("\n目录数对账（差值最大的 8 个顶层目录）：\n")
	fmt.Printf("  %-22s %10s %10s %10s\n", "顶层目录", "MFT", "遍历", "差值")
	for i, d := range dd {
		if i >= 8 {
			break
		}
		fmt.Printf("  %-22s %10d %10d %+10d\n",
			d.name, d.mft, d.wlk, int64(d.mft)-int64(d.wlk))
	}

	// 再钻一级：整体差额集中在某个顶层目录时，这一层直接指出是哪棵子树。
	dd2 := make([]dirDiff, 0, len(res.DirBuckets2)+len(walkDirBuckets2))
	d2seen := make(map[string]bool, len(res.DirBuckets2))
	for name, m := range res.DirBuckets2 {
		dd2 = append(dd2, dirDiff{name, m, walkDirBuckets2[name]})
		d2seen[name] = true
	}
	for name, w := range walkDirBuckets2 {
		if !d2seen[name] {
			dd2 = append(dd2, dirDiff{name, 0, w})
		}
	}
	sort.Slice(dd2, func(i, j int) bool {
		return absDiff(dd2[i].mft, dd2[i].wlk) > absDiff(dd2[j].mft, dd2[j].wlk)
	})
	fmt.Printf("\n目录数对账（再钻一级，差值最大的 6 棵子树）：\n")
	fmt.Printf("  %-30s %10s %10s %10s\n", "子树", "MFT", "遍历", "差值")
	for i, d := range dd2 {
		if i >= 6 {
			break
		}
		fmt.Printf("  %-30s %10d %10d %+10d\n",
			d.name, d.mft, d.wlk, int64(d.mft)-int64(d.wlk))
	}

	sample := make([]string, 0, 10)
	for _, p := range paths {
		if p != "" {
			sample = append(sample, p)
		}
	}
	sort.Strings(sample)
	fmt.Printf("\n路径还原样例（前 8 条）：\n")
	for i, p := range sample {
		if i >= 8 {
			break
		}
		fmt.Printf("  %s\n", p)
	}
	fmt.Println("\n以上只是统计与对账，没有读取或改动任何文件内容。")
}
