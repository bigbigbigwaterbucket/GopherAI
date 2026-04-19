package router

import (
	"GopherAI/controller/user"

	"github.com/gin-gonic/gin"
)

func RegisterUserRouter(r *gin.RouterGroup) {
	{
		r.POST("/register", user.Register)
		r.POST("/login", user.Login)
		// 验证码接口已临时关闭，与 enableRegisterCaptcha 一并恢复
		// r.POST("/captcha", user.HandleCaptcha)
	}
}
