package main

import (
	"strings"
	"testing"
	"time"
)

// 今日建议的核心约束：结论必须依赖实时状态，而不是永远返回同一条。
func todayStore(t *testing.T, scan *Scan, samples []Sample) (*Config, *Store) {
	t.Helper()
	cfg := defaultConfig()
	cfg.DataDir = t.TempDir()
	st := &Store{}
	st.st.Scan = scan
	st.st.Samples = samples
	return &cfg, st
}

func hasHref(items []todayItem, href string) bool {
	for _, it := range items {
		if it.Href == href {
			return true
		}
	}
	return false
}

func TestTodayBoardFirstRun(t *testing.T) {
	cfg, st := todayStore(t, nil, nil)
	b := todayBoardFor(*cfg, st)
	if len(b.Items) == 0 {
		t.Fatal("首次使用必须有建议")
	}
	if b.Items[0].Href != "/scan" {
		t.Fatalf("首次使用应引导扫描，实际 %q", b.Items[0].Href)
	}
}

func TestTodayBoardStaleScan(t *testing.T) {
	fresh := &Scan{TS: time.Now().Unix(), Files: 1000}
	cfg, st := todayStore(t, fresh, nil)
	if b := todayBoardFor(*cfg, st); hasHref(b.Items, "/scan") {
		t.Fatal("刚扫过不应再要求重扫")
	}

	old := &Scan{TS: time.Now().Add(-72 * time.Hour).Unix(), Files: 1000}
	cfg, st = todayStore(t, old, nil)
	b := todayBoardFor(*cfg, st)
	if !hasHref(b.Items, "/scan") {
		t.Fatalf("数据 3 天前应要求重扫，实际 %+v", b.Items)
	}
	if !strings.Contains(b.Basis, "3.0 天前") {
		t.Fatalf("依据行应写明数据新鲜度，实际 %q", b.Basis)
	}
}

func TestTodayBoardAnomalyGrowth(t *testing.T) {
	now := time.Now()
	samples := []Sample{
		{TS: now.AddDate(0, 0, -3).Unix(), Free: 100 << 30},
		{TS: now.AddDate(0, 0, -2).Unix(), Free: 99 << 30},
		{TS: now.AddDate(0, 0, -1).Unix(), Free: 98 << 30},
		{TS: now.Unix(), Free: 92 << 30}, // 今天涨 6 GB，远高于中位
	}
	cfg, st := todayStore(t, &Scan{TS: now.Unix(), Files: 1000}, samples)
	b := todayBoardFor(*cfg, st)
	if !hasHref(b.Items, "/trend") {
		t.Fatalf("今日异常增长应指向趋势页，实际 %+v", b.Items)
	}
	if !strings.Contains(strings.Join(itemTitles(b), "|"), "比平时快") {
		t.Fatalf("应说明比平时快，实际 %v", itemTitles(b))
	}
	if !strings.Contains(b.Basis, "今日净增") {
		t.Fatalf("依据行应包含今日净增，实际 %q", b.Basis)
	}
}

func TestTodayBoardQuiet(t *testing.T) {
	now := time.Now()
	samples := []Sample{
		{TS: now.AddDate(0, 0, -3).Unix(), Free: 100 << 30},
		{TS: now.AddDate(0, 0, -2).Unix(), Free: 100 << 30},
		{TS: now.AddDate(0, 0, -1).Unix(), Free: 100 << 30},
		{TS: now.Unix(), Free: 100 << 30},
	}
	cfg, st := todayStore(t, &Scan{TS: now.Unix(), Files: 1000}, samples)
	b := todayBoardFor(*cfg, st)
	if len(b.Items) != 1 || b.Items[0].Kind != "quiet" {
		t.Fatalf("无事时应只给一条安静建议，实际 %+v", b.Items)
	}
}

func TestTodayBoardAtMostThreeItems(t *testing.T) {
	now := time.Now()
	// 近三天每天净增 10 GB：中位增速把「写满天数」压到 7 天以内，触发 urgent。
	samples := []Sample{
		{TS: now.AddDate(0, 0, -3).Unix(), Free: 100 << 30},
		{TS: now.AddDate(0, 0, -2).Unix(), Free: 90 << 30},
		{TS: now.AddDate(0, 0, -1).Unix(), Free: 80 << 30},
		{TS: now.Unix(), Free: 70 << 30},
	}
	cfg, st := todayStore(t, &Scan{TS: now.Add(-72 * time.Hour).Unix(), Files: 1000,
		Dirs3: []DirEntry{{Path: `C:\x\Temp`, Bytes: 3 << 30, Cat: "temp"}}}, samples)
	b := todayBoardFor(*cfg, st)
	if len(b.Items) > 3 {
		t.Fatalf("最多三条，实际 %d", len(b.Items))
	}
	if len(b.Items) == 0 || b.Items[0].Kind != "urgent" {
		t.Fatalf("空间紧张时首条应为 urgent，实际 %+v", b.Items)
	}
}

func itemTitles(b todayBoard) []string {
	out := make([]string, 0, len(b.Items))
	for _, it := range b.Items {
		out = append(out, it.Title)
	}
	return out
}
