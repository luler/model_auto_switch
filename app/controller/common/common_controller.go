package common

import (
	"gin_base/app/helper/response_helper"
	"net/http"

	"github.com/gin-gonic/gin"
)

func Test(c *gin.Context) {
	response_helper.Success(c, "访问成功")
}

// 首页
func ModelAuthSwitchPage(c *gin.Context) {
	c.HTML(http.StatusOK, "model_auth_switch.html", nil)
}
