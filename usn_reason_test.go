package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// 白名单只放 CLOSE：任何写位、改名位、属性位、甚至不认识的位，一律算改动。
func TestUsnReasonIsModifying(t *testing.T) {
	// 放行：只有 CLOSE。Windows 的 USN 只记改动，读文件/查属性不产生记录，
	// 所以「只有 CLOSE 的记录」意味着这个文件除了被关掉没发生别的事。
	if usnReasonIsModifying(reasonClose) {
		t.Fatal("只有 CLOSE 的记录不该算改动")
	}

	modifying := []struct {
		name   string
		reason uint32
	}{
		{"DATA_OVERWRITE 写覆盖", 0x00000001},
		{"DATA_EXTEND 写扩展", 0x00000002},
		{"DATA_TRUNCATION 截断", 0x00000004},
		{"NAMED_DATA_OVERWRITE 命名流写", 0x00000008},
		{"FILE_CREATE 新建", 0x00000100},
		{"FILE_DELETE 删除", 0x00000200},
		{"EA_CHANGE 扩展属性", 0x00000400},
		{"SECURITY_CHANGE ACL/owner", 0x00000800},
		{"RENAME_OLD_NAME 改名（旧名）", 0x00001000},
		{"RENAME_NEW_NAME 改名（新名）", 0x00002000},
		{"BASIC_INFO_CHANGE mtime/属性", 0x00008000},
		{"HARD_LINK_CHANGE 硬链接数", 0x00010000},
		{"REPARSE_POINT_CHANGE 链接点", 0x00100000},
		{"STREAM_CHANGE 流", 0x00200000},
		{"INTEGRITY_CHANGE", 0x00800000},
		{"不认识的位（0x40）", 0x00000040},
		{"CLOSE 与写位合并", reasonClose | 0x00000002},
	}
	for _, tc := range modifying {
		if !usnReasonIsModifying(tc.reason) {
			t.Fatalf("%s（0x%08x）必须算改动", tc.name, tc.reason)
		}
	}
	// 白名单不能宽到能盖住任何写：把每个写位单独混进 CLOSE 都必须被拒。
	for _, w := range []uint32{0x1, 0x2, 0x4, 0x100, 0x200, 0x1000, 0x2000} {
		if !usnReasonIsModifying(reasonClose | w) {
			t.Fatalf("CLOSE|0x%x 不应放行", w)
		}
	}
}

// buildUsnRecord 造一条 USN_RECORD_V2：判定只读 USN 与 reason，
// 文件名内容用不着，所以只把长度/偏移填对。
func buildUsnRecord(usn int64, reason uint32) []byte {
	recLen := usnRecordHeader + 8 // 名字放 4 个 UTF-16 字符
	if pad := recLen % usnRecordAlign; pad != 0 {
		recLen += usnRecordAlign - pad
	}
	b := make([]byte, recLen)
	binary.LittleEndian.PutUint32(b[0:4], uint32(recLen))
	binary.LittleEndian.PutUint16(b[4:6], 2) // MajorVersion 2
	binary.LittleEndian.PutUint64(b[24:32], uint64(usn))
	binary.LittleEndian.PutUint32(b[40:44], reason)
	binary.LittleEndian.PutUint16(b[56:58], 8)                // FileNameLength
	binary.LittleEndian.PutUint16(b[58:60], usnRecordHeader)  // FileNameOffset
	return b
}

func usnBuffer(next int64, recs ...[]byte) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf[:8], uint64(next))
	for _, r := range recs {
		buf = append(buf, r...)
	}
	return buf
}

