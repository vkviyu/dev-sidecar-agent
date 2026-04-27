package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// =====================================================================
// 1. MCP Server 初始化与工具注册
// =====================================================================

// setupMCPServer 提取公共逻辑：无论哪种传输层，大模型能用的超能力都是一致的
func setupMCPServer() *server.MCPServer {
	// 创建 MCP Server 实例
	s := server.NewMCPServer("dev-sidecar-agent", "1.0.0")

	// 注册工具：获取 L7 HTTP 流量记录
	getHttpTrafficTool := mcp.NewTool("get_http_traffic",
		mcp.WithDescription("获取 MITM 代理捕获的 L7 HTTP 请求/响应记录，包含完整的 Headers、Body、耗时等。"),
		mcp.WithNumber("limit", mcp.Description("返回的记录数量限制，默认 50")),
		mcp.WithArray("trace_ids", mcp.Description("按 Trace-ID 列表过滤（可选）"), mcp.WithStringItems()),
	)
	s.AddTool(getHttpTrafficTool, handleGetHttpTraffic)

	// 注册工具：获取被接管子进程的标准输出/错误
	getAppStdoutTool := mcp.NewTool("get_app_stdout",
		mcp.WithDescription("获取被接管子进程的 Stdout/Stderr 输出（仅当 app.command 配置了子进程时有数据）。支持按 Trace-ID 过滤或按时间窗口关联请求。如需读取磁盘日志文件请使用 read_log_files 工具。"),
		mcp.WithString("trace_id", mcp.Description("按 Trace-ID 过滤日志（可选）")),
		mcp.WithString("start_time", mcp.Description("时间窗口起始（ISO 8601 格式，如 2025-01-01T00:00:00Z，可选）")),
		mcp.WithString("end_time", mcp.Description("时间窗口结束（ISO 8601 格式，可选）")),
		mcp.WithNumber("limit", mcp.Description("返回的日志条数限制，默认 200")),
	)
	s.AddTool(getAppStdoutTool, handleGetAppStdout)

	// =====================================================================
	// 运行时透视工具 (Delve Runtime Tracer) —— 仅在 -debug 模式下生效
	// =====================================================================

	// 注册工具：列出所有 Goroutine
	listGoroutinesTool := mcp.NewTool("list_goroutines",
		mcp.WithDescription("列出目标进程所有 Goroutine 及其当前状态和代码位置（需要启用 -debug 模式）。"),
	)
	s.AddTool(listGoroutinesTool, handleListGoroutines)

	// 注册工具：获取 Goroutine 调用栈
	getStackTraceTool := mcp.NewTool("get_stack_trace",
		mcp.WithDescription("获取指定 Goroutine 的完整函数调用栈，包含每一帧的函数参数和局部变量（需要启用 -debug 模式）。"),
		mcp.WithNumber("goroutine_id", mcp.Required(), mcp.Description("Goroutine ID")),
		mcp.WithNumber("depth", mcp.Description("栈帧深度限制，默认 20")),
	)
	s.AddTool(getStackTraceTool, handleGetStackTrace)

	// 注册工具：求值变量/表达式
	evalVariableTool := mcp.NewTool("eval_variable",
		mcp.WithDescription("在指定 Goroutine 的指定栈帧中求值 Go 表达式，获取变量的真实运行时值（需要启用 -debug 模式）。"),
		mcp.WithNumber("goroutine_id", mcp.Required(), mcp.Description("Goroutine ID")),
		mcp.WithNumber("frame", mcp.Description("栈帧索引，默认 0（当前帧）")),
		mcp.WithString("expr", mcp.Required(), mcp.Description("要求值的 Go 表达式（如变量名、结构体字段等）")),
	)
	s.AddTool(evalVariableTool, handleEvalVariable)

	// 注册工具：设置动态追踪点
	setTracepointTool := mcp.NewTool("set_tracepoint",
		mcp.WithDescription("在指定源码位置设置动态追踪点，命中时自动捕获调用栈和变量但不阻塞执行（需要启用 -debug 模式）。"),
		mcp.WithString("file", mcp.Required(), mcp.Description("源代码文件路径")),
		mcp.WithNumber("line", mcp.Required(), mcp.Description("行号")),
	)
	s.AddTool(setTracepointTool, handleSetTracepoint)

	// 注册工具：清除追踪点
	clearTracepointTool := mcp.NewTool("clear_tracepoint",
		mcp.WithDescription("清除指定 ID 的追踪点（需要启用 -debug 模式）。"),
		mcp.WithNumber("breakpoint_id", mcp.Required(), mcp.Description("追踪点/断点 ID")),
	)
	s.AddTool(clearTracepointTool, handleClearTracepoint)

	// =====================================================================
	// 动态附加工具 —— 运行时附加/断开外部进程
	// =====================================================================

	// 注册工具：附加到外部进程
	attachProcessTool := mcp.NewTool("attach_process",
		mcp.WithDescription("动态附加 Delve 调试器到一个已运行的外部进程（通过 PID）。附加后可使用所有调试工具（list_goroutines/get_stack_trace/eval_variable/set_tracepoint）。适用于业务程序通过 go run 或独立启动的场景。"),
		mcp.WithNumber("pid", mcp.Required(), mcp.Description("目标进程的 PID")),
	)
	s.AddTool(attachProcessTool, handleAttachProcess)

	// 注册工具：断开调试器
	detachProcessTool := mcp.NewTool("detach_process",
		mcp.WithDescription("断开当前附加的 Delve 调试器。目标进程不会被终止，会继续正常运行。"),
	)
	s.AddTool(detachProcessTool, handleDetachProcess)

	// =====================================================================
	// Map Remote 动态管理工具 —— 对标 Charles Proxy 的运行时规则管理
	// =====================================================================

	listMapRemoteTool := mcp.NewTool("list_map_remote",
		mcp.WithDescription("列出当前所有 Map Remote 规则及其启用状态。"),
	)
	s.AddTool(listMapRemoteTool, handleListMapRemote)

	addMapRemoteTool := mcp.NewTool("add_map_remote",
		mcp.WithDescription("新增一条 Map Remote 规则。将匹配 from 条件的请求透明重写到 to 目标地址。"),
		mcp.WithString("name", mcp.Required(), mcp.Description("规则名称")),
		mcp.WithString("from_protocol", mcp.Description("匹配的协议: http/https/* (默认 *)")),
		mcp.WithString("from_host", mcp.Required(), mcp.Description("匹配的 Host (支持正则，如 .*\\.production\\.com)")),
		mcp.WithString("from_port", mcp.Description("匹配的端口 (默认 *)")),
		mcp.WithString("from_path", mcp.Description("匹配的路径 (支持正则，默认 .*)")),
		mcp.WithString("from_query", mcp.Description("匹配的 Query String (支持正则，默认不限)")),
		mcp.WithString("to_protocol", mcp.Description("目标协议 (默认 http)")),
		mcp.WithString("to_host", mcp.Required(), mcp.Description("目标 Host")),
		mcp.WithString("to_port", mcp.Description("目标端口 (默认 *)")),
		mcp.WithString("to_path", mcp.Description("目标路径 (默认 *)")),
		mcp.WithString("to_query", mcp.Description("目标 Query String (默认不修改)")),
	)
	s.AddTool(addMapRemoteTool, handleAddMapRemote)

	removeMapRemoteTool := mcp.NewTool("remove_map_remote",
		mcp.WithDescription("根据规则 ID 删除一条 Map Remote 规则。"),
		mcp.WithString("id", mcp.Required(), mcp.Description("规则 ID")),
	)
	s.AddTool(removeMapRemoteTool, handleRemoveMapRemote)

	toggleMapRemoteTool := mcp.NewTool("toggle_map_remote",
		mcp.WithDescription("启用或禁用指定 ID 的 Map Remote 规则。"),
		mcp.WithString("id", mcp.Required(), mcp.Description("规则 ID")),
		mcp.WithBoolean("enable", mcp.Required(), mcp.Description("true=启用, false=禁用")),
	)
	s.AddTool(toggleMapRemoteTool, handleToggleMapRemote)

	updateMapRemoteTool := mcp.NewTool("update_map_remote",
		mcp.WithDescription("修改一条已有的 Map Remote 规则。根据 ID 查找并更新，未提供的可选字段保持原值不变。"),
		mcp.WithString("id", mcp.Required(), mcp.Description("要修改的规则 ID")),
		mcp.WithString("name", mcp.Description("规则名称（可选）")),
		mcp.WithString("from_protocol", mcp.Description("匹配的协议: http/https/*（可选）")),
		mcp.WithString("from_host", mcp.Description("匹配的 Host（可选）")),
		mcp.WithString("from_port", mcp.Description("匹配的端口（可选）")),
		mcp.WithString("from_path", mcp.Description("匹配的路径（可选）")),
		mcp.WithString("from_query", mcp.Description("匹配的 Query String（可选）")),
		mcp.WithString("to_protocol", mcp.Description("目标协议（可选）")),
		mcp.WithString("to_host", mcp.Description("目标 Host（可选）")),
		mcp.WithString("to_port", mcp.Description("目标端口（可选）")),
		mcp.WithString("to_path", mcp.Description("目标路径（可选）")),
		mcp.WithString("to_query", mcp.Description("目标 Query String（可选）")),
		mcp.WithBoolean("enable", mcp.Description("是否启用（可选）")),
	)
	s.AddTool(updateMapRemoteTool, handleUpdateMapRemote)

	getMapRemoteHitsTool := mcp.NewTool("get_map_remote_hits",
		mcp.WithDescription("获取 Map Remote 规则的命中记录，包含原始 URL 和重写后的 URL。"),
		mcp.WithNumber("limit", mcp.Description("返回的记录数量限制，默认 50")),
		mcp.WithString("rule_id", mcp.Description("按规则 ID 过滤（可选）")),
		mcp.WithArray("trace_ids", mcp.Description("按 Trace-ID 列表过滤（可选）"), mcp.WithStringItems()),
	)
	s.AddTool(getMapRemoteHitsTool, handleGetMapRemoteHits)

	// =====================================================================
	// 标记记录工具 —— 用户收藏的关键记录，跨 tab 统一管理
	// =====================================================================

	getMarkedRecordsTool := mcp.NewTool("get_marked_records",
		mcp.WithDescription("获取用户标记/收藏的关键记录。这些记录是用户在调试时从 Traffic、Map Remote Hits 中手动标记的，代表用户认为重要的上下文信息。"),
		mcp.WithString("source", mcp.Description("按来源类型过滤: traffic/hit（可选）")),
	)
	s.AddTool(getMarkedRecordsTool, handleGetMarkedRecords)

	addMarkedRecordTool := mcp.NewTool("add_marked_record",
		mcp.WithDescription("标记/收藏一条记录。根据 source 类型和 source_id 从对应存储中获取记录快照并保存到标记列表。"),
		mcp.WithString("source", mcp.Required(), mcp.Description("记录来源: traffic/hit")),
		mcp.WithString("source_id", mcp.Required(), mcp.Description("原始记录的 ID（traffic 用 trace_id，hit 用 hit id）")),
		mcp.WithString("note", mcp.Description("备注说明（可选）")),
	)
	s.AddTool(addMarkedRecordTool, handleAddMarkedRecord)

	removeMarkedRecordTool := mcp.NewTool("remove_marked_record",
		mcp.WithDescription("移除一条标记记录。"),
		mcp.WithString("id", mcp.Required(), mcp.Description("标记记录的 ID")),
	)
	s.AddTool(removeMarkedRecordTool, handleRemoveMarkedRecord)

	// =====================================================================
	// 日志文件按需读取工具 —— pull 模型，直接读磁盘文件
	// =====================================================================

	readLogFilesTool := mcp.NewTool("read_log_files",
		mcp.WithDescription("按需读取指定日志文件的最后 N 行。支持 glob 模式匹配多个文件（如 /var/log/app/*.log）。多个路径用逗号分隔。"),
		mcp.WithString("paths", mcp.Required(), mcp.Description("日志文件路径，支持 glob 模式，多个路径用逗号分隔。例: /tmp/app.log,/var/log/myapp/*.log")),
		mcp.WithNumber("tail", mcp.Description("每个文件返回的最后 N 行，默认 100")),
		mcp.WithString("pattern", mcp.Description("正则表达式过滤，只返回匹配的行（可选）")),
	)
	s.AddTool(readLogFilesTool, handleReadLogFiles)

	return s
}

