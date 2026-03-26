package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/rpc2"
)

// =====================================================================
// 1. Delve 运行时透视引擎 (DelveTracer)
// =====================================================================

// DelveTracer 封装 Delve Headless Server 的启动与 RPC2 客户端操作。
// 通过 JSON-RPC 连接 Delve，为 AI 提供 Goroutine 栈追踪、变量求值、动态 Tracepoint 能力。
type DelveTracer struct {
	mu         sync.Mutex
	client     *rpc2.RPCClient // Delve RPC2 客户端
	dlvCmd     *exec.Cmd       // Delve headless 进程句柄
	listenAddr string          // Delve RPC 监听地址，默认 127.0.0.1:2345
	connected  bool            // 是否已成功连接
	attachMode bool            // true=attach 外部进程, false=exec 启动新进程
}

// defaultLoadConfig 通用的变量加载配置，控制加载深度防止数据爆炸
var defaultLoadConfig = api.LoadConfig{
	FollowPointers:     true,
	MaxVariableRecurse: 2,
	MaxStringLen:       256,
	MaxArrayValues:     64,
	MaxStructFields:    -1, // 加载所有字段
}

// =====================================================================
// 2. 生命周期管理：启动与停止
// =====================================================================

// Start 通过 dlv exec 启动 Delve Headless Server，然后建立 RPC 连接。
// binaryPath 是编译好的 Go 二进制文件路径（需要 -gcflags="all=-N -l" 编译）。
// args 是传递给目标程序的命令行参数。
func (t *DelveTracer) Start(binaryPath string, args []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.listenAddr == "" {
		t.listenAddr = "127.0.0.1:2345"
	}

	// 构建 dlv exec 命令
	dlvArgs := []string{
		"exec", binaryPath,
		"--headless",
		"--listen=" + t.listenAddr,
		"--api-version=2",
		"--accept-multiclient",
		"--log=false",
	}
	// 用 "--" 分隔 dlv 参数与目标程序参数
	if len(args) > 0 {
		dlvArgs = append(dlvArgs, "--")
		dlvArgs = append(dlvArgs, args...)
	}

	t.dlvCmd = exec.Command("dlv", dlvArgs...)

	// 劫持 Delve 的 stdout/stderr，接入日志存储
	stdout, err := t.dlvCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 dlv stdout pipe: %w", err)
	}
	stderr, err := t.dlvCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取 dlv stderr pipe: %w", err)
	}

	if err := t.dlvCmd.Start(); err != nil {
		return fmt.Errorf("dlv 启动失败 (请确认已安装 delve: go install github.com/go-delve/delve/cmd/dlv@latest): %w", err)
	}

	log.Printf("[DelveTracer] dlv headless 进程已启动 (PID: %d), 监听 %s", t.dlvCmd.Process.Pid, t.listenAddr)

	// 用临时的 AppProcess scanStream 劫持 dlv 输出到全局日志
	tmpProcess := &AppProcess{}
	go tmpProcess.scanStream(stdout, "INFO")
	go tmpProcess.scanStream(stderr, "ERROR")

	// 等待 Delve RPC 端口就绪
	if err := t.waitForReady(10 * time.Second); err != nil {
		_ = t.dlvCmd.Process.Kill()
		return fmt.Errorf("等待 dlv RPC 就绪超时: %w", err)
	}

	// 建立 RPC 连接
	t.client = rpc2.NewClient(t.listenAddr)
	t.connected = true

	log.Printf("[DelveTracer] RPC 客户端已成功连接到 %s", t.listenAddr)

	// 启动后目标进程处于暂停状态，需要发送 Continue 让它运行起来
	go func() {
		// Continue 会阻塞直到下一个断点命中或程序退出
		state := <-t.client.Continue()
		if state.Exited {
			log.Printf("[DelveTracer] 目标进程已退出, ExitStatus: %d", state.ExitStatus)
		}
	}()

	return nil
}