func TestParseUsnReasons(t *testing.T) {
	r1 := buildUsnRecord(900, reasonClose)
	r2 := buildUsnRecord(905, reasonClose|0x00000002)
	recs, next, err := parseUsnReasons(usnBuffer(910, r1, r2))
	if err != nil {
		t.Fatalf("正常缓冲不该报错：%v", err)
	}
	if next != 910 {
		t.Fatalf("next 应为 910，实际 %d", next)
	}
	if len(recs) != 2 || recs[0].USN != 900 || recs[0].Reason != reasonClose ||
		recs[1].USN != 905 || recs[1].Reason != reasonClose|0x00000002 {
		t.Fatalf("解析结果不对：%+v", recs)
	}

	// 结构上任何说不清的东西都必须报错——判不准就不复用。
	bad := func(name string, buf []byte) {
		t.Helper()
		if _, _, err := parseUsnReasons(buf); err == nil {
			t.Fatalf("%s：应当报错", name)
		}
	}
	bad("缓冲不足 8 字节", []byte{1, 2, 3})
	bad("记录长度越过缓冲", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		binary.LittleEndian.PutUint32(b[8:12], uint32(len(b))) // 声称比实际长
		return b
	}())
	bad("记录长度小于头长", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		binary.LittleEndian.PutUint32(b[8:12], 16)
		return b
	}())
	bad("记录长度未按 8 字节对齐", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		b = append(b, make([]byte, 4)...)
		return b
	}())
	bad("不认识的记录版本", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		binary.LittleEndian.PutUint16(b[12:14], 3)
		return b
	}())
	bad("名字字段越界", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		binary.LittleEndian.PutUint16(b[64:66], 4000) // FileNameLength 撑爆记录
		return b
	}())
	bad("末尾残留半条记录", func() []byte {
		b := usnBuffer(0, buildUsnRecord(1, reasonClose))
		return append(b, 0x01, 0x02, 0x03)
	}())
}

// 正面：锚点之后多了几条记录，但全是 CLOSE —— 放行，复用整卷快照。
func TestUsnAnchorCleanAllowsHarmlessDelta(t *testing.T) {
	prev := usnAnchor{JournalID: 7, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 7, First: 100, Next: 905}
	d := deltaResult{examined: true, records: 5}
	if clean, why := usnAnchorClean(prev, cur, d); !clean {
		t.Fatalf("只有 CLOSE 类记录时应当放行，实际拒绝：%s", why)
	}
	// 一条记录都没有时更不必读 delta。
	if clean, why := usnAnchorClean(prev, prev, deltaResult{}); !clean {
		t.Fatalf("零记录时应当放行，实际拒绝：%s", why)
	}
}

// 反面：只要出现一条改动类记录就拒绝——这是这次放宽的安全底线。
func TestUsnAnchorCleanRefusesModifyingDelta(t *testing.T) {
	prev := usnAnchor{JournalID: 7, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 7, First: 100, Next: 905}

	clean, why := usnAnchorClean(prev, cur, deltaResult{examined: true, records: 5, modifying: 0x00000002})
	if clean {
		t.Fatal("有一条 DATA_EXTEND 必须拒绝复用")
	}
	if !strings.Contains(why, "改动类") {
		t.Fatalf("拒绝理由应当点明改动类记录，实际 %q", why)
	}

	// 读失败 / 根本没去读：都是"不知道"，同样拒绝。
	if clean, _ := usnAnchorClean(prev, cur, deltaResult{examined: true, err: fmt.Errorf("读不到")}); clean {
		t.Fatal("读不到 USN 记录时必须拒绝")
	}
	if clean, _ := usnAnchorClean(prev, cur, deltaResult{}); clean {
		t.Fatal("需要读却没读时必须拒绝")
	}

	// t5 的结构性拒绝不能被本次放宽盖掉：就算 delta 说"干净"也一样拒。
	rebuild := usnAnchor{JournalID: 8, First: 100, Next: 905}
	if clean, _ := usnAnchorClean(prev, rebuild, deltaResult{examined: true}); clean {
		t.Fatal("journalID 变了必须拒绝，delta 干净也不行")
	}
	regress := usnAnchor{JournalID: 7, First: 50, Next: 400}
	if clean, _ := usnAnchorClean(prev, regress, deltaResult{examined: true}); clean {
		t.Fatal("USN 回退必须拒绝")
	}
	outOfWindow := usnAnchor{JournalID: 7, First: 950, Next: 1200}
	if clean, _ := usnAnchorClean(prev, outOfWindow, deltaResult{examined: true}); clean {
		t.Fatal("锚点滚出窗口必须拒绝")
	}
	incomplete := usnAnchor{}
	if clean, _ := usnAnchorClean(incomplete, cur, deltaResult{examined: true}); clean {
		t.Fatal("锚点不完整必须拒绝")
	}
}
