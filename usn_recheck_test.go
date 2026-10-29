package main

import (
	"errors"
	"strings"
	"testing"
)

// t14：读完 USN 记录之后必须**再查一次坐标**，用来判断这段记录是不是「残缺的一段」。
//
// 这一组用例钉两件事：
//  1. 三条拒绝各自能被单独触发，且理由说得出是哪一条；
//  2. 零改动快路径（没读记录）不会多付一次 syscall。

// anchorSeqReader 让 usnAnchorReader 依次返回给定的坐标，并记下被调了几次。
// 这正是 t14 的核心：开头那次 cur 与读完之后的 post 是**两次不同的调用**。
func anchorSeqReader(t *testing.T, seq []usnAnchor, errs []error) *int {
	t.Helper()
	old := usnAnchorReader
	calls := 0
	usnAnchorReader = func(string) (usnAnchor, error) {
		i := calls
		calls++
		if i >= len(seq) {
			// 多调了一次说明代码行为超出预期：返回零值让断言炸出来。
			return usnAnchor{}, errors.New("意料之外的额外坐标读取")
		}
		var err error
		if i < len(errs) {
			err = errs[i]
		}
		return seq[i], err
	}
	t.Cleanup(func() { usnAnchorReader = old })
	return &calls
}

func TestRecheckReadWindowRejects(t *testing.T) {
	prev := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}

	cases := []struct {
		name string
		post usnAnchor
		err  error
		want string
	}{
		{
			name: "复查读不到",
			post: usnAnchor{},
			err:  errors.New("Access is denied"),
			want: "复查坐标失败",
		},
		{
			name: "复查到的坐标不可信",
			post: usnAnchor{JournalID: 0x5150, First: 0, Next: 950},
			want: "不可信",
		},
		{
			name: "读取期间日志被重建",
			post: usnAnchor{JournalID: 0x9999, First: 10, Next: 950},
			want: "被重建",
		},
		{
			name: "锚点之后那段被挤出窗口",
			post: usnAnchor{JournalID: 0x5150, First: 901, Next: 950}, // First > prev.Next=900
			want: "残缺的一段",
		},
	}
	for _, tc := range cases {
		if ok, why := recheckReadWindow(prev, tc.post, tc.err); ok || !strings.Contains(why, tc.want) {
			t.Fatalf("%s：应当拒绝并提到 %q，实际 ok=%v why=%q", tc.name, tc.want, ok, why)
		}
	}

	// 边界：First 正好等于 prev.Next 说明我们要的那一段还在窗口里最后一个位置 → 放行。
	ok := usnAnchor{JournalID: 0x5150, First: 900, Next: 950}
	if good, why := recheckReadWindow(prev, ok, nil); !good {
		t.Fatalf("First == prev.Next 应当放行，实际拒绝：%s", why)
	}
	// 窗口往后长了很多（新增记录）也不影响：我们只关心起点有没有被挤掉。
	if good, why := recheckReadWindow(prev, usnAnchor{JournalID: 0x5150, First: 895, Next: 999999}, nil); !good {
		t.Fatalf("First 在锚点之前应当放行，实际拒绝：%s", why)
	}
}

