package main

// 逐文件索引的单元测试。全部离线：不碰真卷、不需要管理员。
// 测的是三件事——「查得到」「查不到时说不知道」「落盘读回一致」，
// 外加一个容易被写错的边界：0 字节文件和「表里没有这条」必须分得开。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sampleFiles 造一份小样本：普通文件、0 字节文件、多链接文件、目录、0 号记录。
func sampleFiles() map[uint64]mftFile {
	return map[uint64]mftFile{
		5:      {Record: 5, Name: "root", IsDir: true},
		100:    {Record: 100, Name: "a.txt", Size: 1234, Links: 1},
		200:    {Record: 200, Name: "empty.log", Size: 0, Links: 1}, // 0 字节，真实存在
		300:    {Record: 300, Name: "huge.iso", Size: 5 << 30, Links: 1},
		400:    {Record: 400, Name: "linked.dll", Size: 999, Links: 3},
		500:    {Record: 500, Name: "subdir", IsDir: true},
		0:      {Record: 0, Name: "$MFT", Size: 4242, Links: 1},
		1<<40 + 7: {Record: 1<<40 + 7, Name: "far.bin", Size: 7, Links: 1},
	}
}

func TestMftIndexAnswersSizeAndLinks(t *testing.T) {
	x := buildMftIndex(sampleFiles())
	if got := x.Len(); got != 5 {
		// 样本 8 条：去掉 2 个目录（5、500）和 0 号记录，剩 5 条文件
		t.Fatalf("条数应当 5（去掉 2 个目录和 0 号记录），实际 %d", got)
	}

	cases := []struct {
		frn   uint64
		size  uint64
		links uint8
	}{
		{100, 1234, 1},
		{200, 0, 1},       // 0 字节文件：查得到，答案是 0
		{300, 5 << 30, 1}, // 大文件
		{400, 999, 3},     // 多链接：链接数要如实给
		{1<<40 + 7, 7, 1}, // 超大 FRN，考验差值编码
	}
	for _, c := range cases {
		size, links, ok := x.lookup(c.frn)
		if !ok {
			t.Fatalf("FRN %d 应当查得到", c.frn)
		}
		if size != c.size || links != c.links {
			t.Fatalf("FRN %d：要 %d/%d，实得 %d/%d", c.frn, c.size, c.links, size, links)
		}
	}
}

// 这条是本索引最要紧的语义：查不到时不能给 0。
// 一旦调用方把「查不到」当成「大小 0」，一次写入就会被算成整份新增，体积会悄悄变大。
func TestMftIndexMissingIsUnknownNotZero(t *testing.T) {
	x := buildMftIndex(sampleFiles())

	for _, frn := range []uint64{5, 500, 0, 101, 999999, ^uint64(0)} {
		size, links, ok := x.lookup(frn)
		if ok {
			t.Fatalf("FRN %d 不在表里，却报「查到了」：%d/%d", frn, size, links)
		}
	}
	// 目录被排除，所以查它也是「不知道」——这不是缺功能，
	// 而是「目录不参与体积记账」这条约定的直接结果。
	if _, _, ok := x.lookup(5); ok {
		t.Fatal("目录不该在索引里")
	}
	// 空指针 / 空表也必须是「不知道」，不许崩。
	var nilIdx *mftIndex
	if _, _, ok := nilIdx.lookup(100); ok {
		t.Fatal("空索引应当说不知道")
	}
	if _, _, ok := (&mftIndex{}).lookup(100); ok {
		t.Fatal("空表应当说不知道")
	}
}

func TestMftIndexRoundtrip(t *testing.T) {
	src := buildMftIndex(sampleFiles())
	src.volume = "C"
	src.builtAt = 1700000000
	buf := encodeMftIndex(nil, src)
	got, err := decodeMftIndex(buf)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got.volume != "C" || got.builtAt != 1700000000 {
		t.Fatalf("头部没保住：volume=%q builtAt=%d", got.volume, got.builtAt)
	}
	if got.Len() != src.Len() {
		t.Fatalf("条数不一致：%d vs %d", got.Len(), src.Len())
	}
	for i := 0; i < src.Len(); i++ {
		af, as, al := src.entry(i)
		bf, bs, bl := got.entry(i)
		if af != bf || as != bs || al != bl {
			t.Fatalf("第 %d 条不一致：%d/%d/%d vs %d/%d/%d", i, af, as, al, bf, bs, bl)
		}
	}
	// 编码是确定性的：同样输入必须得到同样字节，否则「体积」这个数就不稳定。
	if !bytes.Equal(buf, encodeMftIndex(nil, src)) {
		t.Fatal("编码不确定，两次结果不同")
	}
}

