package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gin_base/app/helper/log_helper"
	"gin_base/app/model"
	"gin_base/app/service/upstream"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// generateRequestID 生成短随机请求ID
func generateRequestID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ConfigGetter 定义动态获取配置的接口
type ConfigGetter interface {
	GetManager() *upstream.Manager
	GetMaxRetries() int
}

// Controller OpenAI 兼容接口控制器
type Controller struct {
	configGetter ConfigGetter // 动态获取配置
}

// NewController 创建控制器
func NewController(configGetter ConfigGetter) *Controller {
	return &Controller{
		configGetter: configGetter,
	}
}

// getManager 获取当前的 manager
func (c *Controller) getManager() *upstream.Manager {
	return c.configGetter.GetManager()
}

// getMaxRetries 获取当前的最大重试次数
func (c *Controller) getMaxRetries() int {
	return c.configGetter.GetMaxRetries()
}

// ChatCompletions 处理 /v1/chat/completions 请求
func (c *Controller) ChatCompletions(ctx *gin.Context) {
	// 读取原始请求体
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		c.sendError(ctx, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}

	// 解析基本字段用于路由和验证
	var req model.ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		c.sendError(ctx, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	// 验证必要字段
	if req.Model == "" {
		c.sendError(ctx, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		c.sendError(ctx, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}

	// 使用负载均衡选择首选 ProviderModel，然后获取完整列表用于故障转移
	providerModels := c.getLoadBalancedProviderModels(req.Model)
	if len(providerModels) == 0 {
		c.sendError(ctx, http.StatusServiceUnavailable, "service_unavailable", "no provider available for model: "+req.Model)
		return
	}

	// 复制请求头（除了敏感头）
	headers := make(map[string]string)
	for k, v := range ctx.Request.Header {
		if len(v) > 0 && k != "Authorization" && k != "Host" && k != "Content-Length" {
			headers[k] = v[0]
		}
	}

	// 保存原始模型名（别名）
	aliasModel := req.Model

	// 生成请求ID用于日志追踪
	reqID := generateRequestID()

	if req.Stream {
		c.handleStreamRequest(ctx, providerModels, bodyBytes, headers, aliasModel, reqID)
	} else {
		c.handleNonStreamRequest(ctx, providerModels, bodyBytes, headers, aliasModel, reqID)
	}
}

// getLoadBalancedProviderModels 获取负载均衡后的 ProviderModel 列表
// 首选的 provider 会被放在第一位，其余按优先级/权重顺序排列用于故障转移
func (c *Controller) getLoadBalancedProviderModels(alias string) []upstream.ProviderModel {
	manager := c.getManager()
	// 获取按优先级/权重排序的完整列表
	allModels := manager.GetProviderModels(alias)
	if len(allModels) <= 1 {
		return allModels
	}

	// 使用负载均衡选择首选 ProviderModel
	selected := manager.SelectProviderModel(alias)
	if selected == nil {
		return allModels
	}

	// 重排列表：把选中的放在第一位，其余保持原顺序
	result := make([]upstream.ProviderModel, 0, len(allModels))
	result = append(result, *selected)

	for _, pm := range allModels {
		// 跳过已选中的（通过 Provider 指针和 Upstream 模型名判断）
		if pm.Provider == selected.Provider && pm.Mapping.Upstream == selected.Mapping.Upstream {
			continue
		}
		result = append(result, pm)
	}

	return result
}

// processRequestBody 处理请求体：替换模型名 + 过滤不支持的参数
func processRequestBody(body []byte, pm upstream.ProviderModel, aliasModel string) []byte {
	upstreamModel := pm.Mapping.Upstream
	excludeParams := pm.Provider.Config.ExcludeParams

	// 如果不需要任何处理，直接返回
	if upstreamModel == aliasModel && len(excludeParams) == 0 {
		return body
	}

	// 解析为通用 map
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	// 替换模型名
	if upstreamModel != aliasModel {
		data["model"] = upstreamModel
	}

	// 过滤不支持的参数
	for _, param := range excludeParams {
		delete(data, param)
	}

	// 清理值为 null 或 "[undefined]" 的参数
	for key, value := range data {
		if value == nil {
			delete(data, key)
			continue
		}
		if str, ok := value.(string); ok && str == "[undefined]" {
			delete(data, key)
		}
	}

	newBody, err := json.Marshal(data)
	if err != nil {
		return body
	}

	return newBody
}

// handleNonStreamRequest 处理非流式请求
func (c *Controller) handleNonStreamRequest(ctx *gin.Context, providerModels []upstream.ProviderModel, body []byte, headers map[string]string, aliasModel string, reqID string) {
	var lastErr error
	var triedProviders []string

	// 限制最大尝试次数
	maxAttempts := c.getMaxRetries()
	if maxAttempts > len(providerModels) {
		maxAttempts = len(providerModels)
	}

	for i := 0; i < maxAttempts; i++ {
		pm := providerModels[i]
		providerName := fmt.Sprintf("%s(%s)", pm.Provider.Config.Name, pm.Mapping.Upstream)
		triedProviders = append(triedProviders, providerName)

		// 处理请求体：替换模型名 + 过滤参数
		reqBody := processRequestBody(body, pm, aliasModel)

		// 创建带超时的上下文
		reqCtx, cancel := context.WithTimeout(ctx.Request.Context(), time.Duration(pm.Provider.Config.Timeout)*time.Second)
		resp, err := pm.Provider.ProxyRequest(reqCtx, "POST", "/v1/chat/completions", reqBody, headers)

		if err != nil {
			cancel()
			lastErr = err
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d completions %s failed: %v", reqID, aliasModel, i+1, providerName, err))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		if err != nil {
			lastErr = err
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d completions %s failed: %v", reqID, aliasModel, i+1, providerName, err))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		// 检查HTTP状态码 - 非200都视为失败
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("upstream returned status %d", resp.StatusCode)
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d completions %s failed: status %d", reqID, aliasModel, i+1, providerName, resp.StatusCode))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		// 成功响应 - 替换响应中的模型名为别名
		respBody = replaceModelInResponse(respBody, pm.Mapping.Upstream, aliasModel)
		c.getManager().RecordSuccess(pm.Provider, aliasModel, pm.Mapping.Upstream)
		attemptInfo := fmt.Sprintf("#%d", i+1)
		if i > 0 {
			attemptInfo += "(retry)"
		}
		log_helper.Info(fmt.Sprintf("[%s] %s %s completions -> %s/%s", reqID, aliasModel, attemptInfo, pm.Provider.Config.Name, pm.Mapping.Upstream))
		ctx.Data(resp.StatusCode, "application/json", respBody)
		return
	}

	// 所有供应商都失败
	log_helper.Error(fmt.Sprintf("[%s] %s all providers failed: %v, tried: %v", reqID, aliasModel, lastErr, triedProviders))
	c.sendError(ctx, http.StatusBadGateway, "upstream_error", fmt.Sprintf("all providers failed: %v", lastErr))
}

// handleStreamRequest 处理流式请求
func (c *Controller) handleStreamRequest(ctx *gin.Context, providerModels []upstream.ProviderModel, body []byte, headers map[string]string, aliasModel string, reqID string) {
	var lastErr error
	var triedProviders []string

	// 限制最大尝试次数
	maxAttempts := c.getMaxRetries()
	if maxAttempts > len(providerModels) {
		maxAttempts = len(providerModels)
	}

	for i := 0; i < maxAttempts; i++ {
		pm := providerModels[i]
		providerName := fmt.Sprintf("%s(%s)", pm.Provider.Config.Name, pm.Mapping.Upstream)
		triedProviders = append(triedProviders, providerName)

		// 处理请求体：替换模型名 + 过滤参数
		reqBody := processRequestBody(body, pm, aliasModel)

		resp, err := pm.Provider.ProxyStreamRequest(ctx.Request.Context(), "/v1/chat/completions", reqBody, headers)
		if err != nil {
			lastErr = err
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d stream %s failed: %v", reqID, aliasModel, i+1, providerName, err))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		// 检查HTTP状态码 - 非200都视为失败
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream returned status %d", resp.StatusCode)
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d stream %s failed: status %d", reqID, aliasModel, i+1, providerName, resp.StatusCode))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		// 读取前几行，检测流内容中的错误（某些上游返回HTTP 200但流内容包含错误）
		// 错误可能在第一行或后续行中出现
		reader := bufio.NewReader(resp.Body)
		var bufferedLines [][]byte
		var streamErr error

		// 最多预读取3行进行错误检测
		for j := 0; j < 3; j++ {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF && len(line) > 0 {
					// EOF但有数据，保存并检测
					bufferedLines = append(bufferedLines, line)
					if detectErr := detectStreamError(line); detectErr != nil {
						streamErr = detectErr
					}
				}
				break
			}
			bufferedLines = append(bufferedLines, line)

			// 检测错误
			if detectErr := detectStreamError(line); detectErr != nil {
				streamErr = detectErr
				break
			}

			// 如果遇到有实际内容的chunk（非空choices），说明流正常，停止预读
			if isValidStreamChunk(line) {
				break
			}
		}

		if streamErr != nil {
			resp.Body.Close()
			lastErr = streamErr
			log_helper.Warning(fmt.Sprintf("[%s] %s #%d stream %s failed: %v", reqID, aliasModel, i+1, providerName, lastErr))
			c.getManager().RecordFailure(pm.Provider, aliasModel, pm.Mapping.Upstream)
			continue
		}

		// 成功，开始流式传输
		c.getManager().RecordSuccess(pm.Provider, aliasModel, pm.Mapping.Upstream)
		attemptInfo := fmt.Sprintf("#%d", i+1)
		if i > 0 {
			attemptInfo += "(retry)"
		}
		log_helper.Info(fmt.Sprintf("[%s] %s %s stream -> %s/%s", reqID, aliasModel, attemptInfo, pm.Provider.Config.Name, pm.Mapping.Upstream))
		c.streamResponseWithBufferedLines(ctx, resp, reader, bufferedLines, pm.Mapping.Upstream, aliasModel)
		return
	}

	// 所有供应商都失败
	log_helper.Error(fmt.Sprintf("[%s] %s stream all providers failed: %v, tried: %v", reqID, aliasModel, lastErr, triedProviders))
	c.sendError(ctx, http.StatusBadGateway, "upstream_error", fmt.Sprintf("all providers failed: %v", lastErr))
}

// replaceModelInResponse 替换响应中的模型名为别名
func replaceModelInResponse(body []byte, upstreamModel, aliasModel string) []byte {
	if upstreamModel == aliasModel {
		return body
	}
	return []byte(strings.ReplaceAll(string(body), `"model":"`+upstreamModel+`"`, `"model":"`+aliasModel+`"`))
}

// replaceModelInStreamLine 替换流式响应行中的模型名
func replaceModelInStreamLine(line []byte, upstreamModel, aliasModel string) []byte {
	if upstreamModel == aliasModel {
		return line
	}
	return []byte(strings.ReplaceAll(string(line), `"model":"`+upstreamModel+`"`, `"model":"`+aliasModel+`"`))
}

// streamResponse 流式传输响应
func (c *Controller) streamResponse(ctx *gin.Context, resp *http.Response, upstreamModel, aliasModel string) {
	defer resp.Body.Close()

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		c.sendError(ctx, http.StatusInternalServerError, "server_error", "streaming not supported")
		return
	}

	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	ctx.Header("Transfer-Encoding", "chunked")

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}

		// 替换模型名
		line = replaceModelInStreamLine(line, upstreamModel, aliasModel)

		// 写入响应
		if _, writeErr := ctx.Writer.Write(line); writeErr != nil {
			break
		}
		flusher.Flush()

		// 检查是否是结束标记
		if strings.TrimSpace(string(line)) == "data: [DONE]" {
			break
		}
	}
}

// Models 处理 /v1/models 请求
func (c *Controller) Models(ctx *gin.Context) {
	models := c.getManager().GetAllModels()

	response := model.ModelsResponse{
		Object: "list",
		Data:   make([]model.ModelInfo, 0, len(models)),
	}

	created := model.GetCreatedTimestamp()
	for _, m := range models {
		response.Data = append(response.Data, model.ModelInfo{
			ID:      m,
			Object:  "model",
			Created: created,
			OwnedBy: "organization-owner",
		})
	}

	ctx.JSON(http.StatusOK, response)
}

// GetModel 处理 /v1/models/:model 请求
func (c *Controller) GetModel(ctx *gin.Context) {
	modelID := ctx.Param("model")
	models := c.getManager().GetAllModels()

	for _, m := range models {
		if m == modelID {
			ctx.JSON(http.StatusOK, model.ModelInfo{
				ID:      m,
				Object:  "model",
				Created: model.GetCreatedTimestamp(),
				OwnedBy: "organization-owner",
			})
			return
		}
	}

	c.sendError(ctx, http.StatusNotFound, "not_found_error", fmt.Sprintf("model %s not found", modelID))
}

// Stats 返回供应商状态统计
func (c *Controller) Stats(ctx *gin.Context) {
	stats := c.getManager().GetStats()
	ctx.JSON(http.StatusOK, gin.H{
		"providers": stats,
	})
}

// detectStreamError 检测流内容中的错误（OpenAI标准错误格式）
// 某些上游（如Gemini）返回HTTP 200但在流内容中包含错误
func detectStreamError(line []byte) error {
	// 快速检测：如果不包含 "error" 关键字，直接返回
	if !bytes.Contains(line, []byte(`"error"`)) {
		return nil
	}

	// 移除 SSE 前缀 "data: "
	data := line
	if bytes.HasPrefix(line, []byte("data: ")) {
		data = bytes.TrimPrefix(line, []byte("data: "))
	}
	data = bytes.TrimSpace(data)

	// 跳过空行和结束标记
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}

	// 尝试解析JSON，检测error字段
	var resp map[string]interface{}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil // 解析失败不视为错误，可能是正常的流数据
	}

	// 检测 error 字段（可能是对象或字符串）
	if errField, exists := resp["error"]; exists && errField != nil {
		switch e := errField.(type) {
		case string:
			return fmt.Errorf("stream error: %s", e)
		case map[string]interface{}:
			msg := "upstream error"
			if m, ok := e["message"].(string); ok && m != "" {
				msg = m
			}
			code := e["code"]
			return fmt.Errorf("stream error: %s (code: %v)", msg, code)
		default:
			return fmt.Errorf("stream error: %v", errField)
		}
	}

	return nil
}

