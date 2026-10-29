package main

// 逐文件索引：把一次 MFT 扫描的「每一条文件」压成一张紧凑表，好让 USN 增量层
// 能回答**一个具体文件**的旧问题——主要是「它现在多大（OldSize）」和「它有几个链接」。
//
// 为什么需要它：t12 的 Phase 1 只交判据没交提速，根子就在这里。快照（scanCache）
// 里只有聚合值（Cats / Total / Files），USN 增量读到「某文件被写了」时，
// 不知道它原来多大，减旧加新这一步做不了，于是写/删两类只能回落全扫。
// 有了这张表，Δ 里出现的 FRN 能直接查到旧尺寸。
//
// 设计上的三个硬约束（都是踩过的方向）：
//
//  1. **只用来「查」，不用来「算总数」**。总数仍以扫描快照为准；索引是旁证。
//     两套口径各算一遍迟早对不上，而这里对不上的后果是体积悄悄漂移。
//  2. **查不到 ≠ 0**。lookup 的第二个返回值必须被调用方当回事：
//     索引里没有这条 FRN（新建的、目录、超出可见集），答案是「不知道」，
//     不是「大小为 0」。把它当 0 会让「写入」把一个非零文件算成新增，
//     体积直接多算；这个错很难在数字上看出来，只能在逻辑上守住。
//  3. **体积要能算得清**。见下面编码注释——每条约 6-8 字节，
//     而不是 JSON 的 80-100 字节。一张 100 万文件的表，前者 7MB，后者 90MB。

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// mftIndexMagic 是文件头魔数。用魔数而不是「直接看长度猜」，是为了让
	// 一个被截断/被别的程序覆盖的文件明确地报错，而不是解出一堆垃圾数字。
	mftIndexMagic = "DMFX"
	// mftIndexVersion 变了就必须让旧文件失效——宁可重扫一遍，也不能按旧布局解释新数据。
	mftIndexVersion = 1
	// mftIndexMaxEntries 是解码时的上限，防一个坏掉的头部让进程去要几十 GB。
	// 1<<28 ≈ 2.68 亿条，远超任何单卷的记录数（$MFT 撑不到那个量级）。
	mftIndexMaxEntries = 1 << 28
)

// ErrMftIndexFormat 是「这份文件不是我们的索引，或者坏了」。
// 单独摘出来是因为调用方对它只有一种处理：丢掉、重扫。别的错（IO）要能区分开。
var ErrMftIndexFormat = errors.New("逐文件索引格式不对")

// mftIndex 是一张按 FRN 升序排好的表。
//
// 三个平行切片而不是 []struct，是为了省 padding：entry{uint64,uint64,uint8}
// 在 Go 里对齐成 24 字节，拆开是 8+8+1=17 字节。100 万条差 7MB，值。
type mftIndex struct {
	frn   []uint64 // 升序，MFT 记录号（已去掉序列号高 16 位）
	size  []uint64 // 文件大小（真实字节数）
	links []uint8  // 硬链接数；>1 时说明这个尺寸被多个名字共享
	// 卷标（C / D …）与构建时间：只用于诊断和「别把 C 的表当 D 的用」。
	volume  string
	builtAt int64
}

// Len 是索引条数。
func (x *mftIndex) Len() int {
	if x == nil {
		return 0
	}
	return len(x.frn)
}

// lookup 回答「这条 FRN 的当前大小和链接数」。
//
// ok=false 表示**不知道**，调用方必须回落到全扫，不许当成 0。
// 会返回 false 的三种情况：索引是空的、这条 FRN 不在表里（新建文件 / 目录 / 扫描时被跳过）、
// 索引本身没建起来。
func (x *mftIndex) lookup(frn uint64) (size uint64, links uint8, ok bool) {
	if x == nil || len(x.frn) == 0 {
		return 0, 0, false
	}
	i := sort.Search(len(x.frn), func(i int) bool { return x.frn[i] >= frn })
	if i >= len(x.frn) || x.frn[i] != frn {
		return 0, 0, false
	}
	return x.size[i], x.links[i], true
}

// entry 按下标取一条，供测试和「逐条对账」用。
func (x *mftIndex) entry(i int) (frn, size uint64, links uint8) {
	if x == nil || i < 0 || i >= len(x.frn) {
		return 0, 0, 0
	}
	return x.frn[i], x.size[i], x.links[i]
}

