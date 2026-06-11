package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gin_base/app/helper/log_helper"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ModelMapping 模型映射配置
type ModelMapping struct {
	Alias       string `json:"alias" yaml:"alias" mapstructure:"alias"`                                          // 对外暴露的别名（可选，不填则等于 upstream）
	Upstream    string `json:"upstream" yaml:"upstream" mapstructure:"upstream"`                                 // 上游实际模型名（必填）
	Priority    int    `json:"priority" yaml:"priority" mapstructure:"priority"`                                 // 优先级（数值越小优先级越高，默认0）
	Weight      int    `json:"weight" yaml:"weight" mapstructure:"weight"`                                       // 负载均衡权重（默认1）
	MaxFailures *int   `json:"max_failures,omitempty" yaml:"max_failures,omitempty" mapstructure:"max_failures"` // 该模型的连续失败阈值（可选，不填则使用全局配置）
}

// ProviderConfig 上游供应商配置
type ProviderConfig struct {
	Name          string         `json:"name" yaml:"name" mapstructure:"name"`                               // 供应商名称
	BaseURL       string         `json:"base_url" yaml:"base_url" mapstructure:"base_url"`                   // 基础URL
	APIKey        string         `json:"api_key" yaml:"api_key" mapstructure:"api_key"`                      // API Key
	Weight        int            `json:"weight" yaml:"weight" mapstructure:"weight"`                         // 负载均衡权重（默认1）
	Priority      int            `json:"priority" yaml:"priority" mapstructure:"priority"`                   // 优先级（数值越小优先级越高，默认0）
	Timeout       int            `json:"timeout" yaml:"timeout" mapstructure:"timeout"`                      // 超时时间（秒）
	ModelMappings []ModelMapping `json:"model_mappings" yaml:"model_mappings" mapstructure:"model_mappings"` // 模型映射
	ExcludeParams []string       `json:"exclude_params" yaml:"exclude_params" mapstructure:"exclude_params"` // 要过滤的参数列表
}

// ProviderModel 供应商+模型组合（用于路由）
type ProviderModel struct {
	Provider *Provider
	Mapping  ModelMapping
}

// ModelHealth 模型健康状态（按 upstream 存储）
type ModelHealth struct {
	Healthy          atomic.Bool  // 是否健康
	FailureCount     atomic.Int32 // 连续失败次数
	LastFailure      atomic.Int64 // 上次失败时间戳(秒)
	LastCheckTime    atomic.Int64 // 上次健康检查时间戳(纳秒)
	RecoveryAttempts atomic.Int32 // 连续恢复探测失败次数（用于退避计算）
	UnhealthySince   atomic.Int64 // 开始异常的时间戳(秒)，健康时为0
	lastFailurePath  atomic.Value // 上次失败的接口路径（用于恢复探测）
	maxFailures      int          // 该模型的连续失败阈值
	recoverMutex     sync.Mutex   // 恢复检查互斥锁，防止并发重复检查
}

// ModelStats 模型统计数据（按 alias+upstream 组合存储）
type ModelStats struct {
	TodaySuccess atomic.Int64 // 今日成功请求数
	TodayFailure atomic.Int64 // 今日失败请求数
	TotalSuccess atomic.Int64 // 总成功请求数
	TotalFailure atomic.Int64 // 总失败请求数
}

// ModelStatsSnapshot 模型统计快照
type ModelStatsSnapshot struct {
	ProviderName  string `json:"provider_name"`
	ModelAlias    string `json:"model_alias"`
	UpstreamModel string `json:"upstream_model"`
	TodaySuccess  int64  `json:"today_success"`
	TodayFailure  int64  `json:"today_failure"`
	TotalSuccess  int64  `json:"total_success"`
	TotalFailure  int64  `json:"total_failure"`
}

// ModelHealthSnapshot 模型健康状态快照
type ModelHealthSnapshot struct {
	ProviderName     string `json:"provider_name"`
	UpstreamModel    string `json:"upstream_model"`
	Healthy          bool   `json:"healthy"`
	FailureCount     int32  `json:"failure_count"`
	LastFailure      int64  `json:"last_failure"`
	LastCheckTime    int64  `json:"last_check_time"`
	RecoveryAttempts int32  `json:"recovery_attempts"`
	UnhealthySince   int64  `json:"unhealthy_since"`
}

// StatsSnapshotFile 模型统计持久化文件
type StatsSnapshotFile struct {
	Date    string               `json:"date"`
	SavedAt string               `json:"saved_at"`
	Models  []ModelStatsSnapshot `json:"models"`
}

