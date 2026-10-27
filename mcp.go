package main

import (
	"bufio"
	"io"
	"syscall"
	"unsafe"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------- MCP 入口（stdio JSON-RPC） ----------
//
// 只做"入口"：把磁盘状态、体检、方案、可撤销清理暴露成工具。
// 哪些工具允许调用、清理阈值如何，由接入方（DSH/客户端）自己配置。
// 不暴露永久删除类操作（L2 规则的 rule-run / purge）。

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type toolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

var mcpTools = []toolDef{
	{"diskmon_status", "磁盘容量、剩余空间、最近扫描摘要",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
	{"diskmon_health", "引擎/版本/进程状态",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
	{"diskmon_alerts", "最近的告警（增长/隔离/扫描）",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 200}}}},
	{"diskmon_inspect", "体检一个路径：大小、文件数、类别、能否清理（只读）",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}}},
	{"diskmon_plans", "三档清理方案的预览（保守/标准/激进）",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
	{"diskmon_quarantine_list", "当前隔离区条目（可还原）",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
	{"diskmon_clean_lazy", "执行一次懒人清理（保守方案，全部进隔离区，可撤销）",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
	{"diskmon_restore", "从隔离区还原一项",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"id": map[string]interface{}{"type": "string"}}, "required": []string{"id"}}},
}

func mcpToolByName(name string) (toolDef, bool) {
	for _, t := range mcpTools {
		if t.Name == name {
			return t, true
		}
	}
	return toolDef{}, false
}

func trayURL(cfg Config, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, path)
}

