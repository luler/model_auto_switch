package appconfig

import "gin_base/app/service/upstream"

// OpenAIProxyConfig OpenAI 代理配置
type OpenAIProxyConfig struct {
	// 对外提供的 API Keys（客户端使用这些 key 访问本服务）
	APIKeys []string `mapstructure:"api_keys" yaml:"api_keys"`

	// 管理后台登录密钥（独立于 api_keys，用于管理页面登录）
	AdminKey string `mapstructure:"admin_key" yaml:"admin_key"`

	// 上游供应商配置
	Providers []upstream.ProviderConfig `mapstructure:"providers" yaml:"providers"`

	// 请求重试配置
	MaxRetries int `mapstructure:"max_retries" yaml:"max_retries"` // 单次请求最大尝试次数（默认1，不重试；设置>1启用故障转移）

	// 供应商管理器配置
	MaxFailures       int `mapstructure:"max_failures" yaml:"max_failures"`               // 最大连续失败次数（超过后标记供应商不健康）
	RecoveryInterval  int `mapstructure:"recovery_interval" yaml:"recovery_interval"`     // 恢复间隔（秒）
	HealthCheckPeriod int `mapstructure:"health_check_period" yaml:"health_check_period"` // 健康检查周期（秒）
}