// HealthSnapshotFile 模型健康状态快照
type HealthSnapshotFile struct {
	SavedAt string                `json:"saved_at"`
	Models  []ModelHealthSnapshot `json:"models"`
}

// Provider 上游供应商
type Provider struct {
	Config       ProviderConfig
	totalReqs    atomic.Int64            // 总请求数
	successReqs  atomic.Int64            // 成功请求数
	mu           sync.RWMutex            // 保护其他字段
	httpClient   *http.Client            // HTTP 客户端（非流式请求）
	streamClient *http.Client            // HTTP 客户端（流式请求，无超时）
	modelIndex   map[string][]int        // alias -> ModelMappings 索引
	modelHealths map[string]*ModelHealth // upstream -> ModelHealth (健康状态)
	modelStats   map[string]*ModelStats  // "alias|upstream" -> ModelStats (统计数据)
}

const (
	defaultStatsPersistPath     = "runtime/model_stats.json"
	defaultStatsPersistInterval = 10 * time.Second
	statsDateLayout             = "2006-01-02"
)

// ProviderModelHealth 供应商模型健康信息
type ProviderModelHealth struct {
	ProviderName      string  `json:"provider_name"`
	ModelAlias        string  `json:"model_alias"`    // 模型别名
	UpstreamModel     string  `json:"upstream_model"` // 上游模型名
	Healthy           bool    `json:"healthy"`
	UnhealthySince    int64   `json:"unhealthy_since"`     // 开始异常的时间戳(秒)，健康时为0
	TodayFailureCount int32   `json:"today_failure_count"` // 今日失败请求数
	FailureCount      int32   `json:"failure_count"`
	TodaySuccessReqs  int64   `json:"today_success_requests"` // 今日成功请求数
	TodayTotalReqs    int64   `json:"today_total_requests"`   // 今日总请求数
	TodaySuccessRate  float64 `json:"today_success_rate"`     // 今日成功率(%)
	TotalReqs         int64   `json:"total_requests"`         // 总请求数
	SuccessReqs       int64   `json:"success_requests"`       // 成功请求数
	SuccessRate       float64 `json:"success_rate"`           // 成功率(%)
	Priority          int     `json:"priority"`               // 优先级
	Weight            int     `json:"weight"`                 // 权重
}

// ProviderStats 供应商统计信息
type ProviderStats struct {
	Name         string                `json:"name"`
	Healthy      bool                  `json:"healthy"`
	FailureCount int32                 `json:"failure_count"`
	TotalReqs    int64                 `json:"total_requests"`
	SuccessReqs  int64                 `json:"success_requests"`
	SuccessRate  float64               `json:"success_rate"`
	ModelHealths []ProviderModelHealth `json:"model_healths"`
}

// Manager 供应商管理器
type Manager struct {
	providers         []*Provider
	mu                sync.RWMutex
	roundRobinCounter atomic.Uint64

	// 配置
	maxFailures           int           // 最大连续失败次数，超过后标记为不健康
	recoveryInterval      time.Duration // 恢复检查间隔
	recoveryBackoffFactor float64       // 恢复检查退避倍数
	recoveryMaxInterval   time.Duration // 恢复检查最大间隔
	healthCheckPeriod     time.Duration // 健康检查周期
	statsPersistPath      string        // 模型统计持久化文件路径
	statsPersistEvery     time.Duration // 模型统计持久化周期
	currentStatsDate      atomic.Value  // 当前统计日期（YYYY-MM-DD）
	dailyStatsMu          sync.RWMutex

	// 停止信号
	stopChan  chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	workerWg  sync.WaitGroup
	saveMutex sync.Mutex
}

// ManagerConfig 管理器配置
type ManagerConfig struct {
	MaxFailures           int           // 最大连续失败次数
	RecoveryInterval      time.Duration // 恢复间隔
	RecoveryBackoffFactor float64       // 恢复检查退避倍数
	RecoveryMaxInterval   time.Duration // 恢复检查最大间隔
	HealthCheckPeriod     time.Duration // 健康检查周期
}

