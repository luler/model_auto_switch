package bin

import (
	"context"
	"gin_base/app/appconfig"
	"gin_base/app/middleware"
	"gin_base/app/service/upstream"
	"gin_base/route"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func ServeCommand() *cobra.Command {

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动Gin服务",
		Run: func(cmd *cobra.Command, args []string) {
			StartServer()
		},
	}

	return cmd
}

// StartServer 开启gin服务
func StartServer() {
	gin.SetMode(os.Getenv(gin.EnvGinMode))

	engine := gin.Default()
	engine.Delims("{[", "]}")

	// 检查 templates 目录是否存在
	if _, err := os.Stat("templates"); err == nil {
		engine.LoadHTMLGlob("templates/*")
	}

	// 初始化中间件
	middleware.InitMiddleware(engine)

	// 初始化路由
	route.InitRouter(engine)

	// 初始化 OpenAI 代理
	manager := initOpenAIProxy(engine)

	// 自定义端口
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	port = ":" + port

	// 创建 HTTP 服务器
	srv := &http.Server{
		Addr:    port,
		Handler: engine,
	}

	// 启动服务器（非阻塞）
	go func() {
		logrus.Infof("Server starting on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("Server failed to start: %v", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logrus.Info("Shutting down server...")

	// 停止 Manager 的健康检查
	if manager != nil {
		manager.Stop()
		logrus.Info("Manager stopped")
	}

	// 优雅关闭 HTTP 服务器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logrus.Errorf("Server forced to shutdown: %v", err)
	}

	logrus.Info("Server exited")
}

// initOpenAIProxy 初始化 OpenAI 代理服务
func initOpenAIProxy(engine *gin.Engine) *upstream.Manager {
	config := loadOpenAIProxyConfig()
	if config == nil || len(config.Providers) == 0 {
		logrus.Warn("OpenAI proxy not configured, skipping")
		return nil
	}

	// 创建管理器配置
	mgrConfig := upstream.ManagerConfig{
		MaxFailures:       config.MaxFailures,
		RecoveryInterval:  time.Duration(config.RecoveryInterval) * time.Second,
		HealthCheckPeriod: time.Duration(config.HealthCheckPeriod) * time.Second,
	}

	if mgrConfig.MaxFailures <= 0 {
		mgrConfig.MaxFailures = 3
	}
	if mgrConfig.RecoveryInterval <= 0 {
		mgrConfig.RecoveryInterval = 30 * time.Second
	}
	if mgrConfig.HealthCheckPeriod <= 0 {
		mgrConfig.HealthCheckPeriod = 60 * time.Second
	}

	manager := upstream.NewManager(config.Providers, mgrConfig)

	// 初始化路由（忽略返回的 adminCtrl，因为它会在内部保持对 manager 的引用）
	_ = route.InitOpenAIRouter(engine, manager, config.APIKeys, config.AdminKey, config.MaxRetries)

	logrus.Infof("OpenAI proxy initialized with %d providers (max_retries: %d)", len(config.Providers), config.MaxRetries)
	for _, p := range config.Providers {
		logrus.Infof("  - %s:", p.Name)
		for _, mm := range p.ModelMappings {
			alias := mm.Alias
			if alias == "" {
				alias = mm.Upstream
			}
			logrus.Infof("      %s -> %s (priority: %d, weight: %d)", alias, mm.Upstream, mm.Priority, mm.Weight)
		}
	}

	return manager
}

// loadOpenAIProxyConfig 加载 OpenAI 代理配置
func loadOpenAIProxyConfig() *appconfig.OpenAIProxyConfig {
	v := viper.New()
	v.SetConfigName("openai_proxy")
	v.SetConfigType("yaml")
	v.AddConfigPath("./app/appconfig")
	v.AddConfigPath(".")

	v.SetEnvPrefix("OPENAI_PROXY")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		configPath := filepath.Join("app", "appconfig", "openai_proxy.yaml")
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			logrus.Warnf("Failed to read openai_proxy config: %v", err)
			return nil
		}
	}

	var config appconfig.OpenAIProxyConfig
	if err := v.Unmarshal(&config); err != nil {
		logrus.Errorf("Failed to unmarshal openai_proxy config: %v", err)
		return nil
	}

	return &config
}
