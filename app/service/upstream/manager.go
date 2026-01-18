package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// ModelMapping 模型映射配置
type ModelMapping struct {
	Alias    string `json:"alias" mapstructure:"alias"`       // 对外暴露的别名（可选，不填则等于 upstream）
	Upstream string `json:"upstream" mapstructure:"upstream"` // 上游实际模型名（必填）
	Priority int    `json:"priority" mapstructure:"priority"` // 优先级（数值越小优先级越高，默认0）
	Weight   int    `json:"weight" mapstructure:"weight"`     // 负载均衡权重（默认1）
}

// ProviderConfig 上游供应商配置
type ProviderConfig struct {
	Name          string         `json:"name" mapstructure:"name"`                     // 供应商名称
	BaseURL       string         `json:"base_url" mapstructure:"base_url"`             // 基础URL
	APIKey        string         `json:"api_key" mapstructure:"api_key"`               // API Key
	Weight        int            `json:"weight" mapstructure:"weight"`                 // 负载均衡权重（默认1）
	Priority      int            `json:"priority" mapstructure:"priority"`             // 优先级（数值越小优先级越高，默认0）
	Timeout       int            `json:"timeout" mapstructure:"timeout"`               // 超时时间（秒）
	ModelMappings []ModelMapping `json:"model_mappings" mapstructure:"model_mappings"` // 模型映射
	ExcludeParams []string       `json:"exclude_params" mapstructure:"exclude_params"` // 要过滤的参数列表
}

// ProviderModel 供应商+模型组合（用于路由）
type ProviderModel struct {
	Provider *Provider
	Mapping  ModelMapping
}

// Provider 上游供应商
type Provider struct {
	Config         ProviderConfig
	healthy        atomic.Bool         // 是否健康
	failureCount   atomic.Int32        // 连续失败次数
	lastFailure    atomic.Int64        // 上次失败时间戳
	totalReqs      atomic.Int64        // 总请求数
	successReqs    atomic.Int64        // 成功请求数
	mu             sync.RWMutex        // 保护其他字段
	httpClient     *http.Client        // HTTP 客户端
	modelIndex     map[string][]int    // alias -> ModelMappings 索引
}

// ProviderStats 供应商统计信息
type ProviderStats struct {
	Name         string  `json:"name"`
	Healthy      bool    `json:"healthy"`
	FailureCount int32   `json:"failure_count"`
	TotalReqs    int64   `json:"total_requests"`
	SuccessReqs  int64   `json:"success_requests"`
	SuccessRate  float64 `json:"success_rate"`
}

// Manager 供应商管理器
type Manager struct {
	providers         []*Provider
	mu                sync.RWMutex
	roundRobinCounter atomic.Uint64

	// 配置
	maxFailures       int           // 最大连续失败次数，超过后标记为不健康
	recoveryInterval  time.Duration // 恢复检查间隔
	healthCheckPeriod time.Duration // 健康检查周期

	// 停止信号
	stopChan chan struct{}
}

// ManagerConfig 管理器配置
type ManagerConfig struct {
	MaxFailures       int           // 最大连续失败次数
	RecoveryInterval  time.Duration // 恢复间隔
	HealthCheckPeriod time.Duration // 健康检查周期
}

// DefaultManagerConfig 默认管理器配置
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxFailures:       3,
		RecoveryInterval:  30 * time.Second,
		HealthCheckPeriod: 60 * time.Second,
	}
}

// NewManager 创建供应商管理器
func NewManager(configs []ProviderConfig, mgrConfig ManagerConfig) *Manager {
	m := &Manager{
		providers:         make([]*Provider, 0, len(configs)),
		maxFailures:       mgrConfig.MaxFailures,
		recoveryInterval:  mgrConfig.RecoveryInterval,
		healthCheckPeriod: mgrConfig.HealthCheckPeriod,
		stopChan:          make(chan struct{}),
	}

	for _, cfg := range configs {
		if cfg.Timeout <= 0 {
			cfg.Timeout = 60
		}
		// 处理 Provider 级别的默认值
		if cfg.Weight <= 0 {
			cfg.Weight = 1
		}

		// 处理 ModelMappings 默认值
		for i := range cfg.ModelMappings {
			if cfg.ModelMappings[i].Alias == "" {
				cfg.ModelMappings[i].Alias = cfg.ModelMappings[i].Upstream
			}
			if cfg.ModelMappings[i].Weight <= 0 {
				cfg.ModelMappings[i].Weight = 1
			}
		}

		p := &Provider{
			Config: cfg,
			httpClient: &http.Client{
				Timeout: time.Duration(cfg.Timeout) * time.Second,
				Transport: &http.Transport{
					MaxIdleConns:        100,
					MaxIdleConnsPerHost: 20,
					IdleConnTimeout:     90 * time.Second,
				},
			},
			modelIndex: make(map[string][]int),
		}

		// 构建模型索引
		for i, mm := range cfg.ModelMappings {
			p.modelIndex[mm.Alias] = append(p.modelIndex[mm.Alias], i)
		}

		p.healthy.Store(true)
		m.providers = append(m.providers, p)
	}

	// 启动后台健康检查
	go m.startHealthCheck()

	return m
}

