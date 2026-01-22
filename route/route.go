package route

import (
	"gin_base/app/controller/admin"
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
func InitOpenAIRouter(e *gin.Engine, manager *upstream.Manager, apiKeys []string, maxRetries int) *admin.AdminController {
	// 创建 Admin 控制器
	adminCtrl := admin.NewAdminController(manager, apiKeys)

	// 创建 OpenAI 控制器，并设置 ManagerGetter
	ctrl := openai.NewController(manager, maxRetries)
	ctrl.SetManagerGetter(adminCtrl) // 让 openai controller 从 admin controller 获取 manager

	// 首页
	e.GET("/", common.ModelAuthSwitchPage)

	// favicon
	e.StaticFile("/favicon.png", "./static/image/favicon.png")

	// Admin API 组
	adminAPI := e.Group("/api/admin")
	adminAPI.POST("/login", middleware.IpRateLimit(10.0/3600, 10), adminCtrl.Login)
	adminAPI.GET("/health", adminCtrl.GetHealth)
	adminAPI.GET("/config", adminCtrl.GetConfig)
	adminAPI.POST("/config", adminCtrl.SaveConfig)
	adminAPI.GET("/logs", adminCtrl.GetLogs)

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

	return adminCtrl
}
