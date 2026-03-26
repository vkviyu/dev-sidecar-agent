# Dev-Sidecar Agent - 工具能力详解

Dev-Sidecar Agent 是一个 MCP (Model Context Protocol) 服务，为 AI 编程助手提供运行时可观测性和调试能力。它通过 Streamable HTTP 传输协议对外暴露 **18 个 MCP 工具**，覆盖网络流量分析、进程调试、日志读取、流量改写四大领域。

MCP 端点：`http://localhost:{port}/mcp`（默认端口 3000）

---

## 一、网络流量观测（2 个工具）

### 1. `get_http_traffic`

**用途**：获取 MITM 代理捕获的 L7 HTTP 请求/响应记录。

代理以中间人模式拦截所有经过的 HTTP/HTTPS 流量，记录完整的请求方法、URL、Headers、Body、响应状态码、响应 Body、耗时等。每条记录自动生成 `Trace-ID`（不修改请求头，对后端零侵入），可通过 Trace-ID 实现 HTTP 请求与应用日志的关联。

| 参数       | 类型   | 必填 | 说明                      |
| ---------- | ------ | ---- | ------------------------- |
| `limit`    | number | 否   | 返回记录数量上限，默认 50 |
| `trace_id` | string | 否   | 按 Trace-ID 精确过滤      |

**返回**：JSON 数组，每条记录包含 `trace_id`、`method`、`url`、`req_headers`、`req_body`、`resp_status`、`resp_headers`、`resp_body`、`duration_ms`、`timestamp`。

**典型场景**：

- AI 助手排查 API 调用失败时，查看完整请求/响应内容
- 结合 `trace_id` 关联应用日志定位问题

---

### 2. `get_map_remote_hits`

**用途**：获取 Map Remote 规则的命中记录。

每当一个 HTTP 请求匹配到 Map Remote 规则并被重写时，会记录一条命中日志，包含原始 URL 和重写后的 URL。

| 参数      | 类型   | 必填 | 说明                      |
| --------- | ------ | ---- | ------------------------- |
| `limit`   | number | 否   | 返回记录数量上限，默认 50 |
| `rule_id` | string | 否   | 按规则 ID 过滤            |

**返回**：JSON 数组，每条包含 `id`、`rule_id`、`rule_name`、`method`、`orig_url`、`rewrite_url`、`trace_id`、`timestamp`。

---

## 二、Map Remote 流量改写（5 个工具）

Map Remote 功能对标 Charles Proxy，允许在运行时动态配置 URL 重写规则，将匹配条件的请求透明转发到不同的目标地址。规则持久化存储在 `map_remote.json` 文件中。

### 3. `list_map_remote`

**用途**：列出当前所有 Map Remote 规则及其启用状态。

| 参数 | 无  |
| ---- | --- |

**返回**：JSON 数组，每条规则包含 `id`、`name`、`enable`、`from`（protocol/host/port/path）、`to`（protocol/host/port/path）。

---

### 4. `add_map_remote`

**用途**：新增一条 Map Remote 规则。

| 参数            | 类型   | 必填   | 说明                                              |
| --------------- | ------ | ------ | ------------------------------------------------- |
| `name`          | string | **是** | 规则名称                                          |
| `from_host`     | string | **是** | 匹配的 Host（支持正则，如 `.*\.production\.com`） |
| `to_host`       | string | **是** | 目标 Host                                         |
| `from_protocol` | string | 否     | 匹配的协议：`http`/`https`/`*`，默认 `*`          |
| `from_port`     | string | 否     | 匹配的端口，默认 `*`                              |
| `from_path`     | string | 否     | 匹配的路径（支持正则），默认 `.*`                 |
| `to_protocol`   | string | 否     | 目标协议，默认 `http`                             |
| `to_port`       | string | 否     | 目标端口，默认 `*`（保持原端口）                  |
| `to_path`       | string | 否     | 目标路径，默认 `*`（保持原路径）                  |

**典型场景**：