func callTray(cfg Config, path string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(trayURL(cfg, path))
	if err != nil {
		return "", fmt.Errorf("托盘服务未运行（%v），请先启动 diskmon", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 && resp.StatusCode != 303 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

func mcpCallTool(cfg Config, st *Store, name string, args map[string]interface{}) (interface{}, error) {
	switch name {
	case "diskmon_status":
		v, err := volumeStats(cfg.Root)
		if err != nil {
			return nil, err
		}
		out := map[string]interface{}{
			"root": cfg.Root, "total_gb": gb(v.Total), "used_gb": gb(v.Used), "free_gb": gb(v.Free),
			"free_pct": float64(v.Free) / float64(v.Total) * 100,
		}
		if sc := st.GetScan(); sc != nil {
			out["last_scan"] = map[string]interface{}{
				"ts": sc.TS, "files": sc.Files, "total_gb": float64(sc.Total) / 1e9,
				"duration_sec": sc.Duration, "source": sc.Source,
			}
		}
		eng := st.Engine()
		out["engine"], out["engine_running"] = eng.Engine, eng.Running
		return out, nil

	case "diskmon_health":
		return map[string]interface{}{
			"version": version, "rss_mb": memMB(), "data_dir": cfg.DataDir,
			"mute": muteStatusText(), "lazy_schedule": lazyScheduleText(cfg),
		}, nil

	case "diskmon_alerts":
		limit := 20
		if f, ok := args["limit"].(float64); ok && f > 0 {
			limit = int(f)
		}
		alerts := st.Alerts()
		if len(alerts) > limit {
			alerts = alerts[len(alerts)-limit:]
		}
		return alerts, nil

	case "diskmon_inspect":
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("path 不能为空")
		}
		p = expandPath(p)
		if st, err := os.Stat(p); err != nil || st == nil {
			return nil, fmt.Errorf("路径不存在或不可读：%s", p)
		}
		r := rehearseQuick(cfg, p, 3*time.Second)
		meta := cats[r.Cat]
		return map[string]interface{}{
			"path": p, "refuse": r.Refuse, "bytes": r.Bytes, "size": human(r.Bytes),
			"files": r.Files, "category": meta.Label, "tier": tierText[meta.Tier],
			"truncated": r.Truncated, "locked": len(r.Locked),
		}, nil

	case "diskmon_plans":
		out := map[string]interface{}{}
		for _, key := range []string{"conservative", "standard", "aggressive"} {
			p := buildPlan(cfg, st, key)
			out[key] = map[string]interface{}{
				"label": p.Label, "bytes": p.Bytes, "size": human(p.Bytes), "dirs": len(p.Dirs),
			}
		}
		return out, nil

	case "diskmon_quarantine_list":
		q := openQuarantine(cfg)
		var items []map[string]interface{}
		for _, e := range q.entries() {
			if e.Restored {
				continue
			}
			items = append(items, map[string]interface{}{
				"id": e.ID, "original": e.Original, "size": human(e.Bytes),
				"files": e.Files, "ts": e.TS, "reason": e.Reason,
			})
		}
		return items, nil

	case "diskmon_clean_lazy":
		out, err := callTray(cfg, "/lazy/start")
		if err != nil {
			return nil, err
		}
		if msg := trayError(out); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return map[string]interface{}{"started": true, "note": "清理在后台进行，全部进隔离区，可在看板撤销"}, nil

	case "diskmon_restore":
		id, _ := args["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("id 不能为空")
		}
		out, err := callTray(cfg, "/q/restore?id="+id)
		if err != nil {
			return nil, err
		}
		if msg := trayError(out); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return map[string]interface{}{"restored": true, "id": id}, nil
	}
	return nil, fmt.Errorf("未知工具：%s", name)
}

func mcpServe() error {
	// 把协议通道留在真正的 stdout 上，把其它 Println 全部导向 stderr。
	// 注意：必须复制句柄——否则 os.Stdout 被替换后旧 *File 被 GC 回收会关掉同一个句柄。
	realOut := dupStdout()
	defer realOut.Close()
	os.Stdout = os.Stderr
	_ = bufio.NewReader

	cfg := loadConfig()
	st := openStore(cfg)
	dec := bufio.NewScanner(os.Stdin)
	dec.Buffer(make([]byte, 0, 1<<20), 8<<20)
	writeResp := func(r rpcResponse) {
		r.JSONRPC = "2.0"
		b, _ := json.Marshal(r)
		b = append(b, '\n')
		_, _ = realOut.Write(b)
	}

	for dec.Scan() {
		line := strings.TrimSpace(dec.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeResp(rpcResponse{Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		switch req.Method {
		case "initialize":
			writeResp(rpcResponse{ID: req.ID, Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "diskmon", "version": version},
			}})
		case "notifications/initialized", "notifications/cancelled":
			// 通知无需回复
		case "ping":
			writeResp(rpcResponse{ID: req.ID, Result: map[string]interface{}{}})
		case "tools/list":
			writeResp(rpcResponse{ID: req.ID, Result: map[string]interface{}{"tools": mcpTools}})
		case "tools/call":
			var p struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeResp(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
				continue
			}
			if _, ok := mcpToolByName(p.Name); !ok {
				writeResp(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}})
				continue
			}
			res, err := mcpCallTool(cfg, st, p.Name, p.Arguments)
			if err != nil {
				writeResp(rpcResponse{ID: req.ID, Result: map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": err.Error()}},
				}})
				continue
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			writeResp(rpcResponse{ID: req.ID, Result: map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": string(b)}},
			}})
		default:
			writeResp(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
		}
	}
	return dec.Err()
}

// dupStdout 复制标准输出句柄，得到一个不会被 os.Stdout 生命周期影响的写端
func dupStdout() *os.File {
	cur, err := syscall.GetCurrentProcess()
	if err != nil {
		return os.Stdout
	}
	var out syscall.Handle
	r, _, _ := pDuplicateHandle.Call(uintptr(cur), os.Stdout.Fd(), uintptr(cur),
		uintptr(unsafe.Pointer(&out)), 0, 0, 2 /* DUPLICATE_SAME_ACCESS */)
	if r == 0 {
		return os.Stdout
	}
	return os.NewFile(uintptr(out), "mcp-out")
}

// trayError 从托盘返回的 HTML 里识别常见错误
func trayError(body string) string {
	for _, marker := range []string{"失败", "不存在", "票据无效", "找不到", "拒绝"} {
		if strings.Contains(body, marker) {
			idx := strings.Index(body, marker)
			start := idx - 40
			if start < 0 {
				start = 0
			}
			end := idx + 40
			if end > len(body) {
				end = len(body)
			}
			return "托盘返回错误：" + strings.TrimSpace(body[start:end])
		}
	}
	return ""
}