// buildMftIndex 从 MFT 扫描结果（readMft 的 files 参数）建表。
//
// 只收**非目录**记录。目录不参与体积记账——删一个目录减少 0 字节，
// 收进来只会让「这是不是一个文件」变得含糊；宁可让它查不到、走回落。
//
// 注意 size 为 0 的**文件**要收：0 字节文件是真实存在的，
// 「查到且为 0」和「查不到」必须能分清（这正是上面第 2 条约束）。
func buildMftIndex(files map[uint64]mftFile) *mftIndex {
	x := &mftIndex{builtAt: time.Now().Unix()}
	if len(files) == 0 {
		return x
	}
	// 先挑出要收的 FRN。两趟走：一趟数、一趟填，避免 append 反复扩容拷贝。
	x.frn = make([]uint64, 0, len(files))
	for frn, f := range files {
		if f.IsDir {
			continue
		}
		if frn == 0 {
			continue // 0 号记录是 $MFT 自己那一格的名字占位，没有查询价值
		}
		x.frn = append(x.frn, frn)
	}
	sort.Slice(x.frn, func(i, j int) bool { return x.frn[i] < x.frn[j] })
	// 排序后按同一个顺序取 size/links，保证三个切片一列对一列。
	// 直接在排好序的切片上读源 map，比另建一个 []mftFile 省一份内存。
	x.size = make([]uint64, len(x.frn))
	x.links = make([]uint8, len(x.frn))
	for i, frn := range x.frn {
		f := files[frn]
		x.size[i] = f.Size
		if f.Links < 0 {
			x.links[i] = 1 // 解析失败给 1（「可能只有一个名字」），不伪造多链接
		} else if f.Links > 255 {
			x.links[i] = 255
		} else {
			x.links[i] = uint8(f.Links)
		}
	}
	return x
}

// byteSize 是这张表在磁盘上会占多少字节（不实际写盘也算得出，用于先量后决定）。
func (x *mftIndex) byteSize() int64 {
	return int64(len(encodeMftIndex(nil, x)))
}

// encodeMftIndex 把表写成字节流。
//
// 编码（全部小端 + uvarint）：
//
//	"DMFX" | ver u8 | flags u8 | volLen u8 | volume[volLen]
//	builtAt i64 | count u32
//	count × { ΔFRN uvarint, size uvarint, links uvarint }
//	checksum u64（FNV-1a，覆盖以上全部字节）
//
// 为什么这么选：
//   - FRN 用**差值**而不是绝对值：NTFS 的记录号是递增分配的，相邻两条差通常是 1-3，
//     uvarint 只花 1 字节；写 8 字节绝对值等于每条白扔 7 字节。
//   - size 用 uvarint：绝大多数文件小于 16KB（2 字节就够），
//     只有大文件才吃到 4-5 字节。
//   - links 用 uvarint：99% 以上是 1，1 字节。
//   - 不做通用压缩（gzip/zstd）：按上面的分布，压完大概只剩 10-20% 的收益，
//     而代价是加载时必须整份解压——「加载耗时」这一项会从毫秒变成秒。
//     这条权衡见报告里的备选方案。
func encodeMftIndex(dst []byte, x *mftIndex) []byte {
	if x == nil {
		x = &mftIndex{}
	}
	var head [4]byte
	copy(head[:], mftIndexMagic)
	dst = append(dst, head[:]...)
	dst = append(dst, mftIndexVersion, 0)
	vol := x.volume
	if len(vol) > 255 {
		vol = vol[:255]
	}
	dst = append(dst, uint8(len(vol)))
	dst = append(dst, vol...)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], uint64(x.builtAt))
	dst = append(dst, tmp[:]...)
	binary.LittleEndian.PutUint32(tmp[:4], uint32(len(x.frn)))
	dst = append(dst, tmp[:4]...)

	prev := uint64(0)
	for i := range x.frn {
		frn := x.frn[i]
		delta := frn - prev // frn 升序且已排除 0，prev 单调，不会下溢
		prev = frn
		dst = appendUvarint(dst, delta)
		dst = appendUvarint(dst, x.size[i])
		dst = appendUvarint(dst, uint64(x.links[i]))
	}
	h := fnv.New64a()
	_, _ = h.Write(dst)
	binary.LittleEndian.PutUint64(tmp[:], h.Sum64())
	return append(dst, tmp[:]...)
}

