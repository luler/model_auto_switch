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
# OpenAI 代理配置

# 对外提供的 API Keys（客户端使用这些 key 访问本服务）
api_keys:
  - "sk-your-custom-api-key"
  # - "sk-another-key"

# 管理后台登录密钥（独立于 api_keys，避免暴露客户端密钥）
admin_key: "your-admin-key"

# 请求重试配置
max_retries: 3           # 单次请求最大尝试次数（默认1不重试，设置>1启用故障转移）

# 供应商管理器配置
max_failures: 3          # 全局连续失败多少次后标记模型为不健康（可被单个模型配置覆盖）
recovery_interval: 30    # 恢复检查基础间隔（秒）
recovery_backoff_factor: 2.0  # 恢复检查退避倍数，设为1禁用退避
recovery_max_interval: 3600   # 恢复检查最大间隔（秒）
health_check_period: 3600  # 健康检查周期（秒）

# 上游供应商配置列表
providers:
  # 供应商1: OpenAI 官方
  - name: "openai"
    base_url: "https://api.openai.com"
    api_key: "sk-xxxx"
    weight: 1            # 供应商权重（用于负载均衡）
    priority: 1          # 供应商优先级（数字越小优先级越高）
    timeout: 120         # 超时时间（秒）
    # 过滤上游不支持的参数
    exclude_params:
      - thinking
      - verbosity
    model_mappings:
      # 设置 alias，对外暴露 alias 名称
      - upstream: "gpt-5"
        alias: "gpt-5"
        priority: 0   # 模型优先级（数字越小优先级越高 0-N）
        weight: 1
        # max_failures: 5  # 可选：该模型的连续失败阈值，不填则使用全局配置
      - upstream: "gpt-4o"
        alias: "gpt-5"
        priority: 0
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
| `max_failures`              | int      | 3    | 模型连续失败多少次后标记为不健康，可被单个模型配置覆盖                    |
| `recovery_interval`         | int      | 30   | 不健康模型的恢复检查基础间隔（秒）                              |
| `recovery_backoff_factor`   | float    | 2.0  | 恢复检查退避倍数；`1` 表示禁用退避，`>1` 表示每次恢复探测失败后按倍数拉长间隔 |
| `recovery_max_interval`     | int      | 3600 | 恢复检查退避后的最大间隔（秒），避免间隔无限增大                       |
| `health_check_period`       | int      | 60   | 后台健康检查任务运行周期（秒）；实际是否探测还会受恢复检查间隔和退避限制          |

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

| 字段             | 类型     | 默认值       | 说明                  |
|----------------|--------|-----------|---------------------|
| `upstream`     | string | -         | 上游实际模型名（必填）         |
| `alias`        | string | =upstream | 对外暴露的别名             |
| `weight`       | int    | 1         | 模型权重（用于负载均衡）        |
| `priority`     | int    | 0         | 模型优先级（数值越小优先级越高）   |
| `max_failures` | int    | 全局配置      | 该模型连续失败多少次后标记为不健康 |

## 健康检查与恢复退避

### 工作方式

- 每个 upstream 模型都有独立的健康状态和恢复退避次数，不同模型互不影响。
- 请求失败次数达到 `max_failures` 后，该 upstream 模型会被标记为不健康。
- 后台健康检查任务按 `health_check_period` 周期运行，但不健康模型是否真正发起恢复探测，还会受 `recovery_interval` 和退避策略限制。
- 恢复探测成功后，该模型的失败计数和退避次数都会重置。

### 退避公式

```text
effective_interval = min(recovery_interval × recovery_backoff_factor ^ recovery_attempts, recovery_max_interval)
```

示例：

```yaml
recovery_interval: 30
recovery_backoff_factor: 2.0
recovery_max_interval: 3600
```

持续恢复失败时，同一个模型的恢复探测间隔大致为：30 秒、60 秒、120 秒、240 秒……直到最大 3600 秒。

### 参数说明

- `recovery_backoff_factor: 1`：禁用退避，始终按 `recovery_interval` 固定间隔探测。
- `recovery_backoff_factor: 2.0`：默认推荐值，每次恢复失败后间隔翻倍。
- `recovery_max_interval`：退避上限，防止长期失败模型被无限延后检查。
- `health_check_period`：健康检查循环的运行周期；如果它大于当前模型的有效恢复间隔，实际探测频率会受它限制。



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
recovery_backoff_factor: 2.0
recovery_max_interval: 3600
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