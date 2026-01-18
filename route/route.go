package route

import (
	"gin_base/app/controller/common"
	"gin_base/app/controller/openai"
	"gin_base/app/helper/response_helper"
	"gin_base/app/middleware"
	"gin_base/app/service/upstream"

	"github.com/gin-gonic/gin"
)

func InitRouter(e *gin.Engine) {
	e.NoRoute(func(context *gin.Context) {
		response_helper.Common(context, 404, "路由不存在")
	})

	api := e.Group("/api")
	api.GET("/test", common.Test)

	// 登录相关
	auth := api.Group("", middleware.Auth())
	auth.POST("/test_auth", common.Test)
}

// InitOpenAIRouter 初始化 OpenAI 兼容路由
func InitOpenAIRouter(e *gin.Engine, manager *upstream.Manager, apiKeys []string, maxRetries int) {
	ctrl := openai.NewController(manager, maxRetries)

	// v1 API 组
	v1 := e.Group("/v1")

	// 应用认证中间件
	if len(apiKeys) > 0 {
		v1.Use(middleware.OpenAIAuthMultiKeys(apiKeys))
	}

	// Chat Completions
	v1.POST("/chat/completions", ctrl.ChatCompletions)

	// Models
	v1.GET("/models", ctrl.Models)
	v1.GET("/models/:model", ctrl.GetModel)

	// 内部状态接口（用于监控）
	internal := e.Group("/internal")
	internal.GET("/stats", ctrl.Stats)
}
