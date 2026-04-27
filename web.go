package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// =====================================================================
// 嵌入前端静态资源
// =====================================================================

//go:embed web/*
var webFS embed.FS

// =====================================================================
// REST API Handlers
// =====================================================================

// handleAPITraffic 返回 L7 HTTP 流量记录列表
func handleAPITraffic(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	traceID := r.URL.Query().Get("trace_id")
	traceIDsRaw := r.URL.Query().Get("trace_ids")

	records := store.GetAll()

	// 按 TraceID 过滤（trace_ids 优先，回退到 trace_id 单个）
	if traceIDsRaw != "" {
		ids := strings.Split(traceIDsRaw, ",")
		idSet := make(map[string]bool, len(ids))
		for _, id := range ids {
			if t := strings.TrimSpace(id); t != "" {
				idSet[t] = true
			}
		}
		var filtered []*TrafficRecord
		for _, rec := range records {
			if idSet[rec.TraceID] {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	} else if traceID != "" {
		var filtered []*TrafficRecord
		for _, rec := range records {
			if rec.TraceID == traceID {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}

	// 截取最新 N 条
	if len(records) > limit {
		records = records[len(records)-limit:]
	}

	writeJSON(w, records)
}

// handleAPILogs 返回目标进程的业务日志
// 支持两种模式：
//   - 无 paths 参数：从 Ring Buffer 读取 stdout/stderr 捕获的日志（原有行为）
//   - 有 paths 参数：按需从磁盘读取日志文件（pull 模型）
func handleAPILogs(w http.ResponseWriter, r *http.Request) {
	pathsStr := r.URL.Query().Get("paths")

	// pull 模式：按需读取磁盘日志文件
	if pathsStr != "" {
		tail := queryInt(r, "tail", 100)
		pattern := r.URL.Query().Get("pattern")

		rawPaths := strings.Split(pathsStr, ",")
		var filePaths []string
		for _, p := range rawPaths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			matches, err := filepath.Glob(p)
			if err != nil || len(matches) == 0 {
				filePaths = append(filePaths, p)
			} else {
				filePaths = append(filePaths, matches...)
			}
		}

		var re *regexp.Regexp
		if pattern != "" {
			var err error
			re, err = regexp.Compile(pattern)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"invalid pattern: %v"}`, err), http.StatusBadRequest)
				return
			}
		}

		type fileResult struct {
			File  string   `json:"file"`
			Lines []string `json:"lines"`
			Error string   `json:"error,omitempty"`
		}
		var results []fileResult
		for _, fp := range filePaths {
			lines, err := readTailLines(fp, tail)
			if err != nil {
				results = append(results, fileResult{File: fp, Error: err.Error()})
				continue
			}
			if re != nil {
				var filtered []string
				for _, line := range lines {
					if re.MatchString(line) {
						filtered = append(filtered, line)
					}
				}
				lines = filtered
			}
			results = append(results, fileResult{File: fp, Lines: lines})
		}

		writeJSON(w, results)
		return
	}

	// push 模式：从 Ring Buffer 读取 stdout/stderr 日志
	limit := queryInt(r, "limit", 200)
	traceID := r.URL.Query().Get("trace_id")
	startTimeStr := r.URL.Query().Get("start_time")
	endTimeStr := r.URL.Query().Get("end_time")

	var entries []*LogEntry

	if traceID != "" {
		entries = appLogStore.GetByTraceID(traceID)
	} else if startTimeStr != "" || endTimeStr != "" {
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
		entries = appLogStore.GetAll()
		if len(entries) > limit {
			entries = entries[len(entries)-limit:]
		}
	}

	writeJSON(w, entries)
}

// handleAPIConfig 返回当前运行时配置
func handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, appConfig)
}

// handleAPIMapRemote Map Remote 规则的 CRUD 操作
func handleAPIMapRemote(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, routeEngine.GetRules())

	case http.MethodPost:
		var rule MapRemoteRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("rule-%d", time.Now().UnixMilli())
		}
		routeEngine.AddRule(rule)
		log.Printf("[Map Remote] Web 新增规则: %s (%s)", rule.Name, rule.ID)
		writeJSON(w, rule)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing required parameter: id"}`, http.StatusBadRequest)
			return
		}
		if routeEngine.RemoveRule(id) {
			log.Printf("[Map Remote] Web 删除规则: %s", id)
			writeJSON(w, map[string]string{"status": "deleted", "id": id})
		} else {
			http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		}

	case http.MethodPut:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing required parameter: id"}`, http.StatusBadRequest)
			return
		}

		// 尝试解析 Body：有 Body 则为完整更新，无 Body 则为 toggle
		var rule MapRemoteRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err == nil && rule.From.Host != "" {
			// 完整更新模式
			rule.ID = id
			if routeEngine.UpdateRule(rule) {
				log.Printf("[Map Remote] Web 更新规则: %s (%s)", rule.Name, rule.ID)
				writeJSON(w, rule)
			} else {
				http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
			}
		} else {
			// toggle 模式（向后兼容）
			enableStr := r.URL.Query().Get("enable")
			enable := enableStr != "false"
			if routeEngine.ToggleRule(id, enable) {
				writeJSON(w, map[string]any{"id": id, "enable": enable})
			} else {
				http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
			}
		}

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleAPIMapRemoteHits 返回 Map Remote 命中记录
func handleAPIMapRemoteHits(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	ruleID := r.URL.Query().Get("rule_id")
	traceIDsRaw := r.URL.Query().Get("trace_ids")

	hits := mapRemoteHitStore.GetAll()

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
	if traceIDsRaw != "" {
		ids := strings.Split(traceIDsRaw, ",")
		idSet := make(map[string]bool, len(ids))
		for _, id := range ids {
			if t := strings.TrimSpace(id); t != "" {
				idSet[t] = true
			}
		}
		var filtered []*MapRemoteHit
		for _, h := range hits {
			if idSet[h.TraceID] {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}

	if len(hits) > limit {
		hits = hits[len(hits)-limit:]
	}

	writeJSON(w, hits)
}

// handleAPIMarked 标记记录的 CRUD
func handleAPIMarked(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		source := r.URL.Query().Get("source")
		records := markedStore.GetAll()
		if source != "" {
			var filtered []*MarkedRecord
			for _, rec := range records {
				if rec.Source == source {
					filtered = append(filtered, rec)
				}
			}
			records = filtered
		}
		writeJSON(w, records)

	case http.MethodPost:
		var req struct {
			Source   string `json:"source"`
			SourceID string `json:"source_id"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if req.Source == "" || req.SourceID == "" {
			http.Error(w, `{"error":"source and source_id are required"}`, http.StatusBadRequest)
			return
		}

		// 根据 source 类型查找原始记录
		var snapshot any
		switch req.Source {
		case "traffic":
			for _, rec := range store.GetAll() {
				if rec.TraceID == req.SourceID {
					snapshot = rec
					break
				}
			}
		case "hit":
			for _, h := range mapRemoteHitStore.GetAll() {
				if h.ID == req.SourceID {
					cp := *h
					snapshot = &cp
					break
				}
			}
		default:
			http.Error(w, `{"error":"invalid source, must be traffic/hit"}`, http.StatusBadRequest)
			return
		}

		if snapshot == nil {
			http.Error(w, `{"error":"source record not found (may have been evicted)"}`, http.StatusNotFound)
			return
		}

		record := &MarkedRecord{
			ID:       fmt.Sprintf("mark-%d", time.Now().UnixNano()),
			Source:   req.Source,
			SourceID: req.SourceID,
			Note:     req.Note,
			MarkedAt: time.Now(),
			Data:     snapshot,
		}

		if !markedStore.Add(record) {
			http.Error(w, `{"error":"marked store is full"}`, http.StatusConflict)
			return
		}

		log.Printf("[Marked] 新增标记: %s (%s/%s)", record.ID, req.Source, req.SourceID)
		writeJSON(w, record)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"missing required parameter: id"}`, http.StatusBadRequest)
			return
		}
		if markedStore.Remove(id) {
			log.Printf("[Marked] 删除标记: %s", id)
			writeJSON(w, map[string]string{"status": "deleted", "id": id})
		} else {
			http.Error(w, `{"error":"marked record not found"}`, http.StatusNotFound)
		}

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// =====================================================================
// Web Server 启动与路由注册
// =====================================================================

// RegisterWebRoutes 将所有 Web Dashboard 路由注册到给定的 ServeMux
func RegisterWebRoutes(mux *http.ServeMux) {
	// REST API 端点
	mux.HandleFunc("/api/traffic", handleAPITraffic)
	mux.HandleFunc("/api/logs", handleAPILogs)
	mux.HandleFunc("/api/config", handleAPIConfig)
	mux.HandleFunc("/api/map-remote", handleAPIMapRemote)
	mux.HandleFunc("/api/map-remote/hits", handleAPIMapRemoteHits)
	mux.HandleFunc("/api/marked", handleAPIMarked)

	// 静态资源：将 embed.FS 的 web/ 子目录作为根路径提供
	subFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("[Web] 无法加载嵌入的前端资源: %v", err)
	}
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)
}

// =====================================================================
// 辅助函数
// =====================================================================

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, `{"error":"json encode failed"}`, http.StatusInternalServerError)
	}
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}
