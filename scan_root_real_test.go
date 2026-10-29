package main

import (
	"os"
	"testing"
)

// 诊断用：在真实卷根上验证「根目录散文件」确实被拾起并进账。
// 默认跳过（要在 C:\ 上跑），需要时：
//
//	$env:DISKMON_ROOT_TEST="C:\"; go test -run TestRootFilesRealVolume -v
func TestRootFilesRealVolume(t *testing.T) {
	root := os.Getenv("DISKMON_ROOT_TEST")
	if root == "" {
		t.Skip("未设置 DISKMON_ROOT_TEST，跳过真实卷根诊断")
	}
	files := listRootFiles(root)
	t.Logf("根目录散文件 %d 个：%v", len(files), files)

	sc := scanRootFiles(Config{Root: root}, files)
	if sc == nil {
		t.Fatal("scanRootFiles 返回 nil")
	}
	t.Logf("合计 %.2f GB，分类 %v", float64(sc.Total)/(1<<30), sc.Cats)
	if sc.Cats["system_pagefile"] == 0 {
		t.Errorf("pagefile.sys 没进 system_pagefile 分类（Total=%.2f GB）", float64(sc.Total)/(1<<30))
	}
	if sc.Cats["system_hiber"] == 0 {
		t.Errorf("hiberfil.sys 没进 system_hiber 分类")
	}
}