// decodeMftIndex 是 encodeMftIndex 的逆。任何一处对不上都返回 ErrMftIndexFormat，
// 绝不「尽量解出一些」——半份索引比没有索引更危险，因为它看起来能用。
func decodeMftIndex(src []byte) (*mftIndex, error) {
	if len(src) < 4+1+1+1+8+4+8 {
		return nil, fmt.Errorf("%w：长度只有 %d 字节，连头都不够", ErrMftIndexFormat, len(src))
	}
	if string(src[:4]) != mftIndexMagic {
		return nil, fmt.Errorf("%w：魔数是 %q，不是 %q", ErrMftIndexFormat, src[:4], mftIndexMagic)
	}
	if src[4] != mftIndexVersion {
		return nil, fmt.Errorf("%w：版本 %d，本程序只认 %d", ErrMftIndexFormat, src[4], mftIndexVersion)
	}
	// 校验和先验：后面的解析就敢相信长度字段了。
	body := src[:len(src)-8]
	want := binary.LittleEndian.Uint64(src[len(src)-8:])
	h := fnv.New64a()
	_, _ = h.Write(body)
	if h.Sum64() != want {
		return nil, fmt.Errorf("%w：校验和不符（期望 %016x，实得 %016x）", ErrMftIndexFormat, want, h.Sum64())
	}

	off := 5
	flags := src[off]
	off++
	if flags != 0 {
		return nil, fmt.Errorf("%w：flags=%#x 未知（可能是更高版本写的）", ErrMftIndexFormat, flags)
	}
	volLen := int(src[off])
	off++
	if off+volLen > len(body) {
		return nil, fmt.Errorf("%w：卷标长度 %d 越界", ErrMftIndexFormat, volLen)
	}
	volume := string(src[off : off+volLen])
	off += volLen
	if off+8+4 > len(body) {
		return nil, fmt.Errorf("%w：头部被截断", ErrMftIndexFormat)
	}
	builtAt := int64(binary.LittleEndian.Uint64(src[off : off+8]))
	off += 8
	count := int(binary.LittleEndian.Uint32(src[off : off+4]))
	off += 4
	if count < 0 || count > mftIndexMaxEntries {
		return nil, fmt.Errorf("%w：条数 %d 离谱", ErrMftIndexFormat, count)
	}

	x := &mftIndex{
		frn:     make([]uint64, 0, count),
		size:    make([]uint64, 0, count),
		links:   make([]uint8, 0, count),
		volume:  volume,
		builtAt: builtAt,
	}
	prev := uint64(0)
	for i := 0; i < count; i++ {
		var delta, size, links uint64
		var n int
		delta, n = binary.Uvarint(body[off:])
		if n <= 0 {
			return nil, fmt.Errorf("%w：第 %d 条的 ΔFRN 读不出来（偏移 %d）", ErrMftIndexFormat, i, off)
		}
		off += n
		size, n = binary.Uvarint(body[off:])
		if n <= 0 {
			return nil, fmt.Errorf("%w：第 %d 条的 size 读不出来（偏移 %d）", ErrMftIndexFormat, i, off)
		}
		off += n
		links, n = binary.Uvarint(body[off:])
		if n <= 0 {
			return nil, fmt.Errorf("%w：第 %d 条的 links 读不出来（偏移 %d）", ErrMftIndexFormat, i, off)
		}
		off += n
		if links > 255 {
			return nil, fmt.Errorf("%w：第 %d 条 links=%d 超出 uint8", ErrMftIndexFormat, i, links)
		}
		if delta == 0 {
			// 严格升序：差为 0 意味着同一个 FRN 出现两次，这份表不可信。
			return nil, fmt.Errorf("%w：第 %d 条 FRN 重复", ErrMftIndexFormat, i)
		}
		prev += delta
		x.frn = append(x.frn, prev)
		x.size = append(x.size, size)
		x.links = append(x.links, uint8(links))
	}
	if off != len(body) {
		// 尾部还有字节：多半是版本不一致，宁可不认。
		return nil, fmt.Errorf("%w：解析完还有 %d 字节剩余", ErrMftIndexFormat, len(body)-off)
	}
	return x, nil
}

// saveMftIndex 写盘。先写同目录临时文件再改名：
// 中途断电/磁盘满只会留下一个孤儿临时文件，不会把上一份好索引打坏。
// 返回实际落盘字节数（用于报「体积」）。
func saveMftIndex(path string, x *mftIndex) (int64, error) {
	buf := encodeMftIndex(make([]byte, 0, 1<<20+x.Len()*8), x)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.Write(buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	// 先落盘再改名：否则改完名崩溃，文件存在但内容可能是空的。
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return int64(len(buf)), nil
}

// loadMftIndex 读盘。整份读进内存再解析——按实测体积（百万条个位数 MB），
// 流式解析省下的那点峰值内存不值得多写一套状态机。
func loadMftIndex(path string) (*mftIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeMftIndex(b)
}

// mftIndexPath 是索引在数据目录里的落盘位置。
func mftIndexPath(dataDir, volume string) string {
	return filepath.Join(dataDir, "mftidx-"+volume+".bin")
}

// 下面两个是指标用的辅助，给测量和报告用，不参与判定。
func appendUvarint(dst []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(dst, tmp[:n]...)
}

// decodeMftIndexFrom 从 reader 读（测试里用得着；落盘路径走 loadMftIndex）。
func decodeMftIndexFrom(r io.Reader) (*mftIndex, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return decodeMftIndex(b)
}
