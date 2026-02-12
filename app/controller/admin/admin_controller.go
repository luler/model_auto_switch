package admin

import (
	"bytes"
	"fmt"
	"gin_base/app/appconfig"
	"gin_base/app/helper/log_helper"
	"gin_base/app/helper/response_helper"
	"gin_base/app/service/upstream"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// AdminController 管理后台控制器
type AdminController struct {
	manager    *upstream.Manager
	apiKeys    []string
	adminKey   string
	configPath string
	maxRetries int
	mu         sync.RWMutex
}

// NewAdminController 创建管理控制器
func NewAdminController(manager *upstream.Manager, apiKeys []string, adminKey string, maxRetries int) *AdminController {
	if maxRetries <= 0 {
		maxRetries = 1
	}
	return &AdminController{
		manager:    manager,
		apiKeys:    apiKeys,
		adminKey:   adminKey,
		configPath: filepath.Join("app", "appconfig", "openai_proxy.yaml"),
		maxRetries: maxRetries,
	}
}

// SetManager 设置 Manager（用于热重载）
func (c *AdminController) SetManager(manager *upstream.Manager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.manager = manager
}

// GetManager 获取当前 Manager
func (c *AdminController) GetManager() *upstream.Manager {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.manager
}

// SetMaxRetries 设置最大重试次数（用于热重载）
func (c *AdminController) SetMaxRetries(maxRetries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxRetries <= 0 {
		maxRetries = 1
	}
	c.maxRetries = maxRetries
}

// GetMaxRetries 获取当前最大重试次数
func (c *AdminController) GetMaxRetries() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxRetries
}

// SetAPIKeys 设置 API Keys（用于热重载）
func (c *AdminController) SetAPIKeys(apiKeys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKeys = apiKeys
}

// GetAPIKeys 获取当前 API Keys
func (c *AdminController) GetAPIKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKeys
}

// SetAdminKey 设置管理密钥（用于热重载）
func (c *AdminController) SetAdminKey(adminKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adminKey = adminKey
}

// GetAdminKey 获取当前管理密钥
func (c *AdminController) GetAdminKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.adminKey
}

// LoginRequest 登录请求
type LoginRequest struct {
	APIKey string `json:"api_key"`
}

// Login 登录验证
func (c *AdminController) Login(ctx *gin.Context) {
	var req LoginRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response_helper.Fail(ctx, "参数错误")
		return
	}

	adminKey := c.GetAdminKey()
	if adminKey != "" && req.APIKey == adminKey {
		response_helper.Success(ctx, "登录成功")
		return
	}

	response_helper.Common(ctx, 401, "管理密钥无效")
}

// ValidateAPIKey 验证管理密钥（从 header 获取）
func (c *AdminController) ValidateAPIKey(apiKey string) bool {
	adminKey := c.GetAdminKey()
	return adminKey != "" && apiKey == adminKey
}

// GetHealth 获取健康状态
func (c *AdminController) GetHealth(ctx *gin.Context) {
	apiKey := ctx.GetHeader("X-API-Key")
	if !c.ValidateAPIKey(apiKey) {
		response_helper.Common(ctx, 401, "未授权")
		return
	}

	manager := c.GetManager()
	if manager == nil {
		response_helper.Fail(ctx, "服务未初始化")
		return
	}

	stats := manager.GetStats()
	response_helper.Success(ctx, "获取成功", stats)
}

// ConfigResponse 配置响应
type ConfigResponse struct {
	Providers         []upstream.ProviderConfig `json:"providers"`
	MaxRetries        int                       `json:"max_retries"`
	MaxFailures       int                       `json:"max_failures"`
	RecoveryInterval  int                       `json:"recovery_interval"`
	HealthCheckPeriod int                       `json:"health_check_period"`
}

// GetConfig 获取配置
func (c *AdminController) GetConfig(ctx *gin.Context) {
	apiKey := ctx.GetHeader("X-API-Key")
	if !c.ValidateAPIKey(apiKey) {
		response_helper.Common(ctx, 401, "未授权")
		return
	}

	// 读取配置文件
	config, err := c.loadConfig()
	if err != nil {
		response_helper.Fail(ctx, "读取配置失败: "+err.Error())
		return
	}

	response_helper.Success(ctx, "获取成功", ConfigResponse{
		Providers:         config.Providers,
		MaxRetries:        config.MaxRetries,
		MaxFailures:       config.MaxFailures,
		RecoveryInterval:  config.RecoveryInterval,
		HealthCheckPeriod: config.HealthCheckPeriod,
	})
}

// SaveConfigRequest 保存配置请求
type SaveConfigRequest struct {
	Providers         []upstream.ProviderConfig `json:"providers"`
	MaxRetries        *int                      `json:"max_retries,omitempty"`
	MaxFailures       *int                      `json:"max_failures,omitempty"`
	RecoveryInterval  *int                      `json:"recovery_interval,omitempty"`
	HealthCheckPeriod *int                      `json:"health_check_period,omitempty"`
}

// SaveConfig 保存配置
func (c *AdminController) SaveConfig(ctx *gin.Context) {
	apiKey := ctx.GetHeader("X-API-Key")
	if !c.ValidateAPIKey(apiKey) {
		response_helper.Common(ctx, 401, "未授权")
		return
	}

	var req SaveConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response_helper.Fail(ctx, "参数错误: "+err.Error())
		return
	}

	// 保存配置到文件（更新 providers 和全局配置）
	if err := c.saveConfig(&req); err != nil {
		response_helper.Fail(ctx, "保存配置失败: "+err.Error())
		return
	}

	// 读取完整配置用于热重载
	config, err := c.loadConfig()
	if err != nil {
		response_helper.Fail(ctx, "读取配置失败: "+err.Error())
		return
	}

	// 热重载 Manager
	if err := c.reloadManager(config); err != nil {
		response_helper.Fail(ctx, "重载配置失败: "+err.Error())
		return
	}

	log_helper.Info("配置已更新并重载")
	response_helper.Success(ctx, "保存成功")
}

