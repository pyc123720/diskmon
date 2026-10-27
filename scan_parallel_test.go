package main

import "testing"

func TestMergeScans(t *testing.T) {
	a := &Scan{Total: 100, Files: 2, Dirs: 3, Errors: 1,
		Cats: map[string]uint64{"temp": 100}, Owners: map[string]uint64{"x": 100},
		Protected: map[string]uint64{}, DayWrites: map[string]uint64{"2026-10-27": 100},
		Dirs3:     []DirEntry{{Path: `C:\a`, Bytes: 100, Cat: "temp"}},
		TopFiles:  []FileEntry{{Path: `C:\a\f`, Bytes: 100}},
	}
	b := &Scan{Total: 300, Files: 5, Dirs: 4, Errors: 0,
		Cats: map[string]uint64{"temp": 200, "logs": 100}, Owners: map[string]uint64{"y": 300},
		Protected: map[string]uint64{"p": 1}, DayWrites: map[string]uint64{"2026-10-27": 300},
		Dirs3:     []DirEntry{{Path: `C:\b`, Bytes: 300, Cat: "temp"}},
		TopFiles:  []FileEntry{{Path: `C:\b\f`, Bytes: 300}},
	}
	m := mergeScans(`C:\`, []*Scan{a, b})
	if m.Total != 400 || m.Files != 7 || m.Dirs != 7 || m.Errors != 1 {
		t.Fatalf("汇总错误: %+v", m)
	}
	if m.Cats["temp"] != 300 || m.Cats["logs"] != 100 {
		t.Fatalf("分类汇总错误: %v", m.Cats)
	}
	if len(m.Dirs3) != 2 || m.Dirs3[0].Path != `C:\b` {
		t.Fatalf("Dirs3 应按大小排序: %+v", m.Dirs3)
	}
	if len(m.TopFiles) != 2 || m.TopFiles[0].Bytes != 300 {
		t.Fatalf("TopFiles 应按大小排序: %+v", m.TopFiles)
	}
	if m.Root != `C:\` {
		t.Fatalf("Root 未设置: %q", m.Root)
	}
}
