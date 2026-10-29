package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 窗口几何的存/取必须能往返。这条也顺便解释一个实际现象：
// /api/geom 返回 204 但文件没落地时，是存盘失败还是 HTTP 那一层没解出来。
func TestUIGeomRoundTrip(t *testing.T) {
	// uiGeomPath 现在默认指向数据目录；这里显式指到临时目录，
	// 既不受其他用例（openStore 会设置 uiGeomFile）影响，也不碰真实用户目录。
	old := uiGeomFile
	t.Cleanup(func() { uiGeomFile = old })
	uiGeomFile = filepath.Join(t.TempDir(), "ui-window.json")

	if _, ok := loadUIGeom(); ok {
		t.Fatalf("还没有文件时不该说读到了")
	}

	want := uiGeom{X: 120, Y: 80, W: 1400, H: 900}
	if err := saveUIGeom(want); err != nil {
		t.Fatalf("存盘失败：%v（路径 %s）", err, uiGeomPath())
	}
	if _, err := os.Stat(uiGeomPath()); err != nil {
		t.Fatalf("存完文件仍不存在：%v", err)
	}
	got, ok := loadUIGeom()
	if !ok {
		t.Fatalf("存完读不回来")
	}
	if got != want {
		t.Fatalf("读回来不一致：%+v，期望 %+v", got, want)
	}

	// 太小的尺寸不认——那多半是窗口最小化时记下来的
	if err := saveUIGeom(uiGeom{X: 0, Y: 0, W: 120, H: 90}); err != nil {
		t.Fatalf("小尺寸应当被忽略而不是报错：%v", err)
	}
	if g, _ := loadUIGeom(); g != want {
		t.Fatalf("小尺寸把原值覆盖了：%+v", g)
	}

	// 目录不存在时必须自己建（首次运行时数据目录可能都还没有）
	uiGeomFile = filepath.Join(t.TempDir(), "nested", "deep", "ui-window.json")
	if err := saveUIGeom(want); err != nil {
		t.Fatalf("目录缺失时应当能自建：%v", err)
	}
}