// NewManager 创建供应商管理器
func NewManager(configs []ProviderConfig, mgrConfig ManagerConfig) *Manager {
	m := &Manager{
		providers:             make([]*Provider, 0, len(configs)),
		maxFailures:           mgrConfig.MaxFailures,
		recoveryInterval:      mgrConfig.RecoveryInterval,
		recoveryBackoffFactor: mgrConfig.RecoveryBackoffFactor,
		recoveryMaxInterval:   mgrConfig.RecoveryMaxInterval,
		healthCheckPeriod:     mgrConfig.HealthCheckPeriod,
		statsPersistPath:      defaultStatsPersistPath,
		statsPersistEvery:     defaultStatsPersistInterval,
		stopChan:              make(chan struct{}),
	}
	m.currentStatsDate.Store(currentStatsDate())

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

		// 创建优化的 Transport 配置
		transport := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 100,
			MaxConnsPerHost:     200,
			IdleConnTimeout:     90 * time.Second,
		}

		p := &Provider{
			Config: cfg,
			httpClient: &http.Client{
				Timeout:   time.Duration(cfg.Timeout) * time.Second,
				Transport: transport,
			},
			streamClient: &http.Client{
				// 流式请求不设置超时，由上下文控制
				Transport: transport,
			},
			modelIndex:   make(map[string][]int),
			modelHealths: make(map[string]*ModelHealth),
			modelStats:   make(map[string]*ModelStats),
		}

		// 构建模型索引和初始化模型健康状态、统计数据
		for i, mm := range cfg.ModelMappings {
			p.modelIndex[mm.Alias] = append(p.modelIndex[mm.Alias], i)

			// 初始化该upstream的健康状态（基于upstream而不是alias）
			// 同一个upstream只初始化一次，避免重复创建ModelHealth
			if _, exists := p.modelHealths[mm.Upstream]; !exists {
				maxFailures := m.maxFailures // 默认使用全局配置
				if mm.MaxFailures != nil && *mm.MaxFailures > 0 {
					maxFailures = *mm.MaxFailures // 如果配置了专门用于该模型的值，则覆盖
				}
				health := &ModelHealth{
					maxFailures: maxFailures,
				}
				health.Healthy.Store(true)
				health.LastCheckTime.Store(0) // 初始化为0，确保第一次检查可以触发
				// 确保每个ModelHealth都有独立的mutex
				p.modelHealths[mm.Upstream] = health
			}

			// 初始化该 alias+upstream 组合的统计数据
			statsKey := mm.Alias + "|" + mm.Upstream
			if _, exists := p.modelStats[statsKey]; !exists {
				p.modelStats[statsKey] = &ModelStats{}
			}
		}

		m.providers = append(m.providers, p)
	}

	// 启动后台健康检查
	m.StartBackgroundWorkers()

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

	// 收集所有支持该别名的 ProviderModel（按模型健康状态过滤）
	for _, p := range m.providers {
		if indices, ok := p.modelIndex[alias]; ok {
			for _, idx := range indices {
				mm := p.Config.ModelMappings[idx]
				// 检查该upstream是否健康（使用上游模型名作为key）
				if health, exists := p.modelHealths[mm.Upstream]; exists {
					p.mu.RLock()
					healthy := health.Healthy.Load()
					p.mu.RUnlock()
					if !healthy {
						continue // 跳过不健康的upstream
					}
					candidates = append(candidates, ProviderModel{
						Provider: p,
						Mapping:  mm,
					})
				}
			}
		}
	}

	// 如果没有健康的，使用所有的（作为最后手段）
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

	// Add(1) 返回加1后的值，所以用 counter-1 让轮询从索引0开始
	counter := m.roundRobinCounter.Add(1)
	targetWeight := int((counter - 1) % uint64(totalWeight))

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
func (m *Manager) RecordSuccess(p *Provider, alias string, upstreamModel string) {
	m.resetDailyCountersIfNeeded()
	p.totalReqs.Add(1)
	p.successReqs.Add(1)

	// 记录 alias+upstream 组合的统计数据
	key := statsKey(alias, upstreamModel)
	if stats, exists := p.modelStats[key]; exists {
		stats.TodaySuccess.Add(1)
		stats.TotalSuccess.Add(1)
	}

	// 重置该upstream的失败计数（健康状态按 upstream 维度）
	if health, exists := p.modelHealths[upstreamModel]; exists {
		health.FailureCount.Store(0)
		health.RecoveryAttempts.Store(0)
		if !health.Healthy.Load() {
			health.Healthy.Store(true)
			health.UnhealthySince.Store(0)
			log_helper.Info(fmt.Sprintf("Provider %s upstream model %s recovered and marked as healthy", p.Config.Name, upstreamModel))
		}
	}
}

// RecordFailure 记录失败请求
func (m *Manager) RecordFailure(p *Provider, alias string, upstreamModel string) {
	m.RecordFailureWithPath(p, alias, upstreamModel, "")
}

