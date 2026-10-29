package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(filepath.Clean(p)))
	}
	sort.Strings(out)
	return out
}

// 根目录散文件必须被列出来，`$` 开头的伪文件跳过。
func TestListRootFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pagefile.sys", "hiberfil.sys", "bootmgr"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "$MFT"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := baseNames(listRootFiles(root))
	want := []string{"bootmgr", "hiberfil.sys", "pagefile.sys"}
	if len(got) != len(want) {
		t.Fatalf("根文件=%v，期望 %v（目录和 $ 前缀都要排除）", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("根文件=%v，期望 %v", got, want)
		}
	}
}

// 根目录散文件必须进账——以前它们整个是隐形的（本机 pagefile+hiberfil 共 18 GB）。
func TestScanRootFilesCountsRootLevelFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pagefile.sys"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hiberfil.sys"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Root: root}
	sc := scanRootFiles(cfg, []string{
		filepath.Join(root, "pagefile.sys"),
		filepath.Join(root, "hiberfil.sys"),
	})
	if sc == nil {
		t.Fatal("应当返回一个 Scan")
	}
	if sc.Files != 2 {
		t.Fatalf("Files=%d，期望 2", sc.Files)
	}
	if sc.Total != 8192+4096 {
		t.Fatalf("Total=%d，期望 %d", sc.Total, 8192+4096)
	}
	// 这两类文件必须落到专用分类，否则「可调整」那档会算错
	if sc.Cats["system_pagefile"] != 8192 {
		t.Fatalf("pagefile 分类=%v，期望 system_pagefile=8192", sc.Cats)
	}
	if sc.Cats["system_hiber"] != 4096 {
		t.Fatalf("hiberfil 分类=%v，期望 system_hiber=4096", sc.Cats)
	}
	if sc.Unique+sc.Shared != sc.Total {
		t.Fatalf("恒等式被打破")
	}
}

// 一个文件都没有时不该凭空造出一个 part。
func TestScanRootFilesEmpty(t *testing.T) {
	if sc := scanRootFiles(Config{Root: t.TempDir()}, nil); sc != nil {
		t.Fatal("没有根目录文件时应返回 nil")
	}
}
