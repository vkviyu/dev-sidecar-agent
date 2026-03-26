package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/google/uuid"
)

// ---------------------------------------------------------
// 1. 数据结构：AI Agent 的"网络记忆库"
// ---------------------------------------------------------

// TimingDetail 精确时序分解（单位：毫秒）
type TimingDetail struct {
	DNSMs      int64 `json:"dns_ms"`
	ConnectMs  int64 `json:"connect_ms"`
	TLSMs      int64 `json:"tls_ms"`
	TTFBMs     int64 `json:"ttfb_ms"`
	TransferMs int64 `json:"transfer_ms"`
	TotalMs    int64 `json:"total_ms"`
}

// TLSDetail TLS 连接详情
type TLSDetail struct {
	Version     string `json:"version"`
	CipherSuite string `json:"cipher_suite"`
	ServerName  string `json:"server_name"`
	ALPN        string `json:"alpn"`
}

type TrafficRecord struct {
	TraceID        string              `json:"trace_id"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Host           string              `json:"host"`
	Proto          string              `json:"proto"`
	ReqHeaders     map[string]string   `json:"req_headers"`
	ReqHeadersRaw  map[string][]string `json:"req_headers_raw"`
	ReqBody        string              `json:"req_body"`
	ReqSize        int64               `json:"req_size"`
	RespStatus     int                 `json:"resp_status"`
	RespStatusText string              `json:"resp_status_text"`
	RespHeaders    map[string]string   `json:"resp_headers"`
	RespHeadersRaw map[string][]string `json:"resp_headers_raw"`
	RespBody       string              `json:"resp_body"`
	RespSize       int64               `json:"resp_size"`
	RemoteAddr     string              `json:"remote_addr"`
	DurationMs     int64               `json:"duration_ms"`
	Timing         *TimingDetail       `json:"timing"`
	TLS            *TLSDetail          `json:"tls,omitempty"`
	Timestamp      time.Time           `json:"timestamp"`
}

// 线程安全的全局流量存储
type TrafficStore struct {
	mu      sync.RWMutex
	records []*TrafficRecord
	limit   int
}

func NewTrafficStore(limit int) *TrafficStore {
	return &TrafficStore{
		records: make([]*TrafficRecord, 0),
		limit:   limit,
	}
}

func (s *TrafficStore) Add(record *TrafficRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	if len(s.records) > s.limit {
		s.records = s.records[1:] // 简单的 Ring Buffer
	}
}

func (s *TrafficStore) GetAll() []*TrafficRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records
}

// store 全局流量存储实例，由 main() 中根据配置初始化
var store *TrafficStore

// MapRemoteHit 记录一次 Map Remote 规则命中
type MapRemoteHit struct {
	ID         string    `json:"id"`
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Method     string    `json:"method"`
	OrigURL    string    `json:"orig_url"`
	RewriteURL string    `json:"rewrite_url"`
	TraceID    string    `json:"trace_id"`
	Timestamp  time.Time `json:"timestamp"`
}

// MapRemoteHitStore 线程安全的命中记录存储 (Ring Buffer)
type MapRemoteHitStore struct {
	mu      sync.RWMutex
	records []*MapRemoteHit
	limit   int
}

func NewMapRemoteHitStore(limit int) *MapRemoteHitStore {
	return &MapRemoteHitStore{
		records: make([]*MapRemoteHit, 0),
		limit:   limit,
	}
}

func (s *MapRemoteHitStore) Add(hit *MapRemoteHit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, hit)
	if len(s.records) > s.limit {
		s.records = s.records[1:]
	}
}

func (s *MapRemoteHitStore) GetAll() []*MapRemoteHit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records
}

// mapRemoteHitStore 全局 Map Remote 命中记录存储
var mapRemoteHitStore *MapRemoteHitStore

// MarkedRecord 用户标记/收藏的关键记录
type MarkedRecord struct {
	ID       string    `json:"id"`
	Source   string    `json:"source"` // "traffic" | "hit"
	SourceID string    `json:"source_id"`
	Note     string    `json:"note"`
	MarkedAt time.Time `json:"marked_at"`
	Data     any       `json:"data"`
}

// MarkedStore 线程安全的标记记录存储
type MarkedStore struct {
	mu      sync.RWMutex
	records []*MarkedRecord
	limit   int
}

func NewMarkedStore(limit int) *MarkedStore {
	return &MarkedStore{
		records: make([]*MarkedRecord, 0),
		limit:   limit,
	}
}

func (s *MarkedStore) Add(record *MarkedRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) >= s.limit {
		return false
	}
	s.records = append(s.records, record)
	return true
}

func (s *MarkedStore) GetAll() []*MarkedRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records
}

func (s *MarkedStore) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID == id {
			s.records = append(s.records[:i], s.records[i+1:]...)
			return true
		}
	}
	return false
}

// markedStore 全局标记记录存储
var markedStore *MarkedStore

// Location 描述一个流量匹配端点 (对标 Charles 的 Edit Map Remote 弹窗)
type Location struct {
	Protocol string `json:"protocol" yaml:"protocol"` // "http", "https", 或者 "*"
	Host     string `json:"host"     yaml:"host"`     // 支持确切域名或正则 (如 `.*\.production\.com`)
	Port     string `json:"port"     yaml:"port"`     // "80", "443", "8080", 或者 "*"
	Path     string `json:"path"     yaml:"path"`     // 路径前缀或正则 (如 `/api/v1/.*`)
	Query    string `json:"query"    yaml:"query"`    // Query String 正则匹配 (如 `page=.*`)，空或 "*" 表示不限
}

// MapRemoteRule 描述一条完整的映射规则
type MapRemoteRule struct {
	ID     string   `json:"id"     yaml:"id"`
	Enable bool     `json:"enable" yaml:"enable"`
	Name   string   `json:"name"   yaml:"name"`
	From   Location `json:"from"   yaml:"from"`
	To     Location `json:"to"     yaml:"to"`
}

// 线程安全的路由引擎
type MapRemoteEngine struct {
	mu          sync.RWMutex
	rules       []MapRemoteRule
	persistPath string // JSON 持久化文件路径，为空则不持久化
}

// MatchAndRewrite 尝试匹配并重写请求。如果命中规则返回 true。
func (e *MapRemoteEngine) MatchAndRewrite(r *http.Request, method, traceID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 解析当前请求的原始特征
	origProtocol := r.URL.Scheme
	if origProtocol == "" {
		origProtocol = "https" // 在 goproxy 的 Connect 劫持中，默认通常是 https
	}
	origHost := r.URL.Hostname()
	origPort := r.URL.Port()
	if origPort == "" {
		if origProtocol == "https" {
			origPort = "443"
		} else {
			origPort = "80"
		}
	}
	origPath := r.URL.Path
	origQuery := r.URL.RawQuery
	origURL := r.URL.String()

	for _, rule := range e.rules {
		if !rule.Enable {
			continue
		}

		// 1. 协议匹配
		if rule.From.Protocol != "*" && rule.From.Protocol != origProtocol {
			continue
		}
		// 2. 端口匹配
		if rule.From.Port != "*" && rule.From.Port != origPort {
			continue
		}
		// 3. Host 匹配 (使用正则)
		hostMatched, _ := regexp.MatchString("^"+rule.From.Host+"$", origHost)
		if rule.From.Host != "*" && !hostMatched {
			continue
		}
		// 4. Path 匹配 (支持正则或前缀匹配)
		pathMatched, _ := regexp.MatchString("^"+rule.From.Path, origPath)
		if rule.From.Path != "*" && !pathMatched {
			continue
		}
		// 5. Query 匹配 (支持正则)
		if rule.From.Query != "" && rule.From.Query != "*" {
			queryMatched, _ := regexp.MatchString(rule.From.Query, origQuery)
			if !queryMatched {
				continue
			}
		}

		// =========== 匹配成功！开始执行重写 (Rewrite) ===========

		log.Printf("[Map Remote 命中] 规则: %s | 原URL: %s", rule.Name, origURL)

		// 替换协议
		if rule.To.Protocol != "" && rule.To.Protocol != "*" {
			r.URL.Scheme = rule.To.Protocol
		}

		// 替换 Host 和 Port
		newHost := origHost
		if rule.To.Host != "" && rule.To.Host != "*" {
			newHost = rule.To.Host
		}
		newPort := origPort
		if rule.To.Port != "" && rule.To.Port != "*" {
			newPort = rule.To.Port
		}

		// 组合新的 Host
		if newPort == "80" || newPort == "443" {
			r.URL.Host = newHost
		} else {
			r.URL.Host = fmt.Sprintf("%s:%s", newHost, newPort)
		}
		r.Host = r.URL.Host // 必须同步修改 http 头的 Host 字段

		// 替换 Path (路径前缀替换)
		if rule.To.Path != "" && rule.To.Path != "*" {
			re := regexp.MustCompile("^" + rule.From.Path)
			r.URL.Path = re.ReplaceAllString(origPath, rule.To.Path)
		}

		// 替换 Query
		if rule.To.Query != "" && rule.To.Query != "*" {
			if rule.From.Query != "" && rule.From.Query != "*" {
				re := regexp.MustCompile(rule.From.Query)
				r.URL.RawQuery = re.ReplaceAllString(origQuery, rule.To.Query)
			} else {
				r.URL.RawQuery = rule.To.Query
			}
		}

		// 记录命中到 HitStore
		if mapRemoteHitStore != nil {
			mapRemoteHitStore.Add(&MapRemoteHit{
				ID:         fmt.Sprintf("hit-%d", time.Now().UnixNano()),
				RuleID:     rule.ID,
				RuleName:   rule.Name,
				Method:     method,
				OrigURL:    origURL,
				RewriteURL: r.URL.String(),
				TraceID:    traceID,
				Timestamp:  time.Now(),
			})
		}

		log.Printf("[Map Remote 转发] 目标URL -> %s", r.URL.String())
		return true // 命中一条规则即刻返回
	}

	return false
}

var routeEngine = &MapRemoteEngine{rules: make([]MapRemoteRule, 0)}

// SetPersistPath 设置持久化文件路径
func (e *MapRemoteEngine) SetPersistPath(path string) {
	e.persistPath = path
}

// persist 将当前规则写入 JSON 文件（调用时须已持有写锁）
func (e *MapRemoteEngine) persist() {
	if e.persistPath == "" {
		return
	}
	data, err := json.MarshalIndent(e.rules, "", "  ")
	if err != nil {
		log.Printf("[Map Remote] 持久化序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(e.persistPath, data, 0644); err != nil {
		log.Printf("[Map Remote] 持久化写入失败: %v", err)
	}
}

// UpdateRules 替换所有规则
func (e *MapRemoteEngine) UpdateRules(newRules []MapRemoteRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = newRules
}

// LoadMapRemoteRules 从 JSON 文件加载 Map Remote 规则
func LoadMapRemoteRules(path string) ([]MapRemoteRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rules []MapRemoteRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("map_remote.json 解析失败: %w", err)
	}
	return rules, nil
}

// GetRules 返回当前所有规则的拷贝
func (e *MapRemoteEngine) GetRules() []MapRemoteRule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]MapRemoteRule, len(e.rules))
	copy(result, e.rules)
	return result
}

// AddRule 新增一条规则
func (e *MapRemoteEngine) AddRule(rule MapRemoteRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, rule)
	e.persist()
}

// RemoveRule 根据 ID 删除一条规则
func (e *MapRemoteEngine) RemoveRule(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			e.persist()
			return true
		}
	}
	return false
}

// ToggleRule 根据 ID 切换规则的启用/禁用状态
func (e *MapRemoteEngine) ToggleRule(id string, enable bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == id {
			e.rules[i].Enable = enable
			e.persist()
			return true
		}
	}
	return false
}

// UpdateRule 根据 ID 整体替换一条规则的内容
func (e *MapRemoteEngine) UpdateRule(rule MapRemoteRule) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == rule.ID {
			e.rules[i] = rule
			e.persist()
			return true
		}
	}
	return false
}

// ---------------------------------------------------------
// 2. 核心逻辑：代理服务器与流量拦截
// ---------------------------------------------------------

// recordingBody 包装 resp.Body 实现流式转发+记录。
// goproxy 驱动 io.Copy 时，每次 Read 同时捕获数据副本到 buf；
// 流结束（EOF/Close）时通过 finalize 保存 TrafficRecord。
type recordingBody struct {
	original  io.ReadCloser
	buf       bytes.Buffer
	record    *TrafficRecord
	startTime time.Time
	gotFirst  *time.Time // 指向 httptrace GotFirstResponseByte 时间
	once      sync.Once
}

func (rb *recordingBody) Read(p []byte) (int, error) {
	n, err := rb.original.Read(p)
	if n > 0 {
		rb.buf.Write(p[:n])
	}
	if err != nil {
		rb.finalize()
	}
	return n, err
}

func (rb *recordingBody) Close() error {
	rb.finalize()
	return rb.original.Close()
}

func (rb *recordingBody) finalize() {
	rb.once.Do(func() {
		now := time.Now()
		rb.record.RespBody = rb.buf.String()
		rb.record.RespSize = int64(rb.buf.Len())
		rb.record.DurationMs = now.Sub(rb.startTime).Milliseconds()
		if rb.record.Timing != nil {
			rb.record.Timing.TotalMs = rb.record.DurationMs
			if rb.gotFirst != nil && !rb.gotFirst.IsZero() {
				rb.record.Timing.TransferMs = now.Sub(*rb.gotFirst).Milliseconds()
			}
		}
		store.Add(rb.record)
		log.Printf("[抓包落库] %s %s | 耗时: %dms | Status: %d",
			rb.record.Method, rb.record.URL, rb.record.DurationMs, rb.record.RespStatus)
	})
}

func startProxy() {
	if err := os.WriteFile("ca.crt", goproxy.CA_CERT, 0644); err != nil {
		log.Fatalf("无法导出 CA 证书: %v", err)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	// 支持直接请求：Service A 将请求地址设为代理地址（如 http://localhost:8888/api/users），
	// NonproxyHandler 补全 URL 后重新投递到代理流程，配合 MapRemote 转发到真实目标服务。
	proxy.NonproxyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Scheme = "http"
		r.URL.Host = r.Host
		proxy.ServeHTTP(w, r)
	})

	// 设置自定义 Transport：禁用 Keep-Alive，传播 httptrace context
	proxy.Tr = &http.Transport{
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{}).DialContext,
	}

	// --- 拦截请求：生成 TraceID，读取 Body，处理 Map Remote（零修改请求） ---
	proxy.OnRequest().DoFunc(func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// 生成 TraceID 并记录开始时间
		traceID := uuid.New().String()
		startTime := time.Now()

		// httptrace 时序采集变量
		var dnsStart, dnsEnd, connStart, connEnd, tlsStart, tlsEnd, gotFirstByte time.Time
		var remoteAddr string
		var tlsState *tls.ConnectionState

		trace := &httptrace.ClientTrace{
			DNSStart:     func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
			DNSDone:      func(_ httptrace.DNSDoneInfo) { dnsEnd = time.Now() },
			ConnectStart: func(_, _ string) { connStart = time.Now() },
			ConnectDone: func(_, addr string, _ error) {
				connEnd = time.Now()
				remoteAddr = addr
			},
			TLSHandshakeStart: func() { tlsStart = time.Now() },
			TLSHandshakeDone: func(state tls.ConnectionState, _ error) {
				tlsEnd = time.Now()
				tlsState = &state
			},
			GotFirstResponseByte: func() { gotFirstByte = time.Now() },
		}

		ctx.UserData = map[string]interface{}{
			"trace_id":       traceID,
			"start_time":     startTime,
			"remote_addr":    &remoteAddr,
			"tls_state":      &tlsState,
			"dns_start":      &dnsStart,
			"dns_end":        &dnsEnd,
			"conn_start":     &connStart,
			"conn_end":       &connEnd,
			"tls_start":      &tlsStart,
			"tls_end":        &tlsEnd,
			"got_first_byte": &gotFirstByte,
		}

		// 将 httptrace 注入 request context
		reqCtx := httptrace.WithClientTrace(r.Context(), trace)
		r = r.WithContext(reqCtx)

		// 读取 Request Body
		var reqBody []byte
		if r.Body != nil {
			reqBody, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(reqBody)) // 必须塞回去，否则后端收不到数据
		}

		// 创建 Record 并暂存到 ctx
		record := &TrafficRecord{
			TraceID:       traceID,
			Method:        r.Method,
			URL:           r.URL.String(),
			Host:          r.Host,
			Proto:         r.Proto,
			ReqHeaders:    flattenHeaders(r.Header),
			ReqHeadersRaw: cloneHeaders(r.Header),
			ReqBody:       string(reqBody),
			ReqSize:       int64(len(reqBody)),
			Timestamp:     time.Now(),
		}
		ctx.UserData.(map[string]any)["record"] = record

		// Map Remote: 流量劫持与路由重写
		routeEngine.MatchAndRewrite(r, r.Method, traceID)

		// 注意：不再注入 X-Dev-Trace-Id Header，请求零修改转发到后端

		return r, nil
	})

	// --- 拦截响应：提取头部信息，用 recordingBody 流式转发+记录 ---
	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil || ctx.UserData == nil {
			return resp
		}

		userData := ctx.UserData.(map[string]interface{})
		record := userData["record"].(*TrafficRecord)
		startTime := userData["start_time"].(time.Time)

		// ---- 立即设置：响应头相关字段（不依赖 Body） ----
		record.RespStatus = resp.StatusCode
		record.RespStatusText = resp.Status
		record.RespHeaders = flattenHeaders(resp.Header)
		record.RespHeadersRaw = cloneHeaders(resp.Header)

		if ra, ok := userData["remote_addr"].(*string); ok && *ra != "" {
			record.RemoteAddr = *ra
		}

		// 时序分解：DNS/Connect/TLS/TTFB 在 Body 传输前已确定
		timing := &TimingDetail{}
		if ds, ok := userData["dns_start"].(*time.Time); ok {
			de := userData["dns_end"].(*time.Time)
			if !ds.IsZero() && !de.IsZero() {
				timing.DNSMs = de.Sub(*ds).Milliseconds()
			}
		}
		if cs, ok := userData["conn_start"].(*time.Time); ok {
			ce := userData["conn_end"].(*time.Time)
			if !cs.IsZero() && !ce.IsZero() {
				timing.ConnectMs = ce.Sub(*cs).Milliseconds()
			}
		}
		if ts, ok := userData["tls_start"].(*time.Time); ok {
			te := userData["tls_end"].(*time.Time)
			if !ts.IsZero() && !te.IsZero() {
				timing.TLSMs = te.Sub(*ts).Milliseconds()
			}
		}
		if fb, ok := userData["got_first_byte"].(*time.Time); ok && !fb.IsZero() {
			timing.TTFBMs = fb.Sub(startTime).Milliseconds()
		}
		// TransferMs / TotalMs / DurationMs 延迟到 recordingBody.finalize 设置
		record.Timing = timing

		// TLS 详情：优先 httptrace 采集，回退到 resp.TLS
		var tlsCS *tls.ConnectionState
		if tlsPtr, ok := userData["tls_state"].(**tls.ConnectionState); ok && *tlsPtr != nil {
			tlsCS = *tlsPtr
		} else if resp.TLS != nil {
			tlsCS = resp.TLS
		}
		if tlsCS != nil {
			record.TLS = &TLSDetail{
				Version:     tlsVersionFromUint16(tlsCS.Version),
				CipherSuite: tls.CipherSuiteName(tlsCS.CipherSuite),
				ServerName:  tlsCS.ServerName,
				ALPN:        tlsCS.NegotiatedProtocol,
			}
		}

		// ---- 流式转发：用 recordingBody 包装 resp.Body ----
		// goproxy 的 io.Copy 驱动数据流过 wrapper，每次 Read 同时捕获副本；
		// 流结束时 finalize 保存完整 TrafficRecord。
		if resp.Body != nil {
			var gotFirst *time.Time
			if fb, ok := userData["got_first_byte"].(*time.Time); ok {
				gotFirst = fb
			}
			resp.Body = &recordingBody{
				original:  resp.Body,
				record:    record,
				startTime: startTime,
				gotFirst:  gotFirst,
			}
		} else {
			// Body 为 nil，直接落库
			record.DurationMs = time.Since(startTime).Milliseconds()
			timing.TotalMs = record.DurationMs
			store.Add(record)
			log.Printf("[抓包落库] %s %s | 耗时: %dms | Status: %d",
				record.Method, record.URL, record.DurationMs, record.RespStatus)
		}

		return resp
	})

	log.Printf("[MITM] 代理端口正在监听 :%s", appConfig.Proxy.Port)
	log.Fatal(http.ListenAndServe(":"+appConfig.Proxy.Port, proxy))
}

// 辅助函数：把 http.Header 转成简单的 map
func flattenHeaders(h http.Header) map[string]string {
	m := make(map[string]string)
	for k, v := range h {
		m[k] = strings.Join(v, ", ")
	}
	return m
}

func cloneHeaders(h http.Header) map[string][]string {
	m := make(map[string][]string, len(h))
	for k, v := range h {
		cp := make([]string, len(v))
		copy(cp, v)
		m[k] = cp
	}
	return m
}

func tlsVersionFromUint16(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func main() {
	// 命令行参数：仅保留 -config 指定配置文件路径
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置文件（不存在时使用全部默认值）
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}
	appConfig = cfg

	// 根据配置初始化全局存储
	store = NewTrafficStore(appConfig.Storage.TrafficLimit)
	appLogStore = NewLogStore(appConfig.Storage.LogLimit)
	mapRemoteHitStore = NewMapRemoteHitStore(appConfig.Storage.TrafficLimit)
	markedStore = NewMarkedStore(appConfig.Storage.MarkedLimit)

	// 加载 MapRemote 规则（独立 JSON 文件持久化）
	routeEngine.SetPersistPath("map_remote.json")
	if rules, err := LoadMapRemoteRules("map_remote.json"); err != nil {
		log.Printf("[Map Remote] 规则文件加载失败: %v，使用空规则集", err)
	} else if len(rules) > 0 {
		routeEngine.UpdateRules(rules)
		log.Printf("[Map Remote] 已加载 %d 条规则", len(rules))
	}

	// 用于通知所有子组件优雅退出的根 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. 启动 L7 MITM 代理服务器 (在新协程)
	go startProxy()

	// 3. 如果指定了目标程序，启动子进程接管
	var appProcess *AppProcess
	if appConfig.App.Command != "" {
		if appConfig.App.Debug {
			// Delve 调试模式：使用 DelveTracer 启动目标进程，提供运行时透视能力
			log.Printf("[Runtime Tracer] debug 模式已开启，将通过 Delve 调试目标进程")
			delveTracer = &DelveTracer{
				listenAddr: appConfig.Delve.ListenAddr,
			}
			if err := delveTracer.Start(appConfig.App.Command, flag.Args()); err != nil {
				log.Printf("[Runtime Tracer] Delve 启动失败: %v", err)
				log.Printf("[Runtime Tracer] 降级为普通进程模式...")
				delveTracer = nil
				appProcess = NewAppProcess(appConfig.App.Command)
				if err := appProcess.Start(ctx); err != nil {
					log.Fatalf("目标进程启动失败: %v", err)
				}
			}
		} else {
			// 普通模式：仅劫持 Stdout/Stderr 日志
			appProcess = NewAppProcess(appConfig.App.Command)
			if err := appProcess.Start(ctx); err != nil {
				log.Fatalf("目标进程启动失败: %v", err)
			}
		}
	}

	// 4. 监听操作系统终止信号，优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[Agent] 收到信号 %v，正在优雅退出...", sig)

		// 通知所有子组件停止
		cancel()

		// 确保子进程被清理
		if appProcess != nil {
			appProcess.Stop()
		}
		if delveTracer != nil {
			_ = delveTracer.Stop()
		}

		os.Exit(0)
	}()

	// 5. 启动 MCP 通信协议（Streamable HTTP），阻塞主线程
	StartMCPServer(appConfig.MCP.Port)
}