// loadConfig 加载配置文件
func (c *AdminController) loadConfig() (*appconfig.OpenAIProxyConfig, error) {
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return nil, err
	}

	var config appconfig.OpenAIProxyConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// saveConfig 保存配置到文件（更新 providers 和全局配置，保留其他字段和注释）
func (c *AdminController) saveConfig(req *SaveConfigRequest) error {
	// 读取原文件内容
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return err
	}

	// 解析为 yaml.Node 以保留注释和格式
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	// root 是 Document 节点，其 Content[0] 是实际的 Map 节点
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("invalid yaml structure")
	}

	mapNode := root.Content[0]
	if mapNode.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node")
	}

	// 序列化新的 providers
	var providersNode yaml.Node
	if err := providersNode.Encode(req.Providers); err != nil {
		return err
	}

	// 更新或添加配置项的辅助函数
	updateOrAddField := func(key string, value int) {
		for i := 0; i < len(mapNode.Content)-1; i += 2 {
			if mapNode.Content[i].Value == key {
				mapNode.Content[i+1].Value = fmt.Sprintf("%d", value)
				return
			}
		}
		// 未找到则添加
		mapNode.Content = append(mapNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", value)},
		)
	}

	// 在 map 中找到 providers 键并替换值
	found := false
	for i := 0; i < len(mapNode.Content)-1; i += 2 {
		keyNode := mapNode.Content[i]
		if keyNode.Value == "providers" {
			// 替换 providers 的值节点
			mapNode.Content[i+1] = &providersNode
			found = true
			break
		}
	}

	// 如果没找到 providers 键，添加到末尾
	if !found {
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: "providers",
		}
		mapNode.Content = append(mapNode.Content, keyNode, &providersNode)
	}

	// 更新全局配置参数（如果提供了）
	if req.MaxRetries != nil {
		updateOrAddField("max_retries", *req.MaxRetries)
	}
	if req.MaxFailures != nil {
		updateOrAddField("max_failures", *req.MaxFailures)
	}
	if req.RecoveryInterval != nil {
		updateOrAddField("recovery_interval", *req.RecoveryInterval)
	}
	if req.HealthCheckPeriod != nil {
		updateOrAddField("health_check_period", *req.HealthCheckPeriod)
	}

	// 序列化回 yaml
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return err
	}
	encoder.Close()

	return os.WriteFile(c.configPath, buf.Bytes(), 0644)
}

// GetLogs 获取最新日志
func (c *AdminController) GetLogs(ctx *gin.Context) {
	apiKey := ctx.GetHeader("X-API-Key")
	if !c.ValidateAPIKey(apiKey) {
		response_helper.Common(ctx, 401, "未授权")
		return
	}

	logPath := filepath.Join("runtime", "logs", "app.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		response_helper.Fail(ctx, "读取日志失败: "+err.Error())
		return
	}

	// 获取最新200行
	lines := bytes.Split(data, []byte("\n"))
	start := 0
	if len(lines) > 200 {
		start = len(lines) - 200
	}
	recentLines := lines[start:]

	response_helper.Success(ctx, "获取成功", gin.H{
		"logs": string(bytes.Join(recentLines, []byte("\n"))),
	})
}

// ClearLogs 清空日志文件
func (c *AdminController) ClearLogs(ctx *gin.Context) {
	apiKey := ctx.GetHeader("X-API-Key")
	if !c.ValidateAPIKey(apiKey) {
		response_helper.Common(ctx, 401, "未授权")
		return
	}

	logPath := filepath.Join("runtime", "logs", "app.log")
	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		response_helper.Fail(ctx, "清空日志失败: "+err.Error())
		return
	}

	log_helper.Info("日志文件已清空")
	response_helper.Success(ctx, "清空成功")
}

// reloadManager 重载 Manager
func (c *AdminController) reloadManager(config *appconfig.OpenAIProxyConfig) error {
	oldManager := c.GetManager()

	// 保存旧的轮询计数器值
	var oldCounter uint64
	if oldManager != nil {
		oldCounter = oldManager.GetRoundRobinCounter()
	}

	// 创建新的管理器配置
	recoveryInterval := 30
	healthCheckPeriod := 60
	maxFailures := 3

	if config.RecoveryInterval > 0 {
		recoveryInterval = config.RecoveryInterval
	}
	if config.HealthCheckPeriod > 0 {
		healthCheckPeriod = config.HealthCheckPeriod
	}
	if config.MaxFailures > 0 {
		maxFailures = config.MaxFailures
	}

	mgrConfig := upstream.ManagerConfig{
		MaxFailures:       maxFailures,
		RecoveryInterval:  time.Duration(recoveryInterval) * time.Second,
		HealthCheckPeriod: time.Duration(healthCheckPeriod) * time.Second,
	}

	// 创建新的 Manager
	newManager := upstream.NewManager(config.Providers, mgrConfig)

	// 恢复轮询计数器值（保持负载均衡状态）
	if oldManager != nil {
		newManager.SetRoundRobinCounter(oldCounter)
	}

	// 更新 Manager
	c.SetManager(newManager)

	// 更新 MaxRetries
	if config.MaxRetries > 0 {
		c.SetMaxRetries(config.MaxRetries)
	}

	// 更新 AdminKey
	if config.AdminKey != "" {
		c.SetAdminKey(config.AdminKey)
	}

	// 停止旧的 Manager
	if oldManager != nil {
		oldManager.Stop()
	}

	return nil
}
