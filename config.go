package main

import (
	"fmt"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// =====================================================================
// 全局配置管理
// =====================================================================

// Config 应用全局配置，对应 config.yaml
type Config struct {
	MCP     MCPConfig     `yaml:"mcp"`
	Proxy   ProxyConfig   `yaml:"proxy"`
	App     AppConfig     `yaml:"app"`
	Delve   DelveConfig   `yaml:"delve"`
	Storage StorageConfig `yaml:"storage"`
}

type MCPConfig struct {
	Port string `yaml:"port"` // HTTP 监听端口（MCP SSE + Web Dashboard 共用）
}

type ProxyConfig struct {
	Port string `yaml:"port"` // 代理监听端口
}

type StorageConfig struct {
	TrafficLimit int `yaml:"traffic_limit"` // L7 流量记录容量
	LogLimit     int `yaml:"log_limit"`     // 业务日志容量
	MarkedLimit  int `yaml:"marked_limit"`  // 标记记录容量
}

type AppConfig struct {
	Command string `yaml:"command"` // 目标程序命令
	Debug   bool   `yaml:"debug"`   // 是否启用 Delve 调试模式
}

type DelveConfig struct {
	ListenAddr string `yaml:"listen_addr"` // Delve RPC 监听地址
}

// DefaultConfig 返回填充了所有默认值的配置
func DefaultConfig() *Config {
	return &Config{
		MCP: MCPConfig{
			Port: "3000",
		},
		Proxy: ProxyConfig{
			Port: "8888",
		},
		App: AppConfig{
			Command: "",
			Debug:   false,
		},
		Delve: DelveConfig{
			ListenAddr: "127.0.0.1:2345",
		},
		Storage: StorageConfig{
			TrafficLimit: 1000,
			LogLimit:     10000,
			MarkedLimit:  500,
		},
	}
}

// LoadConfig 从指定路径加载 YAML 配置文件。
// 文件不存在时使用默认值并打印提示；格式错误时返回解析错误。
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Config] 配置文件 %s 不存在，使用全部默认值", path)
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件 %s 解析失败: %w", path, err)
	}

	log.Printf("[Config] 已加载配置文件: %s", path)
	return cfg, nil
}

// 全局配置实例，由 main() 初始化，供各模块读取
var appConfig *Config