// GetUpstreamModel 获取上游模型名（根据别名和映射索引）
func (p *Provider) GetUpstreamModel(alias string, mappingIdx int) string {
	if mappingIdx >= 0 && mappingIdx < len(p.Config.ModelMappings) {
		return p.Config.ModelMappings[mappingIdx].Upstream
	}
	return alias
}

// GetModelMapping 获取模型映射
func (p *Provider) GetModelMapping(alias string, mappingIdx int) *ModelMapping {
	if mappingIdx >= 0 && mappingIdx < len(p.Config.ModelMappings) {
		return &p.Config.ModelMappings[mappingIdx]
	}
	return nil
}

// GetProviderModels 获取所有支持指定别名的 ProviderModel 组合（按优先级和权重排序）
// 综合优先级 = Provider.Priority + Model.Priority
// 综合权重 = Provider.Weight * Model.Weight
func (m *Manager) GetProviderModels(alias string) []ProviderModel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var candidates []ProviderModel

	// 收集所有支持该别名的 ProviderModel
	for _, p := range m.providers {
		if !p.healthy.Load() {
			continue
		}
		if indices, ok := p.modelIndex[alias]; ok {
			for _, idx := range indices {
				candidates = append(candidates, ProviderModel{
					Provider: p,
					Mapping:  p.Config.ModelMappings[idx],
				})
			}
		}
	}

	// 如果没有健康的，使用所有的
	if len(candidates) == 0 {
		for _, p := range m.providers {
			if indices, ok := p.modelIndex[alias]; ok {
				for _, idx := range indices {
					candidates = append(candidates, ProviderModel{
						Provider: p,
						Mapping:  p.Config.ModelMappings[idx],
					})
				}
			}
		}
	}

	// 按综合优先级分组，然后在同优先级内按综合权重排序
	if len(candidates) <= 1 {
		return candidates
	}

	// 计算综合优先级并分组
	prioritySet := make(map[int][]ProviderModel)
	for _, pm := range candidates {
		// 综合优先级 = Provider.Priority + Model.Priority
		combinedPriority := pm.Provider.Config.Priority + pm.Mapping.Priority
		prioritySet[combinedPriority] = append(prioritySet[combinedPriority], pm)
	}

	// 获取排序后的优先级列表
	priorities := make([]int, 0, len(prioritySet))
	for p := range prioritySet {
		priorities = append(priorities, p)
	}
	// 排序优先级（数值小的在前）
	for i := 0; i < len(priorities)-1; i++ {
		for j := i + 1; j < len(priorities); j++ {
			if priorities[i] > priorities[j] {
				priorities[i], priorities[j] = priorities[j], priorities[i]
			}
		}
	}

	// 按优先级顺序组装结果，同优先级按综合权重排序（权重大的在前）
	var result []ProviderModel
	for _, priority := range priorities {
		group := prioritySet[priority]
		// 同优先级内按综合权重排序（权重大的在前）
		for i := 0; i < len(group)-1; i++ {
			for j := i + 1; j < len(group); j++ {
				// 综合权重 = Provider.Weight * Model.Weight
				weightI := group[i].Provider.Config.Weight * group[i].Mapping.Weight
				weightJ := group[j].Provider.Config.Weight * group[j].Mapping.Weight
				if weightI < weightJ {
					group[i], group[j] = group[j], group[i]
				}
			}
		}
		result = append(result, group...)
	}

	return result
}

// GetCombinedPriority 获取 ProviderModel 的综合优先级
func (pm *ProviderModel) GetCombinedPriority() int {
	return pm.Provider.Config.Priority + pm.Mapping.Priority
}

// GetCombinedWeight 获取 ProviderModel 的综合权重
func (pm *ProviderModel) GetCombinedWeight() int {
	return pm.Provider.Config.Weight * pm.Mapping.Weight
}

// SelectProviderModel 从同优先级组中按权重选择一个（用于负载均衡）
func (m *Manager) SelectProviderModel(alias string) *ProviderModel {
	candidates := m.GetProviderModels(alias)
	if len(candidates) == 0 {
		return nil
	}

	// 找出最高（综合）优先级
	minPriority := candidates[0].GetCombinedPriority()

	// 筛选出最高优先级的
	var topPriority []ProviderModel
	for _, pm := range candidates {
		if pm.GetCombinedPriority() == minPriority {
			topPriority = append(topPriority, pm)
		}
	}

	if len(topPriority) == 1 {
		return &topPriority[0]
	}

	// 加权轮询（使用综合权重）
	totalWeight := 0
	for _, pm := range topPriority {
		totalWeight += pm.GetCombinedWeight()
	}

	counter := m.roundRobinCounter.Add(1)
	targetWeight := int(counter % uint64(totalWeight))

	currentWeight := 0
	for i := range topPriority {
		currentWeight += topPriority[i].GetCombinedWeight()
		if targetWeight < currentWeight {
			return &topPriority[i]
		}
	}

	return &topPriority[0]
}