- 将生产环境 API 请求重定向到本地开发服务器
- 将第三方服务请求指向 Mock 服务

---

### 5. `update_map_remote`

**用途**：修改一条已有的 Map Remote 规则。未提供的可选字段保持原值不变。

| 参数                                                      | 类型    | 必填   | 说明            |
| --------------------------------------------------------- | ------- | ------ | --------------- |
| `id`                                                      | string  | **是** | 要修改的规则 ID |
| `name`                                                    | string  | 否     | 规则名称        |
| `from_protocol` / `from_host` / `from_port` / `from_path` | string  | 否     | 匹配条件        |
| `to_protocol` / `to_host` / `to_port` / `to_path`         | string  | 否     | 目标地址        |
| `enable`                                                  | boolean | 否     | 是否启用        |

---

### 6. `toggle_map_remote`

**用途**：启用或禁用指定规则。

| 参数     | 类型    | 必填   | 说明                      |
| -------- | ------- | ------ | ------------------------- |
| `id`     | string  | **是** | 规则 ID                   |
| `enable` | boolean | **是** | `true`=启用，`false`=禁用 |

---

### 7. `remove_map_remote`

**用途**：删除一条 Map Remote 规则。

| 参数 | 类型   | 必填   | 说明    |
| ---- | ------ | ------ | ------- |
| `id` | string | **是** | 规则 ID |

---

## 三、日志与进程输出（2 个工具）

### 8. `get_app_stdout`

**用途**：获取被接管子进程的 Stdout/Stderr 输出。

当 `config.yaml` 中配置了 `app.command` 时，Agent 会启动并接管该子进程，捕获其所有标准输出和标准错误输出。每行日志自动提取 Trace-ID（通过正则匹配 `trace_id=xxx`、`x-dev-trace-id:xxx` 等模式），实现日志与 HTTP 请求的关联。

| 参数         | 类型   | 必填 | 说明                          |
| ------------ | ------ | ---- | ----------------------------- |
| `trace_id`   | string | 否   | 按 Trace-ID 精确过滤          |
| `start_time` | string | 否   | 时间窗口起始（ISO 8601 格式） |
| `end_time`   | string | 否   | 时间窗口结束（ISO 8601 格式） |
| `limit`      | number | 否   | 返回日志条数上限，默认 200    |

**返回**：JSON 数组，每条包含 `timestamp`、`level`（`INFO`=stdout / `ERROR`=stderr）、`trace_id`、`message`。

**典型场景**：

- 请求返回 500 时，用同一个 `trace_id` 查看对应的应用日志和错误栈
- 按时间窗口查看某次请求前后的应用输出

---

### 9. `read_log_files`

**用途**：按需读取磁盘日志文件的最后 N 行。

与 `get_app_stdout`（内存中的实时输出）不同，此工具直接读取磁盘上的日志文件，支持 glob 模式匹配多个文件，使用高效的从文件末尾反向扫描算法。

| 参数      | 类型   | 必填   | 说明                                                                            |
| --------- | ------ | ------ | ------------------------------------------------------------------------------- |
| `paths`   | string | **是** | 日志文件路径，支持 glob，多路径逗号分隔。如 `/tmp/app.log,/var/log/myapp/*.log` |
| `tail`    | number | 否     | 每个文件返回最后 N 行，默认 100                                                 |
| `pattern` | string | 否     | 正则表达式过滤，只返回匹配的行                                                  |

**返回**：JSON 数组，每项包含 `file`（文件路径）、`lines`（行内容数组）、`error`（如有）。

**典型场景**：

- 读取 Nginx/应用部署日志的最近错误
- 用正则过滤特定关键词（如 `pattern: "ERROR|FATAL"`）

---

## 四、Delve 运行时调试（7 个工具）

这些工具通过 Delve 调试器的 RPC API 提供非侵入式的运行时观测能力。支持两种启动模式：