// isValidStreamChunk 检测是否是有效的流数据chunk（包含实际内容）
func isValidStreamChunk(line []byte) bool {
	// 快速检测：检查是否包含实际内容的特征
	// 有效chunk通常包含 "content":" 或 "role":"
	return bytes.Contains(line, []byte(`"content":"`)) || bytes.Contains(line, []byte(`"role":"`))
}

// streamResponseWithBufferedLines 流式传输响应（包含已缓冲的行）
func (c *Controller) streamResponseWithBufferedLines(ctx *gin.Context, resp *http.Response, reader *bufio.Reader, bufferedLines [][]byte, upstreamModel, aliasModel string) {
	defer resp.Body.Close()

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		c.sendError(ctx, http.StatusInternalServerError, "server_error", "streaming not supported")
		return
	}

	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	ctx.Header("Transfer-Encoding", "chunked")

	// 先写入已缓冲的行
	for _, line := range bufferedLines {
		line = replaceModelInStreamLine(line, upstreamModel, aliasModel)
		if _, writeErr := ctx.Writer.Write(line); writeErr != nil {
			return
		}
		flusher.Flush()

		// 检查是否是结束标记
		if strings.TrimSpace(string(line)) == "data: [DONE]" {
			return
		}
	}

	// 继续读取剩余内容
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}

		// 替换模型名
		line = replaceModelInStreamLine(line, upstreamModel, aliasModel)

		// 写入响应
		if _, writeErr := ctx.Writer.Write(line); writeErr != nil {
			break
		}
		flusher.Flush()

		// 检查是否是结束标记
		if strings.TrimSpace(string(line)) == "data: [DONE]" {
			break
		}
	}
}

// sendError 发送 OpenAI 格式的错误响应
func (c *Controller) sendError(ctx *gin.Context, statusCode int, errType, message string) {
	ctx.JSON(statusCode, model.NewOpenAIError(message, errType, nil))
}