// 读了记录 → 一定复查；两次读坐标是**两次独立调用**（不是复用开头那次）。
func TestUsnCacheStillCleanRechecksAfterReading(t *testing.T) {
	prev := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 0x5150, First: 100, Next: 950}  // 读之前：多了 50 字节记录
	post := usnAnchor{JournalID: 0x5150, First: 100, Next: 951} // 读之后：窗口没缩，起点还在
	calls := anchorSeqReader(t, []usnAnchor{cur, post}, nil)
	oldD := usnDeltaReader
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) {
		return 3, 0, nil // 三条记录，全是 CLOSE 这类无改动的
	}
	t.Cleanup(func() { usnDeltaReader = oldD })

	cache := &scanCache{Root: `C:\`, Usn: prev}
	clean, why := usnCacheStillClean(cache, `C:\`)
	if !clean {
		t.Fatalf("应当判定为干净，实际拒绝：%s", why)
	}
	if *calls != 2 {
		t.Fatalf("读了记录就必须复查一次（共 2 次坐标读取），实际 %d 次", *calls)
	}
}

// 复查发现锚点之后那一段在读的过程中被挤掉了 → 回落，理由必须点明「残缺」。
func TestUsnCacheStillCleanTruncatedDuringRead(t *testing.T) {
	prev := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 0x5150, First: 100, Next: 950}
	post := usnAnchor{JournalID: 0x5150, First: 990, Next: 1990} // 读的时候日志被 purge 了
	anchorSeqReader(t, []usnAnchor{cur, post}, nil)
	oldD := usnDeltaReader
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) { return 1, 0, nil }
	t.Cleanup(func() { usnDeltaReader = oldD })

	cache := &scanCache{Root: `C:\`, Usn: prev}
	clean, why := usnCacheStillClean(cache, `C:\`)
	if clean {
		t.Fatal("读取期间被截断时必须回落，不许当成干净")
	}
	if !strings.Contains(why, "残缺") {
		t.Fatalf("理由应当点明读到的是残缺的一段，实际 %q", why)
	}
}

// 复查这一次读失败 → 也回落（不许当成没问题）。
func TestUsnCacheStillCleanRecheckReadFails(t *testing.T) {
	prev := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 0x5150, First: 100, Next: 950}
	anchorSeqReader(t, []usnAnchor{cur, {}}, []error{nil, errors.New("设备未就绪")})
	oldD := usnDeltaReader
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) { return 1, 0, nil }
	t.Cleanup(func() { usnDeltaReader = oldD })

	cache := &scanCache{Root: `C:\`, Usn: prev}
	clean, why := usnCacheStillClean(cache, `C:\`)
	if clean {
		t.Fatal("复查读不到时必须回落")
	}
	if !strings.Contains(why, "复查坐标失败") {
		t.Fatalf("理由应当说明复查失败，实际 %q", why)
	}
}

// 零改动快路径：一条新记录都没有 → 不读记录、不复查，坐标只读一次。
// 这条是「不许给快路径加开销」的可验证形式。
func TestUsnCacheStillCleanFastPathNoExtraSyscall(t *testing.T) {
	prev := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	cur := usnAnchor{JournalID: 0x5150, First: 100, Next: 900} // Next 没动
	calls := anchorSeqReader(t, []usnAnchor{cur}, nil)
	oldD := usnDeltaReader
	deltaCalls := 0
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) {
		deltaCalls++
		return 0, 0, nil
	}
	t.Cleanup(func() { usnDeltaReader = oldD })

	cache := &scanCache{Root: `C:\`, Usn: prev}
	clean, why := usnCacheStillClean(cache, `C:\`)
	if !clean {
		t.Fatalf("零改动应当放行，实际拒绝：%s", why)
	}
	if *calls != 1 {
		t.Fatalf("快路径只能读一次坐标，实际 %d 次", *calls)
	}
	if deltaCalls != 0 {
		t.Fatalf("快路径不该读记录，实际 %d 次", deltaCalls)
	}
}

// trustsAnchor 这一侧（决定能不能落盘锚点）同样要复查：截断时只能返回零值，
// 并且必须说清是哪一条不过——拒绝理由被丢掉的话，日志里就什么都看不到。
func TestTrustworthyAnchorRechecksAfterReading(t *testing.T) {
	before := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	after := usnAnchor{JournalID: 0x5150, First: 100, Next: 950}
	post := usnAnchor{JournalID: 0x5150, First: 1200, Next: 2200} // 读的过程中被 purge
	anchorSeqReader(t, []usnAnchor{after, post}, nil)
	oldD := usnDeltaReader
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) { return 1, 0, nil }
	t.Cleanup(func() { usnDeltaReader = oldD })

	got, why := trustworthyAnchor("C", before, nil)
	if got.valid() {
		t.Fatalf("读取期间被截断时不许落盘锚点，实际拿到 %+v", got)
	}
	if why == "" {
		t.Fatal("拒绝时理由不能为空——这一层拒了却不说为什么，排查只能靠猜")
	}
}