- **`-debug` 模式**：Agent 以 `dlv exec` 方式启动子进程，自动建立调试连接
- **`attach` 模式**：通过 `attach_process` 工具动态附加到一个已运行的外部进程

### 10. `attach_process`

**用途**：动态附加 Delve 调试器到一个已运行的外部进程。

附加后，所有调试工具（`list_goroutines`、`get_stack_trace`、`eval_variable`、`set_tracepoint`）立即可用。适用于业务程序通过 `go run` 或独立启动的场景。如果已有附加连接，会先断开再重新附加。

| 参数  | 类型   | 必填   | 说明           |
| ----- | ------ | ------ | -------------- |
| `pid` | number | **是** | 目标进程的 PID |

---

### 11. `detach_process`

**用途**：断开当前附加的 Delve 调试器。目标进程不会被终止，继续正常运行。

| 参数 | 无  |
| ---- | --- |

---

### 12. `list_goroutines`

**用途**：列出目标进程所有 Goroutine 及其当前状态和代码位置。

**返回**：JSON 数组，每项包含 `id`（Goroutine ID）、`status`（运行状态）、`current_location`（当前执行位置：函数名、文件、行号）、`start_location`（启动位置）、`thread_id`。

**典型场景**：

- 排查 Goroutine 泄漏：查看是否有大量 Goroutine 阻塞在同一位置
- 定位死锁：找到相互等待的 Goroutine

---

### 13. `get_stack_trace`

**用途**：获取指定 Goroutine 的完整函数调用栈。

| 参数           | 类型   | 必填   | 说明                                      |
| -------------- | ------ | ------ | ----------------------------------------- |
| `goroutine_id` | number | **是** | Goroutine ID（从 `list_goroutines` 获取） |
| `depth`        | number | 否     | 栈帧深度限制，默认 20                     |

**返回**：JSON 数组，每帧包含 `function`、`file`、`line`、`args`（函数参数）、`locals`（局部变量）。

---

### 14. `eval_variable`

**用途**：在指定 Goroutine 的指定栈帧中求值 Go 表达式，获取变量的运行时值。

| 参数           | 类型   | 必填   | 说明                                        |
| -------------- | ------ | ------ | ------------------------------------------- |
| `goroutine_id` | number | **是** | Goroutine ID                                |
| `frame`        | number | 否     | 栈帧索引，默认 0（当前帧）                  |
| `expr`         | string | **是** | Go 表达式（变量名、结构体字段、切片索引等） |

**返回**：`{ "name": "...", "type": "...", "value": "..." }`

**典型场景**：

- 查看某个请求处理 Goroutine 中的 `ctx` 值
- 检查缓存 map 的当前大小：`len(cache)`

---

### 15. `set_tracepoint`

**用途**：在指定源码位置设置动态追踪点。

与传统断点不同，追踪点**不阻塞执行**。当代码执行到追踪点时，自动捕获当前的 Goroutine 信息、调用栈（10 层深度）和函数参数/局部变量，然后继续运行。

| 参数   | 类型   | 必填   | 说明           |
| ------ | ------ | ------ | -------------- |
| `file` | string | **是** | 源代码文件路径 |
| `line` | number | **是** | 行号           |

**返回**：创建的追踪点信息，包含 `breakpoint_id`。

---

### 16. `clear_tracepoint`

**用途**：清除指定 ID 的追踪点。

| 参数            | 类型   | 必填   | 说明                                        |
| --------------- | ------ | ------ | ------------------------------------------- |
| `breakpoint_id` | number | **是** | 追踪点 ID（从 `set_tracepoint` 返回值获取） |

---

## 五、标记记录管理（3 个工具）

标记（Marked）功能允许用户将调试过程中发现的关键记录收藏起来，跨 Traffic/Map Remote Hits 统一管理。标记的记录以深拷贝快照方式存储，即使原始记录被环形缓冲区淘汰也不会丢失。

### 17. `get_marked_records`

**用途**：获取用户标记/收藏的关键记录。

