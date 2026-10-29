package main

// MFT 扫描引擎：用 $MFT 直读代替目录遍历，产出一份和 scanVolume 同形的 Scan。
//
// 为什么值得：本机实测 2.56 秒 vs 72.31 秒（28 倍）。MFT 每条记录只算一次，
// 天生免疫 junction 重复和硬链接重复，也不再受「目录读不进去」影响。
//
// 代价（和 WalkDir 的差异，都是语义上的，不是 bug）：
//   - Shared 恒为 0：记录本来就只算一次，没有「重复计入」这回事。
//     Unique 因此等于 Total——这是比遍历更准的数，不是缺了功能。
//   - Owners 仍然要问系统（MFT 里只有 $Secure 的编号），所以只对 ≥4MB 的文件
//     走一次 ownerOf，和 scanVolume 的规则保持一致。
//   - Errors 记的是路径还原不出来的记录数。
//
// 任何一步失败（没权限打开卷、不是 NTFS、结构不认识）都返回 ok=false，
// 调用方必须回落到 Walking 路径——这条引擎永远不该是「唯一选项」。

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// useMftEngine 决定扫描走 MFT 直读还是目录遍历。
//
// 由 `scan` 命令默认打开，`scan --walk` 可关。**常驻路径（watch/tray/serve）保持关闭**，
// 原因在下面，是踩过的：
//
// MFT 成功时会提前 return，**不写增量缓存**。而 USN 增量缓存需要管理员权限才为真。
// 于是常驻进程（5 秒一轮）走 MFT 的话，每轮都「无缓存 → 重读一遍 MFT 表」——
// 5 秒的间隔干 5 秒的活，等于把加速用成了满负荷。那种「每轮都全量」的路径
// 比原来的增量更新差得多，不能算优化。
//
// 所以加速只给一次性、有人在等的 `scan`。常驻进程继续用它原来的增量缓存。
//
// 默认值曾经是「关闭」，条件写的是「MFT 目录数必须和目录遍历一致」。
// 那条判据本身是错的，实测过：同一台机器、同一块卷、同一份代码，
// 普通权限遍历得 241,879 个目录，提权后 258,118 —— 只因权限不同就差 16,239。
// 目录数是「谁在问」的函数，不是磁盘的事实（$Recycle.Bin、System32\config 提权才读得到）。
// MFT 读文件系统记录、不查 ACL，落在两者中间（247,800）正是它该在的位置。
// 换成能独立校验的字节：遍历 158.27 GB vs MFT 157.10 GB，差 0.74%，
// 与已知的硬链接少算量级相符。判据过了，才敢把它接到 `scan` 上。
var useMftEngine bool

