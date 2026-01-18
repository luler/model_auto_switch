# OpenAI API 代理网关

一个支持多供应商、负载均衡和故障转移的 OpenAI 兼容 API 代理网关。

## 功能特性

- **多供应商支持**：可配置多个上游 API 供应商
- **模型别名**：对外暴露统一的模型别名，隐藏实际上游模型名
- **负载均衡**：支持基于权重的加权轮询负载均衡
- **优先级调度**：支持供应商和模型级别的优先级配置
- **故障转移**：请求失败时自动尝试其他可用供应商/模型
- **健康检查**：自动检测不健康的供应商并在恢复后重新启用
- **参数过滤**：可过滤上游不支持的请求参数
- **多 API Key**：支持配置多个对外 API Key

## 快速开始

### 1. 配置文件

编辑 `app/appconfig/openai_proxy.yaml`：

```yaml
# 对外提供的 API Keys
api_keys:
  - "sk-your-custom-api-key"

# 请求重试配置
max_retries: 3  # 最大尝试次数（默认1不重试，设置>1启用故障转移）

# 供应商管理器配置
max_failures: 3          # 连续失败多少次后标记供应商为不健康
recovery_interval: 30    # 恢复检查间隔（秒）
health_check_period: 60  # 健康检查周期（秒）

# 上游供应商配置
providers:
  - name: "provider-a"
    base_url: "https://api.provider-a.com"
    api_key: "sk-xxx"
    weight: 1
    priority: 1
    timeout: 120
    model_mappings:
      - upstream: "gpt-4"
        alias: "my-gpt4"
        priority: 1
        weight: 1
```

### 2. 启动服务

```bash
go run main.go serve
```

### 3. 使用 API

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-custom-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "my-gpt4",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## 配置详解

### 全局配置

| 字段                    | 类型       | 默认值 | 说明                                |
|-----------------------|----------|-----|-----------------------------------|
| `api_keys`            | []string | -   | 对外提供的 API Keys（客户端使用这些 key 访问本服务） |
| `max_retries`         | int      | 1   | 单次请求最大尝试次数（1=不重试，>1=启用故障转移）       |
| `max_failures`        | int      | 3   | 供应商连续失败多少次后标记为不健康                 |
| `recovery_interval`   | int      | 30  | 不健康供应商的恢复检查间隔（秒）                  |
| `health_check_period` | int      | 60  | 健康检查周期（秒）                         |

### 供应商配置 (providers)

| 字段               | 类型       | 默认值 | 说明                |
|------------------|----------|-----|-------------------|
| `name`           | string   | -   | 供应商名称（用于日志和监控）    |
| `base_url`       | string   | -   | 上游 API 基础 URL     |
| `api_key`        | string   | -   | 上游 API Key        |
| `weight`         | int      | 1   | 供应商权重（用于负载均衡）     |
| `priority`       | int      | 0   | 供应商优先级（数值越小优先级越高） |
| `timeout`        | int      | 60  | 请求超时时间（秒）         |
| `exclude_params` | []string | -   | 要过滤的请求参数列表        |
| `model_mappings` | []object | -   | 模型映射配置            |

### 模型映射配置 (model_mappings)

| 字段         | 类型     | 默认值       | 说明               |
|------------|--------|-----------|------------------|
| `upstream` | string | -         | 上游实际模型名（必填）      |
| `alias`    | string | =upstream | 对外暴露的别名          |
| `weight`   | int    | 1         | 模型权重（用于负载均衡）     |
| `priority` | int    | 0         | 模型优先级（数值越小优先级越高） |

## 负载均衡

### 工作原理

1. **综合优先级** = Provider.Priority + Model.Priority
2. **综合权重** = Provider.Weight × Model.Weight
3. 优先选择**综合优先级最小**的候选
4. 同优先级内按**综合权重**进行加权轮询

### 示例：同别名多模型负载均衡

```yaml
providers:
  - name: "openai"
    weight: 1
    priority: 1
    model_mappings:
      - upstream: "gemini-3-pro-preview"
        alias: "smart-model"    # 相同别名
        priority: 1
        weight: 10              # 权重 10
      - upstream: "glm-4"
        alias: "smart-model"    # 相同别名
        priority: 1
        weight: 1               # 权重 1
```

请求 `smart-model` 时：

- `gemini-3-pro-preview` 被选中的概率：10/11 ≈ 91%
- `glm-4` 被选中的概率：1/11 ≈ 9%

### 示例：优先级调度

```yaml
model_mappings:
  - upstream: "gemini-3-pro-preview"
    alias: "aa"
    priority: 0    # 优先级更高（数值小）
    weight: 1
  - upstream: "glm-4"
    alias: "aa"
    priority: 1    # 优先级较低
    weight: 1
```

- 正常情况：总是使用 `gemini-3-pro-preview`
- 故障转移时（如果 max_retries > 1）：尝试 `glm-4`

## 故障转移

### 配置

```yaml
max_retries: 3  # 最多尝试 3 个不同的供应商/模型组合
```

### 行为

| max_retries | 行为               |
|-------------|------------------|
| 0 或 1       | 只尝试 1 次，不进行故障转移  |
| 2           | 首次失败后再尝试 1 个备选   |
| 3           | 首次失败后最多再尝试 2 个备选 |

### 触发条件

以下情况会触发故障转移：

- 网络连接失败
- 请求超时
- 上游返回 5xx 错误

以下情况**不会**触发故障转移：

- 上游返回 4xx 错误（如参数错误、认证失败）

## API 端点

| 端点                     | 方法   | 说明                     |
|------------------------|------|------------------------|
| `/v1/chat/completions` | POST | Chat Completions（支持流式） |
| `/v1/models`           | GET  | 列出所有可用模型               |
| `/v1/models/:model`    | GET  | 获取指定模型信息               |
| `/internal/stats`      | GET  | 获取供应商状态统计              |

## 监控

访问 `/internal/stats` 查看供应商状态：

```json
{
  "providers": [
    {
      "name": "openai",
      "healthy": true,
      "failure_count": 0,
      "total_requests": 100,
      "success_requests": 98,
      "success_rate": 98.0
    }
  ]
}
```

## 完整配置示例

```yaml
# OpenAI 代理配置

# 对外提供的 API Keys
api_keys:
  - "sk-your-custom-api-key"
  - "sk-another-key"

# 请求重试配置
max_retries: 3

# 供应商管理器配置
max_failures: 3
recovery_interval: 30
health_check_period: 60

# 上游供应商配置
providers:
  # 主要供应商
  - name: "primary"
    base_url: "https://api.openai.com"
    api_key: "sk-xxx"
    weight: 1
    priority: 0          # 最高优先级
    timeout: 120
    model_mappings:
      - upstream: "gpt-4"
        alias: "smart"
        priority: 0
        weight: 1

  # 备用供应商
  - name: "backup"
    base_url: "https://api.backup.com"
    api_key: "sk-yyy"
    weight: 1
    priority: 1          # 较低优先级，作为备用
    timeout: 120
    exclude_params:
      - "thinking"       # 过滤不支持的参数
    model_mappings:
      - upstream: "claude-3"
        alias: "smart"   # 相同别名，作为备用
        priority: 0
        weight: 1
```

## License

Apache 2.0