// =====================================================================
// 2. 传输层 (Transports)
// =====================================================================

func StartMCPServer(port string) {
	s := setupMCPServer()

	// 封装为 Streamable HTTP Server（MCP spec 2025-03-26 推荐传输方式）
	streamableServer := server.NewStreamableHTTPServer(s)

	// 使用自定义 ServeMux，挂载 MCP 端点与 Web Dashboard 路由
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableServer)
	RegisterWebRoutes(mux)

	log.Printf("[MCP] Streamable HTTP 已启动 (端口 :%s)", port)
	log.Printf("[MCP] IDE 连接配置 URL: http://localhost:%s/mcp", port)
	log.Printf("[Web] Dashboard: http://localhost:%s", port)

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("[MCP] 服务启动失败: %v", err)
	}
}

// =====================================================================
// 3. 工具处理器 (Tool Handlers)
// =====================================================================

// handleGetHttpTraffic 获取 L7 HTTP 流量记录
func handleGetHttpTraffic(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := request.GetInt("limit", 50)
	traceIDs := request.GetStringSlice("trace_ids", nil)

	records := store.GetAll()

	// 按 TraceID 列表过滤
	if len(traceIDs) > 0 {
		idSet := make(map[string]bool, len(traceIDs))
		for _, id := range traceIDs {
			idSet[id] = true
		}
		var filtered []*TrafficRecord
		for _, rec := range records {
			if idSet[rec.TraceID] {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}

	// 截取最新 N 条
	if len(records) > limit {
		records = records[len(records)-limit:]
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// handleGetAppStdout 处理获取子进程标准输出/错误的请求
func handleGetAppStdout(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	traceID := request.GetString("trace_id", "")
	startTimeStr := request.GetString("start_time", "")
	endTimeStr := request.GetString("end_time", "")
	limit := request.GetInt("limit", 200)

	var entries []*LogEntry

	if traceID != "" {
		// 按 TraceID 精确查询
		entries = appLogStore.GetByTraceID(traceID)
	} else if startTimeStr != "" || endTimeStr != "" {
		// 按时间窗口查询（用于关联请求与日志）
		startTime := time.Time{}
		endTime := time.Now()
		if startTimeStr != "" {
			if t, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
				startTime = t
			}
		}
		if endTimeStr != "" {
			if t, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
				endTime = t
			}
		}
		entries = appLogStore.GetByTimeRange(startTime, endTime)
	} else {
		// 无过滤条件，返回最近 N 条
		entries = appLogStore.GetAll()
		if len(entries) > limit {
			entries = entries[len(entries)-limit:]
		}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("日志 JSON 序列化失败: %v", err)), nil
	}

	return mcp.NewToolResultText(string(data)), nil
}

// =====================================================================
// 4. Delve 运行时透视工具处理器
// =====================================================================

const delveNotReady = "Delve 调试器未连接。请先使用 attach_process 工具附加到目标进程（提供 PID），或在配置中设置 app.command 和 app.debug=true。"

// handleListGoroutines 列出所有 Goroutine
func handleListGoroutines(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError(delveNotReady), nil
	}

	goroutines, err := delveTracer.ListGoroutines()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("获取 Goroutine 列表失败: %v", err)), nil
	}

	data, err := json.MarshalIndent(goroutines, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// handleGetStackTrace 获取指定 Goroutine 的调用栈
func handleGetStackTrace(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError(delveNotReady), nil
	}

	goroutineID, err := request.RequireInt("goroutine_id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: goroutine_id"), nil
	}
	depth := request.GetInt("depth", 20)

	frames, err := delveTracer.GetStackTrace(int64(goroutineID), depth)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("获取调用栈失败: %v", err)), nil
	}

	data, err := json.MarshalIndent(frames, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// handleEvalVariable 求值变量/表达式
func handleEvalVariable(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError(delveNotReady), nil
	}

	goroutineID, err := request.RequireInt("goroutine_id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: goroutine_id"), nil
	}
	frame := request.GetInt("frame", 0)

	expr, err := request.RequireString("expr")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: expr"), nil
	}

	variable, err := delveTracer.EvalVariable(int64(goroutineID), frame, expr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("变量求值失败: %v", err)), nil
	}

	data, err := json.MarshalIndent(variable, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// handleSetTracepoint 设置动态追踪点
func handleSetTracepoint(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError(delveNotReady), nil
	}

	file, err := request.RequireString("file")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: file"), nil
	}
	line, err := request.RequireInt("line")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: line"), nil
	}

	tp, err := delveTracer.SetTracepoint(file, line)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("设置追踪点失败: %v", err)), nil
	}

	data, err := json.MarshalIndent(tp, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("追踪点已设置:\n%s", string(data))), nil
}

