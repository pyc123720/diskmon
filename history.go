package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- 扫描快照历史（用于「对比两次扫描」） ----------

type SnapDir struct {
	Path  string `json:"p"`
	Bytes uint64 `json:"b"`
}

type Snapshot struct {
	TS    int64             `json:"ts"`
	Total uint64            `json:"total"`
	Used  uint64            `json:"used"`
	Free  uint64            `json:"free"`
	Cats  map[string]uint64 `json:"cats"`
	Dirs  []SnapDir         `json:"dirs"`
}

func historyPath(cfg Config) string { return filepath.Join(cfg.DataDir, "history.jsonl") }

func appendSnapshot(cfg Config, sc *Scan) {
	if sc == nil {
		return
	}
	snap := Snapshot{TS: sc.TS, Total: sc.Total, Used: sc.Used, Free: sc.Free, Cats: sc.Cats}
	for _, d := range sc.Deep {
		snap.Dirs = append(snap.Dirs, SnapDir{Path: d.Path, Bytes: d.Bytes})
	}
	for _, d := range sc.Dirs3 {
		snap.Dirs = append(snap.Dirs, SnapDir{Path: d.Path, Bytes: d.Bytes})
	}
	if len(snap.Dirs) > 400 {
		snap.Dirs = snap.Dirs[:400]
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	f, err := os.OpenFile(historyPath(cfg), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(string(b) + "\n")
	_ = f.Close()
	trimHistory(cfg, 60)
}

func trimHistory(cfg Config, keep int) {
	b, err := os.ReadFile(historyPath(cfg))
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) <= keep {
		return
	}
	lines = lines[len(lines)-keep:]
	_ = os.WriteFile(historyPath(cfg), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func loadSnapshots(cfg Config) []Snapshot {
	b, err := os.ReadFile(historyPath(cfg))
	if err != nil {
		return nil
	}
	var out []Snapshot
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s Snapshot
		if json.Unmarshal([]byte(line), &s) == nil && s.TS > 0 {
			out = append(out, s)
		}
	}
	return out
}

func pickBaseline(snaps []Snapshot, age time.Duration) (Snapshot, bool) {
	if len(snaps) < 2 {
		return Snapshot{}, false
	}
	target := time.Now().Add(-age).Unix()
	best := snaps[0]
	bestDiff := int64(1 << 62)
	for i := 0; i < len(snaps)-1; i++ { // 排除最后一个（当前）
		d := snaps[i].TS - target
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			bestDiff = d
			best = snaps[i]
		}
	}
	return best, true
}

type DirDelta struct {
	Path  string
	Delta int64
	Now   uint64
	Old   uint64
}

func diffSnapshots(old, cur Snapshot) (growth, shrink []DirDelta) {
	oldMap := map[string]uint64{}
	for _, d := range old.Dirs {
		oldMap[d.Path] = d.Bytes
	}
	curMap := map[string]uint64{}
	for _, d := range cur.Dirs {
		curMap[d.Path] = d.Bytes
	}
	for p, nb := range curMap {
		ob := oldMap[p]
		delta := int64(nb) - int64(ob)
		if delta > 0 {
			growth = append(growth, DirDelta{p, delta, nb, ob})
		}
	}
	for p, ob := range oldMap {
		nb, ok := curMap[p]
		if !ok {
			nb = 0
		}
		delta := int64(nb) - int64(ob)
		if delta < 0 {
			shrink = append(shrink, DirDelta{p, delta, nb, ob})
		}
	}
	sort.Slice(growth, func(i, j int) bool { return growth[i].Delta > growth[j].Delta })
	sort.Slice(shrink, func(i, j int) bool { return shrink[i].Delta < shrink[j].Delta })
	if len(growth) > 10 {
		growth = growth[:10]
	}
	if len(shrink) > 6 {
		shrink = shrink[:6]
	}
	return
}

func printDiff(cfg Config) {
	snaps := loadSnapshots(cfg)
	if len(snaps) < 2 {
		fmt.Println("快照不足（至少扫描两次才能对比）")
		return
	}
	cur := snaps[len(snaps)-1]
	base, ok := pickBaseline(snaps, 7*24*time.Hour)
	if !ok {
		fmt.Println("没有可对比的历史快照")
		return
	}
	growth, shrink := diffSnapshots(base, cur)
	fmt.Printf("对比：%s → %s\n", time.Unix(base.TS, 0).Format("01-02 15:04"), time.Unix(cur.TS, 0).Format("01-02 15:04"))
	for _, g := range growth {
		fmt.Printf("  +%10s  %s\n", human(uint64(g.Delta)), g.Path)
	}
	for _, s := range shrink {
		fmt.Printf("  -%10s  %s\n", human(uint64(-s.Delta)), s.Path)
	}
}
