package middleware

import (
	"gin_base/app/model"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// OpenAIAuthMultiKeys API Key 认证中间件
func OpenAIAuthMultiKeys(validAPIKeys []string) gin.HandlerFunc {
	keySet := make(map[string]struct{}, len(validAPIKeys))
	for _, key := range validAPIKeys {
		keySet[key] = struct{}{}
	}

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, model.NewOpenAIError(
				"Missing Authorization header",
				"invalid_request_error",
				nil,
			))
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.JSON(http.StatusUnauthorized, model.NewOpenAIError(
				"Invalid Authorization header format. Expected: Bearer <api_key>",
				"invalid_request_error",
				nil,
			))
			c.Abort()
			return
		}

		apiKey := strings.TrimSpace(parts[1])
		if _, ok := keySet[apiKey]; !ok {
			c.JSON(http.StatusUnauthorized, model.NewOpenAIError(
				"Invalid API key",
				"invalid_api_key",
				nil,
			))
			c.Abort()
			return
		}

		c.Next()
	}
}
