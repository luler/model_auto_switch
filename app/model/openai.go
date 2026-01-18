package model

import "time"

// ChatCompletionRequest OpenAI 聊天完成请求
type ChatCompletionRequest struct {
	// 必填参数
	Model    string        `json:"model" binding:"required"`
	Messages []ChatMessage `json:"messages" binding:"required"`

	// 可选参数
	Audio               *AudioConfig       `json:"audio,omitempty"`
	FrequencyPenalty    *float64           `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]float64 `json:"logit_bias,omitempty"`
	Logprobs            *bool              `json:"logprobs,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	Metadata            map[string]string  `json:"metadata,omitempty"`
	Modalities          []string           `json:"modalities,omitempty"`
	N                   *int               `json:"n,omitempty"`
	ParallelToolCalls   *bool              `json:"parallel_tool_calls,omitempty"`
	Prediction          *Prediction        `json:"prediction,omitempty"`
	PresencePenalty     *float64           `json:"presence_penalty,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	ResponseFormat      *ResponseFormat    `json:"response_format,omitempty"`
	ServiceTier         string             `json:"service_tier,omitempty"`
	Stop                interface{}        `json:"stop,omitempty"`
	Store               *bool              `json:"store,omitempty"`
	Stream              bool               `json:"stream,omitempty"`
	StreamOptions       *StreamOptions     `json:"stream_options,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	ToolChoice          interface{}        `json:"tool_choice,omitempty"`
	Tools               []Tool             `json:"tools,omitempty"`
	TopLogprobs         *int               `json:"top_logprobs,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Verbosity           string             `json:"verbosity,omitempty"`
	WebSearchOptions    *WebSearchOptions  `json:"web_search_options,omitempty"`

	// 缓存和安全相关
	PromptCacheKey       string `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     string `json:"safety_identifier,omitempty"`
}

// ChatMessage 聊天消息
type ChatMessage struct {
	Role       string      `json:"role" binding:"required"`
	Content    interface{} `json:"content"` // 可以是 string 或 []ContentPart
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Refusal    string      `json:"refusal,omitempty"`
}

// ContentPart 多模态内容部分
type ContentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *ImageURL   `json:"image_url,omitempty"`
	InputAudio *InputAudio `json:"input_audio,omitempty"`
}

// ImageURL 图片URL
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// InputAudio 输入音频
type InputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// AudioConfig 音频输出配置
type AudioConfig struct {
	Voice  string `json:"voice"`
	Format string `json:"format"`
}

// Tool 工具定义
type Tool struct {
	Type     string              `json:"type"`
	Function *FunctionDefinition `json:"function,omitempty"`
}

// FunctionDefinition 函数定义
type FunctionDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
	Strict      *bool       `json:"strict,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数调用
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat 响应格式
type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema interface{} `json:"json_schema,omitempty"`
}

// StreamOptions 流式选项
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Prediction 预测输出配置
type Prediction struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// WebSearchOptions 网络搜索选项
type WebSearchOptions struct {
	SearchContextSize string   `json:"search_context_size,omitempty"`
	UserLocation      *UserLoc `json:"user_location,omitempty"`
}

// UserLoc 用户位置
type UserLoc struct {
	Type        string `json:"type"`
	Approximate *struct {
		City     string `json:"city,omitempty"`
		Country  string `json:"country,omitempty"`
		Region   string `json:"region,omitempty"`
		Timezone string `json:"timezone,omitempty"`
	} `json:"approximate,omitempty"`
}

// ChatCompletionResponse OpenAI 聊天完成响应
type ChatCompletionResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	Choices           []Choice `json:"choices"`
	Usage             *Usage   `json:"usage,omitempty"`
	ServiceTier       string   `json:"service_tier,omitempty"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
}

// Choice 选择项
type Choice struct {
	Index        int          `json:"index"`
	Message      *ChatMessage `json:"message,omitempty"`
	Delta        *ChatMessage `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
	Logprobs     interface{}  `json:"logprobs,omitempty"`
}

// Usage 使用量统计
type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// PromptTokensDetails prompt token 详情
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// CompletionTokensDetails completion token 详情
type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// ChatCompletionChunk 流式响应块
type ChatCompletionChunk struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	Choices           []Choice `json:"choices"`
	Usage             *Usage   `json:"usage,omitempty"`
	ServiceTier       string   `json:"service_tier,omitempty"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
}

// ModelsResponse 模型列表响应
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ModelInfo 模型信息
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// OpenAIError OpenAI 错误响应
type OpenAIError struct {
	Error OpenAIErrorDetail `json:"error"`
}

// OpenAIErrorDetail 错误详情
type OpenAIErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

// NewOpenAIError 创建 OpenAI 格式错误
func NewOpenAIError(message, errType string, code *string) OpenAIError {
	return OpenAIError{
		Error: OpenAIErrorDetail{
			Message: message,
			Type:    errType,
			Param:   nil,
			Code:    code,
		},
	}
}

// GetCreatedTimestamp 获取当前时间戳
func GetCreatedTimestamp() int64 {
	return time.Now().Unix()
}
