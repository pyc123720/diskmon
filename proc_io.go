package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ---------- 进程级 I/O 归因（近似，基于 GetProcessIoCounters） ----------
//
// 说明：这拿到的是「每个进程累计写了多少字节」，不是「写了哪个文件」。
// 路径级归因由 USN 引擎负责；两者按同一时间窗对齐，就能回答
// 「这次增长是哪个进程造成的、落在哪些目录」。

var (
	pCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	pProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	pProcess32NextW           = kernel32.NewProc("Process32NextW")
	pOpenProcess              = kernel32.NewProc("OpenProcess")
	pGetProcessIoCounters     = kernel32.NewProc("GetProcessIoCounters")
)

const (
	th32csSnapProcess = 0x00000002
	processQueryLimitedInfo = 0x1000
)

type processEntry32W struct {
	DwSize              uint32
	CntUsage            uint32
	Th32ProcessID       uint32
	Th32DefaultHeapID   uintptr
	Th32ModuleID        uint32
	CntThreads          uint32
	Th32ParentProcessID uint32
	PcPriClassBase      int32
	DwFlags             uint32
	SzExeFile           [260]uint16
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

func enumProcesses() map[uint32]string {
	out := map[uint32]string{}
	h, _, _ := pCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if h == 0 || h == invalidHandle {
		return out
	}
	defer pCloseHandle.Call(h)
	var pe processEntry32W
	pe.DwSize = uint32(unsafe.Sizeof(pe))
	r, _, _ := pProcess32FirstW.Call(h, uintptr(unsafe.Pointer(&pe)))
	for r != 0 {
		out[pe.Th32ProcessID] = syscall.UTF16ToString(pe.SzExeFile[:])
		r, _, _ = pProcess32NextW.Call(h, uintptr(unsafe.Pointer(&pe)))
	}
	return out
}

func processWriteBytes(pid uint32) (uint64, bool) {
	h, _, _ := pOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	if h == 0 || h == invalidHandle {
		return 0, false
	}
	defer pCloseHandle.Call(h)
	var io ioCounters
	ok, _, _ := pGetProcessIoCounters.Call(h, uintptr(unsafe.Pointer(&io)))
	if ok == 0 {
		return 0, false
	}
	return io.WriteTransferCount, true
}

type procSample struct {
	TS     int64
	Writes map[string]uint64 // 进程名 -> 该采样窗口的写入字节
}

var (
	procMu      sync.Mutex
	procSamples []procSample
	procTotals  = map[string]uint64{}
)

func procLoop(cfg Config) {
	prev := map[uint32]uint64{}
	names := enumProcesses()
	for pid, name := range names {
		if v, ok := processWriteBytes(pid); ok {
			prev[pid] = v
		}
		_ = name
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		names := enumProcesses()
		cur := map[uint32]uint64{}
		deltas := map[string]uint64{}
		for pid, name := range names {
			v, ok := processWriteBytes(pid)
			if !ok {
				continue
			}
			cur[pid] = v
			if old, had := prev[pid]; had && v >= old {
				d := v - old
				if d > 0 {
					deltas[name] += d
				}
			}
		}
		prev = cur
		procMu.Lock()
		procSamples = append(procSamples, procSample{TS: time.Now().Unix(), Writes: deltas})
		if len(procSamples) > 240 { // 20 分钟
			procSamples = procSamples[len(procSamples)-240:]
		}
		for k, v := range deltas {
			procTotals[k] += v
		}
		procMu.Unlock()
		persistProcSnapshot(cfg)
	}
}

// 最近窗口内各进程写入量（进程名聚合）
func procWindowSum(window time.Duration) map[string]uint64 {
	cut := time.Now().Add(-window).Unix()
	procMu.Lock()
	defer procMu.Unlock()
	out := map[string]uint64{}
	for _, s := range procSamples {
		if s.TS < cut {
			continue
		}
		for k, v := range s.Writes {
			out[k] += v
		}
	}
	return out
}

func procTotalSum() map[string]uint64 {
	procMu.Lock()
	defer procMu.Unlock()
	out := map[string]uint64{}
	for k, v := range procTotals {
		out[k] = v
	}
	return out
}

type ProcRank struct {
	Name  string
	Bytes uint64
}

func procRanks(window time.Duration, limit int) []ProcRank {
	m := procWindowSum(window)
	var out []ProcRank
	for k, v := range m {
		if v < 64*1024 {
			continue
		}
		out = append(out, ProcRank{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func printProcs() {
	ranks := procRanks(5*time.Minute, 12)
	tot := procTotalSum()
	if len(ranks) == 0 {
		if r, tt := loadProcSnapshot(getConfig()); len(r) > 0 {
			ranks, tot = r, tt
			if len(ranks) > 12 {
				ranks = ranks[:12]
			}
		}
	}
	if len(ranks) == 0 {
		fmt.Println("最近 5 分钟没有明显写入（或需要管理员权限读取 I/O 计数）")
		return
	}
	fmt.Println("最近 5 分钟写入最多的进程（近似归因，按 I/O 计数）：")
	for _, r := range ranks {
		fmt.Printf("  %-28s %10s   （累计 %s）\n", r.Name, human(r.Bytes), human(tot[r.Name]))
	}
}

// 把最近 5 分钟的进程写入排行写成文件，供命令行/其它进程读取
func persistProcSnapshot(cfg Config) {
	ranks := procRanks(5*time.Minute, 20)
	totals := procTotalSum()
	payload := map[string]interface{}{
		"ts":     time.Now().Unix(),
		"ranks":  ranks,
		"totals": totals,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = writeFileAtomic(filepath.Join(cfg.DataDir, "procs.json"), b)
}

func loadProcSnapshot(cfg Config) ([]ProcRank, map[string]uint64) {
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "procs.json"))
	if err != nil {
		return nil, nil
	}
	var payload struct {
		Ranks  []ProcRank        `json:"ranks"`
		Totals map[string]uint64 `json:"totals"`
	}
	if json.Unmarshal(b, &payload) != nil {
		return nil, nil
	}
	return payload.Ranks, payload.Totals
}