// scanVolumeViaMft 尝试用 MFT 完成一次扫描。ok=false 表示不可用，调用方应回落。
func scanVolumeViaMft(cfg Config, root string) (*Scan, bool) {
	t0 := time.Now()

	norm := root
	if !strings.HasSuffix(norm, `\`) {
		norm += `\`
	}
	letter := strings.TrimSuffix(norm, `:\`)
	if len(letter) != 1 {
		return nil, false
	}

	// 只接「整卷扫描」这一种活。MFT 读的就是整个卷，没法按子目录截。
	// 文件那一侧还能用 HasPrefix 过滤掉卷外的，但 res.Dirs 是整卷的目录总数，
	// 没有对应的过滤——若 Root 是 C:\Users 这种子目录，就会把整卷 247,800 个目录
	// 报成它的目录数。宁可不接，回落遍历反而是对的。
	if !strings.EqualFold(norm, letter+`:\`) {
		return nil, false
	}

	res, paths, files, err := readMft(letter, "")
	if err != nil || res == nil {
		return nil, false
	}
	if res.Files == 0 {
		return nil, false // 读到了卷但一条文件都没有，不正常，别信
	}

	sc := &Scan{
		TS:         t0.Unix(),
		Root:       norm,
		Cats:       map[string]uint64{},
		CatsShared: map[string]uint64{},
		Owners:     map[string]uint64{},
		Protected:  map[string]uint64{},
		DayWrites:  map[string]uint64{},
		Source:     "mft",
	}
	nodes := map[string]uint64{}
	type bigFile struct {
		path  string
		bytes uint64
		mtime int64
	}
	var bigs []bigFile

	wl := make([]string, len(cfg.Whitelist))
	for i, w := range cfg.Whitelist {
		wl[i] = strings.ToLower(w)
	}
	dataDir := strings.ToLower(strings.TrimSuffix(cfg.DataDir, `\`))
	dirCat := map[string]string{}
	dirProt := map[string]string{}
	rootLen := len(norm)

	sc.Dirs = res.Dirs
	sc.Errors = res.Unresolved
	scanAdd(0, res.Dirs, 0)

	seen := 0
	for _, f := range files {
		if f.IsDir || f.Size == 0 {
			continue
		}
		path, ok := paths[f.Record]
		if !ok || !strings.HasPrefix(path, norm) {
			continue // 路径还原不出来，或不在这个卷的命名空间里
		}
		low := strings.ToLower(path)
		if dataDir != "" && (low == dataDir || strings.HasPrefix(low, dataDir+`\`)) {
			continue // 不把自己算进去
		}

		size := f.Size
		sc.Total += size
		sc.Files++
		seen++
		scanAdd(1, 0, size)
		if seen%2000 == 0 {
			scanTouch(path)
			if scanCancelled() {
				break
			}
		}

		// 分类：和 scanVolume 完全一致的规则
		dir := parentDir(path)
		cat, ok := dirCat[dir]
		if !ok {
			cat = classify(strings.ToLower(dir) + `\`)
			dirCat[dir] = cat
		}
		base := path[len(dir)+1:]
		switch strings.ToLower(base) {
		case "hiberfil.sys", "swapfile.sys":
			cat = "system_hiber"
		case "pagefile.sys":
			cat = "system_pagefile"
		case "memory.dmp":
			cat = "logs_dumps"
		}
		sc.Cats[cat] += size

		rel := strings.TrimPrefix(path, norm)
		parts := strings.Split(rel, `\`)
		for depth := 1; depth <= len(parts) && depth <= 5; depth++ {
			key := norm + strings.Join(parts[:depth], `\`)
			nodes[key] += size
		}

		if f.MTime > 0 {
			day := time.Unix(f.MTime, 0).Format("2006-01-02")
			sc.DayWrites[day] += size
		}

		if size >= 32<<20 {
			bigs = append(bigs, bigFile{path, size, f.MTime})
		}
		if size >= 4<<20 {
			if o := ownerOf(path); o != "" {
				sc.Owners[o] += size
			}
		}

		prot, pok := dirProt[dir]
		if !pok {
			lowDir := strings.ToLower(dir)
			prot = ""
			for _, w := range wl {
				if strings.Contains(lowDir, w) {
					prot = w
					break
				}
			}
			dirProt[dir] = prot
		}
		if prot == "" && strings.Contains(strings.ToLower(base), ".env") {
			prot = ".env"
		}
		if prot != "" {
			sc.Protected[prot] += size
		}
	}
	_ = rootLen

	// 深度 3 / 4 目录
	type kv struct {
		k string
		v uint64
	}
	var d3, d4 []kv
	for k, v := range nodes {
		depth := strings.Count(strings.TrimRight(k, `\`), `\`) - 1
		if depth == 3 {
			d3 = append(d3, kv{k, v})
		} else if depth == 4 && v >= 64<<20 {
			d4 = append(d4, kv{k, v})
		}
	}
	sort.Slice(d3, func(i, j int) bool { return d3[i].v > d3[j].v })
	sort.Slice(d4, func(i, j int) bool { return d4[i].v > d4[j].v })
	if len(d3) > 15 {
		d3 = d3[:15]
	}
	if len(d4) > 800 {
		d4 = d4[:800]
	}
	for _, e := range d3 {
		sc.Dirs3 = append(sc.Dirs3, DirEntry{Path: e.k, Bytes: e.v, Cat: classify(strings.ToLower(e.k + `\`))})
	}
	for _, e := range d4 {
		sc.Deep = append(sc.Deep, DirEntry{Path: e.k, Bytes: e.v, Cat: classify(strings.ToLower(e.k + `\`))})
	}

	sort.Slice(bigs, func(i, j int) bool { return bigs[i].bytes > bigs[j].bytes })
	if len(bigs) > 15 {
		bigs = bigs[:15]
	}
	for _, b := range bigs {
		sc.TopFiles = append(sc.TopFiles, FileEntry{Path: b.path, Bytes: b.bytes, MTime: b.mtime})
	}

	// MFT 每条记录只算一次，不存在「重复计入」的字节
	sc.Shared = 0
	sc.Unique = sc.Total
	sc.Duration = time.Since(t0).Seconds()
	fmt.Printf("  MFT 引擎：%d 文件 / %d 目录，耗时 %.2fs\n", sc.Files, sc.Dirs, sc.Duration)
	// 目录数的口径必须和 mft-dump 对得上：那边报的是「路径可还原 + 目录标志明确」。
	// 这里只计入路径可还原的那部分，所以必须把另一半说清楚，不能让它凭空消失。
	if res.DirUnplaced > 0 {
		fmt.Printf("  ⚠ 另有 %d 个目录记录（父引用已丢失）定位不到、未计入；"+
			"含它们则为 %d 个\n", res.DirUnplaced, int64(sc.Dirs)+int64(res.DirUnplaced))
	}
	return sc, true
}