// handleClearTracepoint 清除追踪点
func handleClearTracepoint(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError(delveNotReady), nil
	}

	bpID, err := request.RequireInt("breakpoint_id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: breakpoint_id"), nil
	}

	if err := delveTracer.ClearTracepoint(bpID); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("清除追踪点失败: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("追踪点 %d 已成功清除", bpID)), nil
}

// =====================================================================
// 5. 动态附加工具处理器
// =====================================================================

// handleAttachProcess 附加 Delve 到外部进程
func handleAttachProcess(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := request.RequireInt("pid")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: pid"), nil
	}

	// 如果已有连接，先断开
	if delveTracer != nil && delveTracer.IsConnected() {
		_ = delveTracer.Stop()
		delveTracer = nil
	}

	// 创建新的 DelveTracer 并附加
	listenAddr := "127.0.0.1:2345"
	if appConfig != nil && appConfig.Delve.ListenAddr != "" {
		listenAddr = appConfig.Delve.ListenAddr
	}

	delveTracer = &DelveTracer{
		listenAddr: listenAddr,
	}

	if err := delveTracer.Attach(pid); err != nil {
		delveTracer = nil
		return mcp.NewToolResultError(fmt.Sprintf("附加到进程 %d 失败: %v", pid, err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("已成功附加到进程 PID=%d。现在可以使用 list_goroutines、get_stack_trace、eval_variable、set_tracepoint 等调试工具。", pid)), nil
}

// handleDetachProcess 断开 Delve 调试器
func handleDetachProcess(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if delveTracer == nil || !delveTracer.IsConnected() {
		return mcp.NewToolResultError("当前没有附加的调试器"), nil
	}

	if err := delveTracer.Stop(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("断开调试器失败: %v", err)), nil
	}
	delveTracer = nil

	return mcp.NewToolResultText("调试器已断开，目标进程继续正常运行。"), nil
}