// RecordFailureWithPath 记录失败请求，并保存失败接口用于恢复探测
func (m *Manager) RecordFailureWithPath(p *Provider, alias string, upstreamModel string, path string) {
	m.resetDailyCountersIfNeeded()
	p.totalReqs.Add(1)

	// 记录 alias+upstream 组合的统计数据
	key := statsKey(alias, upstreamModel)
	if stats, exists := p.modelStats[key]; exists {
		stats.TodayFailure.Add(1)
		stats.TotalFailure.Add(1)
	}

	// 记录该upstream的失败（健康状态按 upstream 维度）
	if health, exists := p.modelHealths[upstreamModel]; exists {
		failures := health.FailureCount.Add(1)
		health.LastFailure.Store(time.Now().Unix())
		if path != "" {
			health.lastFailurePath.Store(path)
		}

		if int(failures) >= health.maxFailures && health.Healthy.Load() {
			health.Healthy.Store(false)
			health.UnhealthySince.Store(time.Now().Unix())
			log_helper.Warning(fmt.Sprintf("Provider %s upstream model %s marked as unhealthy after %d consecutive failures", p.Config.Name, upstreamModel, failures))
		}
	}
}

// statsKey 获取模型统计key
func statsKey(alias string, upstreamModel string) string {
	return alias + "|" + upstreamModel
}

// currentStatsDate 获取当前统计日期
func currentStatsDate() string {
	return time.Now().Format(statsDateLayout)
}

// currentDate 获取当前缓存统计日期
func (m *Manager) currentDate() string {
	if value := m.currentStatsDate.Load(); value != nil {
		if date, ok := value.(string); ok && date != "" {
			return date
		}
	}
	date := currentStatsDate()
	m.currentStatsDate.Store(date)
	return date
}

// StartBackgroundWorkers 启动后台任务
func (m *Manager) StartBackgroundWorkers() {
	m.startOnce.Do(func() {
		m.workerWg.Add(1)
		go m.startHealthCheck()

		m.workerWg.Add(1)
		go m.startStatsPersistence()
	})
}

// resetDailyCountersIfNeeded 跨天时重置今日统计
func (m *Manager) resetDailyCountersIfNeeded() {
	currentDate := currentStatsDate()

	m.dailyStatsMu.RLock()
	if m.currentDate() == currentDate {
		m.dailyStatsMu.RUnlock()
		return
	}
	m.dailyStatsMu.RUnlock()

	m.dailyStatsMu.Lock()
	defer m.dailyStatsMu.Unlock()

	if m.currentDate() == currentDate {
		return
	}

	m.mu.RLock()
	providers := append([]*Provider(nil), m.providers...)
	m.mu.RUnlock()

	for _, p := range providers {
		p.mu.Lock()
		for _, stats := range p.modelStats {
			stats.TodaySuccess.Store(0)
			stats.TodayFailure.Store(0)
		}
		p.mu.Unlock()
	}

	m.currentStatsDate.Store(currentDate)
}

// buildStatsSnapshot 构建当前配置模型统计快照
func (m *Manager) buildStatsSnapshot() StatsSnapshotFile {
	m.resetDailyCountersIfNeeded()

	m.mu.RLock()
	providers := append([]*Provider(nil), m.providers...)
	m.mu.RUnlock()

	snapshot := StatsSnapshotFile{
		Date:    m.currentDate(),
		SavedAt: time.Now().Format(time.RFC3339),
		Models:  make([]ModelStatsSnapshot, 0),
	}

	for _, p := range providers {
		p.mu.RLock()
		for _, mm := range p.Config.ModelMappings {
			key := statsKey(mm.Alias, mm.Upstream)
			stats, exists := p.modelStats[key]
			if !exists {
				continue
			}
			snapshot.Models = append(snapshot.Models, ModelStatsSnapshot{
				ProviderName:  p.Config.Name,
				ModelAlias:    mm.Alias,
				UpstreamModel: mm.Upstream,
				TodaySuccess:  stats.TodaySuccess.Load(),
				TodayFailure:  stats.TodayFailure.Load(),
				TotalSuccess:  stats.TotalSuccess.Load(),
				TotalFailure:  stats.TotalFailure.Load(),
			})
		}
		p.mu.RUnlock()
	}

	return snapshot
}

// ExportStatsSnapshot 导出模型统计快照
func (m *Manager) ExportStatsSnapshot() StatsSnapshotFile {
	return m.buildStatsSnapshot()
}

