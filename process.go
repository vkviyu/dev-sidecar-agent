package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// =====================================================================
// 1. 日志数据结构与存储
// =====================================================================

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"` // stdout为INFO, stderr为ERROR (简单区分)
	TraceID   string    `json:"trace_id"`
	Message   string    `json:"message"`
}

type LogStore struct {
	mu      sync.RWMutex
	entries []*LogEntry
	maxSize int
}

func NewLogStore(maxSize int) *LogStore {
	return &LogStore{
		entries: make([]*LogEntry, 0),
		maxSize: maxSize,
	}
}

func (s *LogStore) Add(entry *LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	if len(s.entries) > s.maxSize {
		// Ring buffer, discard oldest
		s.entries = s.entries[len(s.entries)-s.maxSize:]
	}
}

func (s *LogStore) GetAll() []*LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*LogEntry{}, s.entries...) // return copy
}

func (s *LogStore) GetByTraceID(traceID string) []*LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*LogEntry
	for _, entry := range s.entries {
		if entry.TraceID == traceID {
			result = append(result, entry)
		}
	}
	return result
}

// GetByTimeRange 按时间窗口查询日志，用于将请求与业务日志进行时间关联
func (s *LogStore) GetByTimeRange(start, end time.Time) []*LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*LogEntry
	for _, entry := range s.entries {
		if !entry.Timestamp.Before(start) && !entry.Timestamp.After(end) {
			result = append(result, entry)
		}
	}
	return result
}

// 全局日志存储，由 main() 中根据配置初始化
var appLogStore *LogStore

// =====================================================================
// 2. 子进程接管引擎
// =====================================================================

type AppProcess struct {
	CmdStr string
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func NewAppProcess(cmdStr string) *AppProcess {
	return &AppProcess{
		CmdStr: cmdStr,
	}
}

// Start 启动目标进程并劫持输出
func (p *AppProcess) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	parts := strings.Fields(p.CmdStr)
	if len(parts) == 0 {
		return nil
	}

	p.cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)

	// 劫持 stdout
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return err
	}

	// 劫持 stderr
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		return err
	}

	// 启动进程
	if err := p.cmd.Start(); err != nil {
		return err
	}

	log.Printf("🚀 [Runtime Tracer] 已成功接管目标进程: %s (PID: %d)", p.CmdStr, p.cmd.Process.Pid)

	// 开启协程读取日志
	go p.scanStream(stdout, "INFO")
	go p.scanStream(stderr, "ERROR")

	// 开启协程等待进程退出
	go func() {
		err := p.cmd.Wait()
		if err != nil {
			log.Printf("⚠️ [Runtime Tracer] 目标进程退出: %v", err)
		} else {
			log.Printf("✅ [Runtime Tracer] 目标进程正常退出")
		}
	}()

	return nil
}

// Stop 优雅关闭进程
func (p *AppProcess) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		// 给它发个信号，或者 context 已经帮忙杀掉了
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
}

// =====================================================================
// 3. 日志解析与 TraceID 提取
// =====================================================================

// 匹配 trace_id=xxx, trace_id:xxx, x-dev-trace-id:xxx 等格式，提取 UUID 或字符串
var traceIDRegex = regexp.MustCompile(`(?i)(?:trace[-_]?id)["']?\s*[:=]\s*["']?([a-zA-Z0-9-]+)["']?`)

// parseLine 解析单行日志，提取 TraceID 并写入全局日志存储
func parseLine(line string, defaultLevel string) {
	traceID := ""
	matches := traceIDRegex.FindStringSubmatch(line)
	if len(matches) > 1 {
		traceID = matches[1]
	}

	entry := &LogEntry{
		Timestamp: time.Now(),
		Level:     defaultLevel,
		TraceID:   traceID,
		Message:   line,
	}
	appLogStore.Add(entry)

	log.Printf("[APP %s] %s", defaultLevel, line)
}

func (p *AppProcess) scanStream(stream io.Reader, defaultLevel string) {
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		parseLine(scanner.Text(), defaultLevel)
	}
}

// =====================================================================
// 4. 按需读取日志文件（pull 模型）
// =====================================================================

// readTailLines 从文件末尾逆向读取最后 n 行，避免全文件加载。
func readTailLines(filePath string, n int) ([]string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size == 0 {
		return nil, nil
	}

	// 从文件末尾逆向扫描换行符
	buf := make([]byte, 0, 4096)
	offset := size
	lineCount := 0
	const chunkSize int64 = 4096

	for offset > 0 && lineCount <= n {
		readSize := chunkSize
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}

		// 预置到 buf 前面
		buf = append(chunk, buf...)

		// 计算当前 chunk 中的换行符数量
		for _, b := range chunk {
			if b == '\n' {
				lineCount++
			}
		}
	}

	// 按换行分割，取最后 n 行
	allLines := strings.Split(string(buf), "\n")
	// 去掉末尾可能的空行
	for len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}
	if len(allLines) > n {
		allLines = allLines[len(allLines)-n:]
	}

	return allLines, nil
}