// =====================================================================
// 6. Map Remote 动态管理处理器
// =====================================================================

func handleListMapRemote(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rules := routeEngine.GetRules()
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	if len(rules) == 0 {
		return mcp.NewToolResultText("当前没有 Map Remote 规则。使用 add_map_remote 工具新增规则。"), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func handleAddMapRemote(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := request.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: name"), nil
	}
	fromHost, err := request.RequireString("from_host")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: from_host"), nil
	}
	toHost, err := request.RequireString("to_host")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: to_host"), nil
	}

	rule := MapRemoteRule{
		ID:     fmt.Sprintf("rule-%d", time.Now().UnixMilli()),
		Enable: true,
		Name:   name,
		From: Location{
			Protocol: request.GetString("from_protocol", "*"),
			Host:     fromHost,
			Port:     request.GetString("from_port", "*"),
			Path:     request.GetString("from_path", ".*"),
			Query:    request.GetString("from_query", ""),
		},
		To: Location{
			Protocol: request.GetString("to_protocol", "http"),
			Host:     toHost,
			Port:     request.GetString("to_port", "*"),
			Path:     request.GetString("to_path", "*"),
			Query:    request.GetString("to_query", ""),
		},
	}

	routeEngine.AddRule(rule)
	log.Printf("[Map Remote] 动态新增规则: %s (%s)", rule.Name, rule.ID)

	data, _ := json.MarshalIndent(rule, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("规则已新增:\n%s", string(data))), nil
}