// ExportHealthSnapshot 导出模型健康状态快照
func (m *Manager) ExportHealthSnapshot() HealthSnapshotFile {
	m.mu.RLock()
	providers := append([]*Provider(nil), m.providers...)
	m.mu.RUnlock()

	snapshot := HealthSnapshotFile{
		SavedAt: time.Now().Format(time.RFC3339),
		Models:  make([]ModelHealthSnapshot, 0),
	}

	for _, p := range providers {
		p.mu.RLock()
		for upstreamModel, health := range p.modelHealths {
			snapshot.Models = append(snapshot.Models, ModelHealthSnapshot{
				ProviderName:     p.Config.Name,
				UpstreamModel:    upstreamModel,
				Healthy:          health.Healthy.Load(),
				FailureCount:     health.FailureCount.Load(),
				LastFailure:      health.LastFailure.Load(),
				LastCheckTime:    health.LastCheckTime.Load(),
				RecoveryAttempts: health.RecoveryAttempts.Load(),
				UnhealthySince:   health.UnhealthySince.Load(),
			})
		}
		p.mu.RUnlock()
	}

	return snapshot
}

// ImportStatsSnapshot 导入模型统计快照，仅恢复当前配置内的模型
func (m *Manager) ImportStatsSnapshot(snapshot StatsSnapshotFile) {
	m.resetDailyCountersIfNeeded()
	currentDate := currentStatsDate()
	restoreToday := snapshot.Date == currentDate

	allowedProviders := make(map[string]*Provider, len(m.providers))
	m.mu.RLock()
	for _, p := range m.providers {
		allowedProviders[p.Config.Name] = p
	}
	m.mu.RUnlock()

	for _, item := range snapshot.Models {
		p, exists := allowedProviders[item.ProviderName]
		if !exists {
			continue
		}

		key := statsKey(item.ModelAlias, item.UpstreamModel)
		p.mu.RLock()
		stats, exists := p.modelStats[key]
		p.mu.RUnlock()
		if !exists {
			continue
		}

		stats.TotalSuccess.Store(item.TotalSuccess)
		stats.TotalFailure.Store(item.TotalFailure)
		if restoreToday {
			stats.TodaySuccess.Store(item.TodaySuccess)
			stats.TodayFailure.Store(item.TodayFailure)
		} else {
			stats.TodaySuccess.Store(0)
			stats.TodayFailure.Store(0)
		}
	}

	m.currentStatsDate.Store(currentDate)
}

// ImportHealthSnapshot 导入模型健康状态快照，仅恢复当前配置内的模型
func (m *Manager) ImportHealthSnapshot(snapshot HealthSnapshotFile) {
	allowedProviders := make(map[string]*Provider, len(m.providers))
	m.mu.RLock()
	for _, p := range m.providers {
		allowedProviders[p.Config.Name] = p
	}
	m.mu.RUnlock()

	for _, item := range snapshot.Models {
		p, exists := allowedProviders[item.ProviderName]
		if !exists {
			continue
		}

		p.mu.RLock()
		health, exists := p.modelHealths[item.UpstreamModel]
		p.mu.RUnlock()
		if !exists {
			continue
		}

		health.Healthy.Store(item.Healthy)
		health.FailureCount.Store(item.FailureCount)
		health.LastFailure.Store(item.LastFailure)
		health.LastCheckTime.Store(item.LastCheckTime)
		health.RecoveryAttempts.Store(item.RecoveryAttempts)
		health.UnhealthySince.Store(item.UnhealthySince)
	}
}

// LoadStatsFromFile 从JSON文件恢复模型统计
func (m *Manager) LoadStatsFromFile() error {
	data, err := os.ReadFile(m.statsPersistPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var snapshot StatsSnapshotFile
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}

	m.ImportStatsSnapshot(snapshot)
	return nil
}

// SaveStatsToFile 将模型统计写入JSON文件
func (m *Manager) SaveStatsToFile() error {
	m.saveMutex.Lock()
	defer m.saveMutex.Unlock()

	snapshot := m.buildStatsSnapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(m.statsPersistPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(m.statsPersistPath, data, 0644)
}

// startStatsPersistence 启动模型统计持久化
func (m *Manager) startStatsPersistence() {
	defer m.workerWg.Done()

	ticker := time.NewTicker(m.statsPersistEvery)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			if err := m.SaveStatsToFile(); err != nil {
				log_helper.Warning(fmt.Sprintf("Save model stats failed: %v", err))
			}
		}
	}
}

// startHealthCheck 启动健康检查
func (m *Manager) startHealthCheck() {
	defer m.workerWg.Done()
	ticker := time.NewTicker(m.healthCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.checkAndRecover()

			// 检查完成后，清空 ticker channel 中可能堆积的事件
		drainLoop:
			for {
				select {
				case <-ticker.C:
				default:
					break drainLoop
				}
			}
			// 重置定时器，确保下一次检查在完整周期后触发
			ticker.Reset(m.healthCheckPeriod)
		}
	}
}