// Attach 通过 dlv attach 附加到一个已运行的外部进程。
// 适用于业务程序通过 go run / 独立启动的场景，Agent 运行时动态附加调试。
// 附加后所有调试能力（Goroutine/Stack/Eval/Tracepoint）与 Start() 完全一致。
func (t *DelveTracer) Attach(pid int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.listenAddr == "" {
		t.listenAddr = "127.0.0.1:2345"
	}

	// 构建 dlv attach 命令
	dlvArgs := []string{
		"attach", strconv.Itoa(pid),
		"--headless",
		"--listen=" + t.listenAddr,
		"--api-version=2",
		"--accept-multiclient",
		"--log=false",
	}

	t.dlvCmd = exec.Command("dlv", dlvArgs...)

	// 劫持 Delve 的 stdout/stderr
	stdout, err := t.dlvCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 dlv stdout pipe: %w", err)
	}
	stderr, err := t.dlvCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取 dlv stderr pipe: %w", err)
	}

	if err := t.dlvCmd.Start(); err != nil {
		return fmt.Errorf("dlv attach 失败 (请确认 PID %d 存在且有权限附加): %w", pid, err)
	}

	log.Printf("[DelveTracer] dlv attach 进程已启动 (PID: %d), 附加到目标进程 %d, 监听 %s", t.dlvCmd.Process.Pid, pid, t.listenAddr)

	tmpProcess := &AppProcess{}
	go tmpProcess.scanStream(stdout, "INFO")
	go tmpProcess.scanStream(stderr, "ERROR")

	// 等待 Delve RPC 端口就绪
	if err := t.waitForReady(10 * time.Second); err != nil {
		_ = t.dlvCmd.Process.Kill()
		return fmt.Errorf("等待 dlv RPC 就绪超时: %w", err)
	}

	// 建立 RPC 连接
	t.client = rpc2.NewClient(t.listenAddr)
	t.connected = true
	t.attachMode = true

	log.Printf("[DelveTracer] RPC 客户端已成功连接到 %s (attach 模式)", t.listenAddr)

	// attach 后目标进程处于暂停状态，发送 Continue 让它恢复运行
	go func() {
		state := <-t.client.Continue()
		if state.Exited {
			log.Printf("[DelveTracer] 目标进程已退出, ExitStatus: %d", state.ExitStatus)
		}
	}()

	return nil
}

// waitForReady 轮询等待 Delve RPC 端口可达
func (t *DelveTracer) waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", t.listenAddr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("dlv RPC 端口 %s 在 %v 内未就绪", t.listenAddr, timeout)
}

// Stop 断开 RPC 连接并终止 Delve 进程。
// attach 模式下仅断开调试器，不杀死目标进程；exec 模式下会终止目标进程。
func (t *DelveTracer) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client != nil && t.connected {
		if t.attachMode {
			// attach 模式：Detach(kill=false) 仅断开，保留目标进程运行
			_ = t.client.Detach(false)
			log.Printf("[DelveTracer] RPC 客户端已断开 (attach 模式，目标进程继续运行)")
		} else {
			// exec 模式：Detach(kill=true) 会终止被调试的目标进程
			_ = t.client.Detach(true)
			log.Printf("[DelveTracer] RPC 客户端已断开")
		}
		t.connected = false
	}

	if t.dlvCmd != nil && t.dlvCmd.Process != nil {
		_ = t.dlvCmd.Process.Kill()
		_ = t.dlvCmd.Wait()
		log.Printf("[DelveTracer] dlv 进程已终止")
	}

	t.attachMode = false
	return nil
}

// IsConnected 返回 Delve RPC 是否已连接
func (t *DelveTracer) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// =====================================================================
// 3. 核心运行时透视方法
// =====================================================================

// GoroutineInfo 是对 AI 友好的 Goroutine 摘要信息
type GoroutineInfo struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	CurrentLoc string `json:"current_location"` // file:line funcName
	StartLoc   string `json:"start_location"`   // 启动位置
	ThreadID   int    `json:"thread_id"`
}

// ListGoroutines 列出目标进程所有 Goroutine
func (t *DelveTracer) ListGoroutines() ([]GoroutineInfo, error) {
	if !t.IsConnected() {
		return nil, fmt.Errorf("Delve 未连接")
	}

	goroutines, _, err := t.client.ListGoroutines(0, 0)
	if err != nil {
		return nil, fmt.Errorf("ListGoroutines 失败: %w", err)
	}

	result := make([]GoroutineInfo, 0, len(goroutines))
	for _, g := range goroutines {
		info := GoroutineInfo{
			ID:         g.ID,
			Status:     goroutineStatusString(g.Status),
			CurrentLoc: formatLocation(g.UserCurrentLoc),
			StartLoc:   formatLocation(g.StartLoc),
			ThreadID:   g.ThreadID,
		}
		result = append(result, info)
	}
	return result, nil
}

// StackFrameInfo 是对 AI 友好的栈帧信息
type StackFrameInfo struct {
	Index    int            `json:"index"`
	Location string         `json:"location"` // file:line funcName
	PC       uint64         `json:"pc"`
	Args     []VariableInfo `json:"args,omitempty"`
	Locals   []VariableInfo `json:"locals,omitempty"`
}