| 参数     | 类型   | 必填 | 说明                            |
| -------- | ------ | ---- | ------------------------------- |
| `source` | string | 否   | 按来源类型过滤：`traffic`/`hit` |

**返回**：JSON 数组，每条包含 `id`、`source`、`source_id`、`note`、`marked_at`、`data`（原始记录快照）。

**典型场景**：

- AI 助手获取用户手动标记的"重要线索"，结合上下文分析问题

---

### 18. `add_marked_record`

**用途**：标记/收藏一条记录。

| 参数        | 类型   | 必填   | 说明                                              |
| ----------- | ------ | ------ | ------------------------------------------------- |
| `source`    | string | **是** | 记录来源：`traffic`/`hit`                         |
| `source_id` | string | **是** | 原始记录 ID（traffic 用 trace_id，hit 用 hit id） |
| `note`      | string | 否     | 备注说明                                          |

---

### 19. `remove_marked_record`

**用途**：移除一条标记记录。

| 参数 | 类型   | 必填   | 说明          |
| ---- | ------ | ------ | ------------- |
| `id` | string | **是** | 标记记录的 ID |

---

## 六、底层引擎能力（非 MCP 工具，但支撑上层能力）

以下能力不直接暴露为 MCP 工具，但作为基础设施支撑上述工具运行：

### MITM 代理引擎

基于 `goproxy` 的全量 HTTPS 中间人代理，自动生成 CA 证书信任链。每个请求生成唯一 Trace-ID 但**不修改请求头**（零侵入）。通过 `net/http/httptrace` 注入精确的时序测量（DNS/Connect/TLS/TTFB/Transfer），并从 `crypto/tls.ConnectionState` 提取 TLS 版本、CipherSuite、SNI、ALPN 等元数据。Trace-ID 可用于关联 HTTP 请求与应用日志。

### Map Remote 引擎

运行时 URL 重写引擎，规则支持正则匹配，持久化到 `map_remote.json`。在代理请求处理流程中，每个请求经过 Map Remote 引擎检查，匹配则透明重写目标地址并记录命中日志。

### 子进程管理

当配置了 `app.command` 时，Agent 启动并接管子进程的 stdin/stdout/stderr。stdout 标记为 `INFO` 级别，stderr 标记为 `ERROR` 级别。每行输出通过正则自动提取 Trace-ID（匹配 `trace_id=xxx`、`trace[-_]?id[:=]xxx` 等模式）。支持 Delve debug 模式（`-debug` 标志），以 `dlv exec` 方式启动子进程实现零配置调试。

---

## 七、配置参考

配置文件：`config.yaml`（通过 `-config` 标志指定路径）

```yaml
mcp:
  port: "3000" # MCP + Web Dashboard 端口

proxy:
  port: "8888" # MITM 代理端口

app:
  command: "" # 子进程启动命令（留空则不启动）
  debug: false # 是否启用 Delve 调试模式

delve:
  listen_addr: "127.0.0.1:2345" # Delve RPC 监听地址

storage:
  traffic_limit: 1000 # L7 流量记录环形缓冲区容量
  log_limit: 10000 # 日志记录环形缓冲区容量
  marked_limit: 500 # 标记记录容量上限（满后拒绝新增）
```

---

## 八、Web Dashboard

除 MCP 工具外，Agent 同时提供 Web Dashboard（`http://localhost:{port}/`），包含以下页面：

| Tab              | 功能                                                                |
| ---------------- | ------------------------------------------------------------------- |
| **Traffic (L7)** | HTTP 请求/响应列表，点击查看完整 Headers 和 Body                    |
| **Logs**         | 子进程 stdout/stderr 实时输出                                       |
| **Map Remote**   | 规则管理（增删改查）+ 命中记录；点击命中记录查看关联的 Traffic 详情 |
| **Marked**       | 跨 tab 收藏的记录统一查看；支持右键标记任意记录                     |

Dashboard 支持自动刷新（3 秒间隔），打开详情面板时自动暂停刷新，关闭后恢复。
