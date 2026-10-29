package main

import (
	"strings"
	"testing"
)

// cleanPage 的 ?cat= 过滤：进入某分类、不认识的分类回落、空分类走整页。
//
// 特意不开 HTTP 服务器来测——为了验这段逻辑去起一个进程、占一个端口，
// 代价太大而且会碰到别人正在用的实例（我就是这么把用户跑着的服务停掉的）。
func TestCleanPageCategoryFilter(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	st := openStore(cfg)
	st.SetScan(&Scan{
		Root:  `C:\`,
		Total: 21 << 30,
		Cats: map[string]uint64{
			"temp":          3 << 30,
			"system_winsxs": 18 << 30,
		},
		CatsShared: map[string]uint64{"system_winsxs": 2 << 30},
		Dirs3: []DirEntry{
			{Path: `C:\Users\lenovo\AppData\Local\Temp`, Bytes: 3 << 30, Cat: "temp"},
		},
	})

	// 指定分类 → 进入过滤视图，带上分类名、该类路径、以及回到全部的口子
	got := cleanPage(cfg, st, "temp")
	for _, want := range []string{"清理中心 · 临时文件", `C:\Users\lenovo\AppData\Local\Temp`, `href="/clean"`, "正在只看这一类"} {
		if !strings.Contains(got, want) {
			t.Fatalf("过滤视图缺少 %q", want)
		}
	}
	// 不能混进别的分类的入口
	if strings.Contains(got, "媒体分类") {
		t.Fatalf("过滤视图不该还显示整页的其它卡片")
	}

	// 不认识的分类键 → 当成没传，回落整页；绝不能渲染成一片空白。
	// 判据用「正在只看这一类」而不是「清理中心 ·」——后者在 <title> 里永远都有。
	bad := cleanPage(cfg, st, "根本不存在的类")
	if strings.Contains(bad, "正在只看这一类") {
		t.Fatalf("未知分类不应进入过滤视图")
	}
	if !strings.Contains(bad, "媒体分类") {
		t.Fatalf("未知分类应回落到完整清理中心")
	}

	// 空分类 → 完整清理中心
	if full := cleanPage(cfg, st, ""); !strings.Contains(full, "媒体分类") {
		t.Fatalf("空分类应渲染完整清理中心")
	}
}

// 分类页要把「这一类里体积最大的路径」摆出来，并且用的是去重后的口径。
func TestCleanPageCategoryShowsDedupedSize(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	st := openStore(cfg)
	st.SetScan(&Scan{
		Root:       `C:\`,
		Total:      20 << 30,
		Cats:       map[string]uint64{"system_winsxs": 18 << 30},
		CatsShared: map[string]uint64{"system_winsxs": 3 << 30},
		HlProbeMB:  8,
	})

	got := cleanPage(cfg, st, "system_winsxs")
	if !strings.Contains(got, "硬链接") {
		t.Fatalf("有共享字节时应当说明硬链接口径")
	}
	// 18 - 3 = 15 GB 去重后。human() 用 %.2f GB，所以是 "15.00 GB"。
	if !strings.Contains(got, "15.00 GB") {
		t.Fatalf("应当给出 15.00 GB 去重后体积，实际没找到")
	}
	// system_winsxs 是 adjust 档：必须提醒不要手删
	if !strings.Contains(got, "不要手删") {
		t.Fatalf("adjust 档应当提示不要手删")
	}
}