// 落盘再读回：走真实文件，验证 save/load 这一对（含临时文件改名）。
func TestMftIndexSaveLoadFile(t *testing.T) {
	dir, err := os.MkdirTemp(".", "mftidx-test")
	if err != nil {
		t.Fatalf("建临时目录失败：%v", err)
	}
	defer os.RemoveAll(dir)

	src := buildMftIndex(sampleFiles())
	src.volume = "C"
	path := mftIndexPath(dir, "C")
	n, err := saveMftIndex(path, src)
	if err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件不存在：%v", err)
	}
	if st.Size() != n || n != int64(len(encodeMftIndex(nil, src))) {
		t.Fatalf("落盘字节数和返回值/编码长度不一致：stat=%d 返回=%d", st.Size(), n)
	}
	// 临时文件必须被改名走掉，不能留在目录里。
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时文件没被清掉：%v", err)
	}
	got, err := loadMftIndex(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if size, links, ok := got.lookup(400); !ok || size != 999 || links != 3 {
		t.Fatalf("读回后查 400 不对：%d/%d/%v", size, links, ok)
	}
	if _, _, ok := got.lookup(12345); ok {
		t.Fatal("读回后不存在的 FRN 应当是不知道")
	}
	// 路径命名要带上卷，否则 C: 和 D: 会互相覆盖。
	if filepath.Base(path) != "mftidx-C.bin" {
		t.Fatalf("落盘文件名不对：%s", filepath.Base(path))
	}
}

// 坏文件必须明确报错，不许「尽量解出一些」——半份索引看起来能用，比没有更危险。
func TestMftIndexRejectsCorrupt(t *testing.T) {
	good := encodeMftIndex(nil, buildMftIndex(sampleFiles()))

	cases := map[string][]byte{
		"太短":     good[:10],
		"魔数不对": append([]byte("XXXX"), good[4:]...),
		"版本不对": func() []byte {
			b := append([]byte(nil), good...)
			b[4] = 99
			return b
		}(),
		"校验和不符": func() []byte {
			b := append([]byte(nil), good...)
			b[len(b)-1] ^= 0xFF
			return b
		}(),
		"中途截断": good[:len(good)/2],
	}
	for name, buf := range cases {
		if _, err := decodeMftIndex(buf); err == nil {
			t.Fatalf("%s：应当报错，实际解出来了", name)
		} else if !errors.Is(err, ErrMftIndexFormat) {
			t.Fatalf("%s：错误应当是 ErrMftIndexFormat，实际 %v", name, err)
		}
	}
}

// 空表也要能存能读：一次扫描一个文件都没有时不至于崩，只是「什么都不知道」。
func TestMftIndexEmptyRoundtrip(t *testing.T) {
	src := buildMftIndex(nil)
	buf := encodeMftIndex(nil, src)
	got, err := decodeMftIndex(buf)
	if err != nil {
		t.Fatalf("空表解码失败：%v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("空表读回条数为 %d", got.Len())
	}
	if _, _, ok := got.lookup(1); ok {
		t.Fatal("空表应当说不知道")
	}
}

// 体积要有个粗的数量级证：每条平均远小于「8+8+1」的定长写法。
// 这条不是在测性能，是在钉「编码选择确实生效了」——
// 谁把它改成定长或者 JSON，这里会立刻红。
func TestMftIndexEncodingIsCompact(t *testing.T) {
	files := map[uint64]mftFile{}
	// 1 万条，FRN 递增、尺寸像真实小文件（多在 4KB 以内）
	for i := uint64(1); i <= 10000; i++ {
		files[i*3] = mftFile{Record: i * 3, Name: "f", Size: 100 + i%4000, Links: 1}
	}
	x := buildMftIndex(files)
	n := x.byteSize()
	fixed := int64(x.Len()) * 17 // 三个切片的内存口径（8+8+1）
	if n >= fixed {
		t.Fatalf("落盘 %d 字节，没有比定长口径 %d 小，说明差值/varint 没起作用", n, fixed)
	}
	if per := float64(n) / float64(x.Len()); per > 9 {
		t.Fatalf("平均每条 %.2f 字节，超出预期（应当 6-8）", per)
	}
}