// VariableInfo 是对 AI 友好的变量信息
type VariableInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// GetStackTrace 获取指定 Goroutine 的调用栈
func (t *DelveTracer) GetStackTrace(goroutineID int64, depth int) ([]StackFrameInfo, error) {
	if !t.IsConnected() {
		return nil, fmt.Errorf("Delve 未连接")
	}

	if depth <= 0 {
		depth = 20
	}

	frames, err := t.client.Stacktrace(goroutineID, depth, 0, &defaultLoadConfig)
	if err != nil {
		return nil, fmt.Errorf("Stacktrace 失败: %w", err)
	}

	result := make([]StackFrameInfo, 0, len(frames))
	for i, f := range frames {
		frame := StackFrameInfo{
			Index:    i,
			Location: formatLocation(f.Location),
			PC:       f.PC,
		}
		for _, arg := range f.Arguments {
			frame.Args = append(frame.Args, formatVariable(arg))
		}
		for _, local := range f.Locals {
			frame.Locals = append(frame.Locals, formatVariable(local))
		}
		result = append(result, frame)
	}
	return result, nil
}

// EvalVariable 在指定 Goroutine 的指定栈帧中求值表达式
func (t *DelveTracer) EvalVariable(goroutineID int64, frame int, expr string) (*VariableInfo, error) {
	if !t.IsConnected() {
		return nil, fmt.Errorf("Delve 未连接")
	}

	scope := api.EvalScope{
		GoroutineID: goroutineID,
		Frame:       frame,
	}

	v, err := t.client.EvalVariable(scope, expr, defaultLoadConfig)
	if err != nil {
		return nil, fmt.Errorf("EvalVariable 失败: %w", err)
	}

	info := formatVariable(*v)
	return &info, nil
}

// TracepointInfo 追踪点的返回信息
type TracepointInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// SetTracepoint 在指定代码位置设置追踪点（命中后不阻塞，自动捕获栈和变量并继续执行）
func (t *DelveTracer) SetTracepoint(file string, line int) (*TracepointInfo, error) {
	if !t.IsConnected() {
		return nil, fmt.Errorf("Delve 未连接")
	}

	bp := &api.Breakpoint{
		File:       file,
		Line:       line,
		Tracepoint: true, // 关键：设为 trace 模式，命中后不暂停
		Goroutine:  true, // 命中时捕获 Goroutine 信息
		Stacktrace: 10,   // 自动捕获 10 层栈帧
		LoadArgs:   &defaultLoadConfig,
		LoadLocals: &defaultLoadConfig,
	}

	created, err := t.client.CreateBreakpoint(bp)
	if err != nil {
		return nil, fmt.Errorf("CreateBreakpoint (tracepoint) 失败: %w", err)
	}

	return &TracepointInfo{
		ID:   created.ID,
		Name: created.Name,
		File: created.File,
		Line: created.Line,
	}, nil
}

// ClearTracepoint 清除指定 ID 的追踪点
func (t *DelveTracer) ClearTracepoint(id int) error {
	if !t.IsConnected() {
		return fmt.Errorf("Delve 未连接")
	}

	_, err := t.client.ClearBreakpoint(id)
	if err != nil {
		return fmt.Errorf("ClearBreakpoint 失败: %w", err)
	}
	return nil
}

// =====================================================================
// 4. 辅助格式化函数
// =====================================================================

// formatLocation 将 Delve 的 Location 格式化为 "file:line funcName"
func formatLocation(loc api.Location) string {
	funcName := "<unknown>"
	if loc.Function != nil {
		funcName = loc.Function.Name()
	}
	if loc.File == "" {
		return funcName
	}
	return fmt.Sprintf("%s:%d %s", loc.File, loc.Line, funcName)
}

// formatVariable 将 Delve 的 Variable 转为 AI 友好的 VariableInfo
func formatVariable(v api.Variable) VariableInfo {
	value := v.Value
	if v.Unreadable != "" {
		value = fmt.Sprintf("<unreadable: %s>", v.Unreadable)
	}
	// 对于复合类型，如果 Value 为空，尝试用 Children 拼接
	if value == "" && len(v.Children) > 0 {
		parts := make([]string, 0, len(v.Children))
		for _, child := range v.Children {
			parts = append(parts, fmt.Sprintf("%s=%s", child.Name, child.Value))
		}
		value = "{" + strings.Join(parts, ", ") + "}"
	}
	return VariableInfo{
		Name:  v.Name,
		Type:  v.Type,
		Value: value,
	}
}

// goroutineStatusString 将 Goroutine 的 Status 数值转为可读字符串
func goroutineStatusString(status uint64) string {
	switch status {
	case 0:
		return "Idle"
	case 1:
		return "Runnable"
	case 2:
		return "Running"
	case 3:
		return "Syscall"
	case 4:
		return "Waiting"
	case 6:
		return "Dead"
	default:
		return fmt.Sprintf("Unknown(%d)", status)
	}
}

// 全局 DelveTracer 实例，供 MCP handler 使用
var delveTracer *DelveTracer