func handleRemoveMapRemote(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: id"), nil
	}

	if routeEngine.RemoveRule(id) {
		log.Printf("[Map Remote] 动态删除规则: %s", id)
		return mcp.NewToolResultText(fmt.Sprintf("规则 %s 已删除", id)), nil
	}
	return mcp.NewToolResultError(fmt.Sprintf("找不到 ID 为 %s 的规则", id)), nil
}

func handleToggleMapRemote(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: id"), nil
	}
	enable := request.GetBool("enable", true)

	if routeEngine.ToggleRule(id, enable) {
		state := "启用"
		if !enable {
			state = "禁用"
		}
		log.Printf("[Map Remote] 规则 %s 已%s", id, state)
		return mcp.NewToolResultText(fmt.Sprintf("规则 %s 已%s", id, state)), nil
	}
	return mcp.NewToolResultError(fmt.Sprintf("找不到 ID 为 %s 的规则", id)), nil
}

func handleUpdateMapRemote(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: id"), nil
	}

	// 查找现有规则作为基础
	var existing *MapRemoteRule
	for _, r := range routeEngine.GetRules() {
		if r.ID == id {
			existing = &r
			break
		}
	}
	if existing == nil {
		return mcp.NewToolResultError(fmt.Sprintf("找不到 ID 为 %s 的规则", id)), nil
	}

	// 用传入参数覆盖，未传入的字段保持原值
	updated := *existing
	if v := request.GetString("name", ""); v != "" {
		updated.Name = v
	}
	if v := request.GetString("from_protocol", ""); v != "" {
		updated.From.Protocol = v
	}
	if v := request.GetString("from_host", ""); v != "" {
		updated.From.Host = v
	}
	if v := request.GetString("from_port", ""); v != "" {
		updated.From.Port = v
	}
	if v := request.GetString("from_path", ""); v != "" {
		updated.From.Path = v
	}
	if v := request.GetString("from_query", ""); v != "" {
		updated.From.Query = v
	}
	if v := request.GetString("to_protocol", ""); v != "" {
		updated.To.Protocol = v
	}
	if v := request.GetString("to_host", ""); v != "" {
		updated.To.Host = v
	}
	if v := request.GetString("to_port", ""); v != "" {
		updated.To.Port = v
	}
	if v := request.GetString("to_path", ""); v != "" {
		updated.To.Path = v
	}
	if v := request.GetString("to_query", ""); v != "" {
		updated.To.Query = v
	}
	// enable 字段：检查是否显式传入
	if args, ok := request.Params.Arguments.(map[string]any); ok {
		if _, hasEnable := args["enable"]; hasEnable {
			updated.Enable = request.GetBool("enable", existing.Enable)
		}
	}

	routeEngine.UpdateRule(updated)
	log.Printf("[Map Remote] 动态更新规则: %s (%s)", updated.Name, updated.ID)

	data, _ := json.MarshalIndent(updated, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("规则已更新:\n%s", string(data))), nil
}