// supportsModel 检查供应商是否支持模型
func (m *Manager) supportsModel(p *Provider, model string) bool {
	if _, ok := p.modelIndex[model]; ok {
		return true
	}
	if len(p.Config.ModelMappings) == 0 {
		return true
	}
	return false
}

// RecordSuccess 记录成功请求
func (m *Manager) RecordSuccess(p *Provider) {
	p.totalReqs.Add(1)
	p.successReqs.Add(1)
	p.failureCount.Store(0)
	if !p.healthy.Load() {
		p.healthy.Store(true)
		logrus.Infof("Provider %s recovered and marked as healthy", p.Config.Name)
	}
}

// RecordFailure 记录失败请求
func (m *Manager) RecordFailure(p *Provider) {
	p.totalReqs.Add(1)
	failures := p.failureCount.Add(1)
	p.lastFailure.Store(time.Now().Unix())

	if int(failures) >= m.maxFailures && p.healthy.Load() {
		p.healthy.Store(false)
		logrus.Warnf("Provider %s marked as unhealthy after %d consecutive failures", p.Config.Name, failures)
	}
}

// startHealthCheck 启动健康检查
func (m *Manager) startHealthCheck() {
	ticker := time.NewTicker(m.healthCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.checkAndRecover()
		}
	}
}

// checkAndRecover 检查并恢复不健康的供应商
func (m *Manager) checkAndRecover() {
	m.mu.RLock()
	providers := m.providers
	m.mu.RUnlock()

	now := time.Now().Unix()
	for _, p := range providers {
		if !p.healthy.Load() {
			lastFailure := p.lastFailure.Load()
			if now-lastFailure > int64(m.recoveryInterval.Seconds()) {
				// 尝试恢复
				go m.tryRecover(p)
			}
		}
	}
}

// tryRecover 尝试恢复供应商
func (m *Manager) tryRecover(p *Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 发送简单的健康检查请求
	req, err := http.NewRequestWithContext(ctx, "GET", p.Config.BaseURL+"/v1/models", nil)
	if err != nil {
		logrus.Warnf("Failed to create health check request for %s: %v", p.Config.Name, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.Config.APIKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		logrus.Warnf("Health check failed for %s: %v", p.Config.Name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		p.failureCount.Store(0)
		p.healthy.Store(true)
		logrus.Infof("Provider %s recovered after health check", p.Config.Name)
	}
}

// Stop 停止管理器
func (m *Manager) Stop() {
	close(m.stopChan)
}

// GetStats 获取所有供应商统计信息
func (m *Manager) GetStats() []ProviderStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]ProviderStats, 0, len(m.providers))
	for _, p := range m.providers {
		total := p.totalReqs.Load()
		success := p.successReqs.Load()
		var rate float64
		if total > 0 {
			rate = float64(success) / float64(total) * 100
		}
		stats = append(stats, ProviderStats{
			Name:         p.Config.Name,
			Healthy:      p.healthy.Load(),
			FailureCount: p.failureCount.Load(),
			TotalReqs:    total,
			SuccessReqs:  success,
			SuccessRate:  rate,
		})
	}
	return stats
}

// GetAllModels 获取所有可用模型（返回别名）
func (m *Manager) GetAllModels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	modelSet := make(map[string]struct{})
	for _, p := range m.providers {
		// 添加别名
		for alias := range p.modelIndex {
			modelSet[alias] = struct{}{}
		}
	}

	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	return models
}

// ProxyRequest 代理请求到上游
func (p *Provider) ProxyRequest(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	url := p.Config.BaseURL + path

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// 设置请求头
	req.Header.Set("Authorization", "Bearer "+p.Config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if k != "Authorization" && k != "Host" {
			req.Header.Set(k, v)
		}
	}

	return p.httpClient.Do(req)
}

// ProxyStreamRequest 代理流式请求
func (p *Provider) ProxyStreamRequest(ctx context.Context, path string, body []byte, headers map[string]string) (*http.Response, error) {
	url := p.Config.BaseURL + path

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create stream request failed: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.Config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		if k != "Authorization" && k != "Host" && k != "Accept" {
			req.Header.Set(k, v)
		}
	}

	// 使用不带超时的传输层（流式请求可能持续很长时间）
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	return client.Do(req)
}

// GenerateRequestID 生成请求ID
func GenerateRequestID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 24)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return "chatcmpl-" + string(b)
}

// ParseErrorResponse 解析错误响应
func ParseErrorResponse(body []byte) string {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	return string(body)
}