// checkAndRecover 检查并恢复不健康的upstream模型
func (m *Manager) checkAndRecover() {
	m.mu.RLock()
	providers := m.providers
	m.mu.RUnlock()

	// 统计不健康的模型
	var unhealthyModels []string
	for _, p := range providers {
		p.mu.RLock()
		for upstream, health := range p.modelHealths {
			if !health.Healthy.Load() {
				unhealthyModels = append(unhealthyModels, fmt.Sprintf("%s/%s", p.Config.Name, upstream))
			}
		}
		p.mu.RUnlock()
	}

	if len(unhealthyModels) == 0 {
		return
	}

	log_helper.Info(fmt.Sprintf("Health check: %d unhealthy models: %v", len(unhealthyModels), unhealthyModels))

	for _, p := range providers {
		p.mu.RLock()
		healths := p.modelHealths
		p.mu.RUnlock()

		for upstream, health := range healths {
			if !health.Healthy.Load() {
				// 获取互斥锁，确保同一时间只有一个检查在执行
				health.recoverMutex.Lock()
				// 使用退避策略计算该模型的有效恢复间隔
				effectiveInterval := m.effectiveRecoveryInterval(health.RecoveryAttempts.Load())
				// 在锁内重新获取当前时间，确保时间检查的准确性
				now := time.Now().UnixNano()
				lastCheckTime := health.LastCheckTime.Load()
				if now-lastCheckTime >= effectiveInterval {
					// 更新检查时间
					health.LastCheckTime.Store(now)
					// 同步执行检查
					m.tryRecoverModel(p, upstream)
				}
				health.recoverMutex.Unlock()
			}
		}
	}
}

// tryRecoverModel 尝试恢复upstream模型
func (m *Manager) tryRecoverModel(p *Provider, upstreamModel string) {
	// 第一步：先检查 /v1/models 接口
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Config.Timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", p.Config.BaseURL+"/v1/models", nil)
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: create request failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.Config.APIKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: models API failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: models API returned %d", p.Config.Name, upstreamModel, resp.StatusCode))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}

	// 第二步：根据上次失败接口选择轻量探测
	if isImageFailurePath(recoveryPath(p, upstreamModel)) {
		m.tryRecoverImageModel(p, upstreamModel)
		return
	}

	m.tryRecoverChatModel(p, upstreamModel)
}

func recoveryPath(p *Provider, upstreamModel string) string {
	if health, exists := p.modelHealths[upstreamModel]; exists {
		if value := health.lastFailurePath.Load(); value != nil {
			if path, ok := value.(string); ok {
				return path
			}
		}
	}
	return ""
}

func isImageFailurePath(path string) bool {
	return strings.HasPrefix(path, "/v1/images/")
}

func (m *Manager) tryRecoverImageModel(p *Provider, upstreamModel string) {
	testCtx, testCancel := context.WithTimeout(context.Background(), time.Duration(p.Config.Timeout)*time.Second)
	defer testCancel()

	testReqBody := []byte(fmt.Sprintf(`{"model":"%s","prompt":"a small cat","n":1,"size":"1024x1024"}`, upstreamModel))
	testReq, err := http.NewRequestWithContext(testCtx, "POST", p.Config.BaseURL+"/v1/images/generations", bytes.NewReader(testReqBody))
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: create image generations request failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}

	testReq.Header.Set("Authorization", "Bearer "+p.Config.APIKey)
	testReq.Header.Set("Content-Type", "application/json")

	testResp, err := p.httpClient.Do(testReq)
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: image generations API failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}
	defer testResp.Body.Close()

	if testResp.StatusCode == http.StatusOK || testResp.StatusCode == http.StatusBadRequest {
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.FailureCount.Store(0)
			health.Healthy.Store(true)
			health.RecoveryAttempts.Store(0)
			health.UnhealthySince.Store(0)
		}
		log_helper.Info(fmt.Sprintf("Recovery check %s/%s: recovered by image generations (status %d)", p.Config.Name, upstreamModel, testResp.StatusCode))
	} else {
		attempts := int32(0)
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
			attempts = health.RecoveryAttempts.Load()
		}
		nextInterval := time.Duration(m.effectiveRecoveryInterval(attempts))
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: image generations returned %d, still unhealthy (attempts=%d, next check in ~%s)", p.Config.Name, upstreamModel, testResp.StatusCode, attempts, nextInterval))
	}
}

