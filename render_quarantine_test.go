package main

import (
	"strings"
	"testing"
)

// 隔离区卡片必须自己声明它是逻辑删除，不是安全擦除。
//
// 这句话原本只写在 SECURITY.md 里——一个用户点开「隔离区」之后不会去翻的地方。
// 卡片标题写的是「可还原」，旁边的按钮写的是「还原」，读起来很像「已经安全处理」；
// 而保留期到点那次删除只是把文件从文件系统摘掉，字节仍留在介质上。
// 把它放在标题下面，是让看见「还原」的人同时看见这件事。
func TestQuarantineCardSaysItIsNotAnErase(t *testing.T) {
	cfg := Config{Root: `C:\`, DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	st := openStore(cfg)
	st.SetScan(&Scan{Root: `C:\`, Total: 21 << 30})

	got := renderPage(cfg, st)
	for _, want := range []string{"逻辑删除", "不是安全擦除", "不可恢复"} {
		if !strings.Contains(got, want) {
			t.Fatalf("隔离区卡片缺少 %q——「可还原」容易被读成「安全处理」，这层意思不能丢", want)
		}
	}
}