// handleGetMapRemoteHits 获取 Map Remote 命中记录
func handleGetMapRemoteHits(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := request.GetInt("limit", 50)
	ruleID := request.GetString("rule_id", "")
	traceIDs := request.GetStringSlice("trace_ids", nil)

	hits := mapRemoteHitStore.GetAll()

	// 按 rule_id 过滤
	if ruleID != "" {
		var filtered []*MapRemoteHit
		for _, h := range hits {
			if h.RuleID == ruleID {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}

	// 按 TraceID 列表过滤
	if len(traceIDs) > 0 {
		idSet := make(map[string]bool, len(traceIDs))
		for _, id := range traceIDs {
			idSet[id] = true
		}
		var filtered []*MapRemoteHit
		for _, h := range hits {
			if idSet[h.TraceID] {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}

	// 截取最新 N 条
	if len(hits) > limit {
		hits = hits[len(hits)-limit:]
	}

	if len(hits) == 0 {
		return mcp.NewToolResultText("当前没有 Map Remote 命中记录。"), nil
	}

	data, err := json.MarshalIndent(hits, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// =====================================================================
// 日志文件按需读取 Handler
// =====================================================================

type logFileResult struct {
	File  string   `json:"file"`
	Lines []string `json:"lines"`
	Error string   `json:"error,omitempty"`
}

func handleReadLogFiles(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pathsStr, err := request.RequireString("paths")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: paths"), nil
	}
	tail := request.GetInt("tail", 100)
	pattern := request.GetString("pattern", "")

	// 解析逗号分隔的路径列表
	rawPaths := strings.Split(pathsStr, ",")

	// glob 展开
	var filePaths []string
	for _, p := range rawPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			// 无效 glob 模式，当作字面路径
			filePaths = append(filePaths, p)
			continue
		}
		if len(matches) == 0 {
			// glob 无匹配，保留原始路径（readTailLines 会返回文件不存在的错误）
			filePaths = append(filePaths, p)
		} else {
			filePaths = append(filePaths, matches...)
		}
	}

	// 编译可选的正则过滤
	var re *regexp.Regexp
	if pattern != "" {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("pattern 正则编译失败: %v", err)), nil
		}
	}

	// 逐文件读取
	var results []logFileResult
	for _, fp := range filePaths {
		lines, err := readTailLines(fp, tail)
		if err != nil {
			results = append(results, logFileResult{File: fp, Error: err.Error()})
			continue
		}

		// 正则过滤
		if re != nil {
			var filtered []string
			for _, line := range lines {
				if re.MatchString(line) {
					filtered = append(filtered, line)
				}
			}
			lines = filtered
		}

		results = append(results, logFileResult{File: fp, Lines: lines})
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

// =====================================================================
// 8. 标记记录处理器
// =====================================================================

func handleGetMarkedRecords(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	source := request.GetString("source", "")
	records := markedStore.GetAll()

	if source != "" {
		var filtered []*MarkedRecord
		for _, r := range records {
			if r.Source == source {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	if len(records) == 0 {
		return mcp.NewToolResultText("当前没有标记记录。用户可在 Dashboard 中右键任意记录进行标记，或使用 add_marked_record 工具。"), nil
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("JSON 序列化失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func handleAddMarkedRecord(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	source, err := request.RequireString("source")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: source"), nil
	}
	sourceID, err := request.RequireString("source_id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: source_id"), nil
	}
	note := request.GetString("note", "")

	// 查找原始记录
	var snapshot any
	switch source {
	case "traffic":
		for _, rec := range store.GetAll() {
			if rec.TraceID == sourceID {
				snapshot = rec
				break
			}
		}
	case "hit":
		for _, h := range mapRemoteHitStore.GetAll() {
			if h.ID == sourceID {
				cp := *h
				snapshot = &cp
				break
			}
		}
	default:
		return mcp.NewToolResultError("source 必须为 traffic/hit"), nil
	}

	if snapshot == nil {
		return mcp.NewToolResultError(fmt.Sprintf("找不到 source=%s, source_id=%s 的记录（可能已被淘汰）", source, sourceID)), nil
	}

	record := &MarkedRecord{
		ID:       fmt.Sprintf("mark-%d", time.Now().UnixNano()),
		Source:   source,
		SourceID: sourceID,
		Note:     note,
		MarkedAt: time.Now(),
		Data:     snapshot,
	}

	if !markedStore.Add(record) {
		return mcp.NewToolResultError("标记存储已满，请先删除部分标记"), nil
	}

	log.Printf("[Marked] MCP 新增标记: %s (%s/%s)", record.ID, source, sourceID)
	data, _ := json.MarshalIndent(record, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("记录已标记:\n%s", string(data))), nil
}

func handleRemoveMarkedRecord(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("缺少必填参数: id"), nil
	}

	if markedStore.Remove(id) {
		log.Printf("[Marked] MCP 删除标记: %s", id)
		return mcp.NewToolResultText(fmt.Sprintf("标记 %s 已删除", id)), nil
	}
	return mcp.NewToolResultError(fmt.Sprintf("找不到 ID 为 %s 的标记记录", id)), nil
}