func (m *Manager) tryRecoverChatModel(p *Provider, upstreamModel string) {
	testCtx, testCancel := context.WithTimeout(context.Background(), time.Duration(p.Config.Timeout)*time.Second)
	defer testCancel()

	testReqBody := []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`, upstreamModel))
	testReq, err := http.NewRequestWithContext(testCtx, "POST", p.Config.BaseURL+"/v1/chat/completions", bytes.NewReader(testReqBody))
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: create completions request failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}

	testReq.Header.Set("Authorization", "Bearer "+p.Config.APIKey)
	testReq.Header.Set("Content-Type", "application/json")

	testResp, err := p.httpClient.Do(testReq)
	if err != nil {
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: completions API failed: %v", p.Config.Name, upstreamModel, err))
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
		}
		return
	}
	defer testResp.Body.Close()

	// 检查completions响应：200表示成功，或者400表示模型可能不兼容但服务可用
	if testResp.StatusCode == http.StatusOK || testResp.StatusCode == http.StatusBadRequest {
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.FailureCount.Store(0)
			health.Healthy.Store(true)
			health.RecoveryAttempts.Store(0)
			health.UnhealthySince.Store(0)
		}
		log_helper.Info(fmt.Sprintf("Recovery check %s/%s: recovered (status %d)", p.Config.Name, upstreamModel, testResp.StatusCode))
	} else {
		attempts := int32(0)
		if health, exists := p.modelHealths[upstreamModel]; exists {
			health.RecoveryAttempts.Add(1)
			attempts = health.RecoveryAttempts.Load()
		}
		nextInterval := time.Duration(m.effectiveRecoveryInterval(attempts))
		log_helper.Warning(fmt.Sprintf("Recovery check %s/%s: completions returned %d, still unhealthy (attempts=%d, next check in ~%s)", p.Config.Name, upstreamModel, testResp.StatusCode, attempts, nextInterval))
	}
}

// effectiveRecoveryInterval 根据连续恢复探测失败次数计算有效恢复间隔（纳秒）
// 公式: min(recoveryInterval * backoffFactor^attempts, recoveryMaxInterval)
// attempts <= 0 或 backoffFactor <= 1.0 时返回基础间隔
func (m *Manager) effectiveRecoveryInterval(attempts int32) int64 {
	if attempts <= 0 || m.recoveryBackoffFactor <= 1.0 {
		return m.recoveryInterval.Nanoseconds()
	}
	multiplier := math.Pow(m.recoveryBackoffFactor, float64(attempts))
	effective := float64(m.recoveryInterval.Nanoseconds()) * multiplier
	maxNanos := m.recoveryMaxInterval.Nanoseconds()
	if effective > float64(maxNanos) {
		return maxNanos
	}
	return int64(effective)
}

// HealthCheckResult 健康检查结果
type HealthCheckResult struct {
	ProviderName  string `json:"provider_name"`
	UpstreamModel string `json:"upstream_model"`
	Healthy       bool   `json:"healthy"`
}

// checkTarget 健康检查目标
type checkTarget struct {
	provider *Provider
	upstream string
}

// CheckHealthNow 立即执行健康检查，复用 tryRecoverModel，与定时健康检查逻辑完全一致。
// 如果 providerName 和 upstreamModel 非空，检查指定模型；否则检查所有异常模型。
func (m *Manager) CheckHealthNow(providerName, upstreamModel string) []HealthCheckResult {
	// 收集检查目标
	var targets []checkTarget

	if providerName != "" && upstreamModel != "" {
		// 单个检查：查找指定供应商和模型
		m.mu.RLock()
		for _, p := range m.providers {
			if p.Config.Name == providerName {
				if _, exists := p.modelHealths[upstreamModel]; exists {
					targets = append(targets, checkTarget{provider: p, upstream: upstreamModel})
				}
				break
			}
		}
		m.mu.RUnlock()
		// 未找到目标，直接返回失败
		if len(targets) == 0 {
			return []HealthCheckResult{{ProviderName: providerName, UpstreamModel: upstreamModel, Healthy: false}}
		}
	} else {
		// 批量检查：收集所有异常模型
		m.mu.RLock()
		for _, p := range m.providers {
			p.mu.RLock()
			for upstream, health := range p.modelHealths {
				if !health.Healthy.Load() {
					targets = append(targets, checkTarget{provider: p, upstream: upstream})
				}
			}
			p.mu.RUnlock()
		}
		m.mu.RUnlock()
	}

	// 统一执行检查
	var results []HealthCheckResult
	for _, t := range targets {
		health := t.provider.modelHealths[t.upstream]
		health.recoverMutex.Lock()
		health.LastCheckTime.Store(time.Now().UnixNano())
		m.tryRecoverModel(t.provider, t.upstream)
		healthy := health.Healthy.Load()
		health.recoverMutex.Unlock()
		results = append(results, HealthCheckResult{
			ProviderName:  t.provider.Config.Name,
			UpstreamModel: t.upstream,
			Healthy:       healthy,
		})
	}
	return results
}

// Stop 停止管理器
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopChan)
		m.workerWg.Wait()
		if err := m.SaveStatsToFile(); err != nil {
			log_helper.Warning(fmt.Sprintf("Final save model stats failed: %v", err))
		}
	})
}

// GetRoundRobinCounter 获取当前轮询计数器值
func (m *Manager) GetRoundRobinCounter() uint64 {
	return m.roundRobinCounter.Load()
}

// SetRoundRobinCounter 设置轮询计数器值（用于热重载时保留状态）
func (m *Manager) SetRoundRobinCounter(value uint64) {
	m.roundRobinCounter.Store(value)
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

		// 收集模型健康状态：遍历 ModelMappings，为每个 alias+upstream 组合生成一行
		p.mu.RLock()
		modelHealths := make([]ProviderModelHealth, 0, len(p.Config.ModelMappings))
		allModelsHealthy := true

		for _, mm := range p.Config.ModelMappings {
			// 获取健康状态（按 upstream 维度）
			var healthy bool = true
			var unhealthySince int64
			if health, exists := p.modelHealths[mm.Upstream]; exists {
				healthy = health.Healthy.Load()
				if !healthy {
					allModelsHealthy = false
					unhealthySince = health.UnhealthySince.Load()
				}
			}

			// 获取统计数据（按 alias+upstream 组合）
			key := statsKey(mm.Alias, mm.Upstream)
			var modelTodaySuccess, modelTodayFailure, modelTodayTotal int64
			var modelTotal, modelSuccess, modelFailure int64
			if stats, exists := p.modelStats[key]; exists {
				modelTodaySuccess = stats.TodaySuccess.Load()
				modelTodayFailure = stats.TodayFailure.Load()
				modelTodayTotal = modelTodaySuccess + modelTodayFailure
				modelSuccess = stats.TotalSuccess.Load()
				modelFailure = stats.TotalFailure.Load()
				modelTotal = modelSuccess + modelFailure
			}

			var modelTodayRate float64
			if modelTodayTotal > 0 {
				modelTodayRate = float64(modelTodaySuccess) / float64(modelTodayTotal) * 100
			}

			var modelRate float64
			if modelTotal > 0 {
				modelRate = float64(modelSuccess) / float64(modelTotal) * 100
			}

			modelHealths = append(modelHealths, ProviderModelHealth{
				ProviderName:      p.Config.Name,
				ModelAlias:        mm.Alias,
				UpstreamModel:     mm.Upstream,
				Healthy:           healthy,
				UnhealthySince:    unhealthySince,
				TodayFailureCount: int32(modelTodayFailure),
				FailureCount:      int32(modelFailure),
				TodaySuccessReqs:  modelTodaySuccess,
				TodayTotalReqs:    modelTodayTotal,
				TodaySuccessRate:  modelTodayRate,
				TotalReqs:         modelTotal,
				SuccessReqs:       modelSuccess,
				SuccessRate:       modelRate,
				Priority:          mm.Priority,
				Weight:            mm.Weight,
			})
		}
		p.mu.RUnlock()

		// Provider 级别的累计失败次数
		failure := total - success

		stats = append(stats, ProviderStats{
			Name:         p.Config.Name,
			Healthy:      allModelsHealthy,
			FailureCount: int32(failure),
			TotalReqs:    total,
			SuccessReqs:  success,
			SuccessRate:  rate,
			ModelHealths: modelHealths,
		})
	}
	return stats
}

// ResetStats 清空所有模型的成功失败统计数据
func (m *Manager) ResetStats() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, p := range m.providers {
		p.mu.RLock()
		for _, stats := range p.modelStats {
			stats.TodaySuccess.Store(0)
			stats.TodayFailure.Store(0)
			stats.TotalSuccess.Store(0)
			stats.TotalFailure.Store(0)
		}
		p.mu.RUnlock()
	}
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
		if k != "Authorization" && k != "Host" && k != "Content-Length" {
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
		if k != "Authorization" && k != "Host" && k != "Accept" && k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}

	return p.streamClient.Do(req)
}
