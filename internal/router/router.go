package router

import (
	"embed"
	"net/http"

	"l2tp-manager/internal/api"
	"l2tp-manager/internal/middleware"

	"github.com/gin-gonic/gin"
)

// Setup 设置路由
func Setup(handler *api.Handler, staticFiles embed.FS) *gin.Engine {
	r := gin.Default()

	// 禁用CORS中间件 - 不允许跨域访问
	// r.Use(middleware.CORS())

	// 静态文件服务(嵌入的前端文件)
	r.GET("/", func(c *gin.Context) {
		data, err := staticFiles.ReadFile("public/index.html")
		if err != nil {
			c.String(http.StatusNotFound, "页面未找到")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})

	// 静态资源路由
	r.GET("/static/*filepath", func(c *gin.Context) {
		filepath := c.Param("filepath")
		fullPath := "public/static" + filepath
		data, err := staticFiles.ReadFile(fullPath)
		if err != nil {
			c.String(http.StatusNotFound, "文件未找到: "+fullPath)
			return
		}

		// 设置正确的Content-Type
		contentType := getContentType(filepath)
		c.Data(http.StatusOK, contentType, data)
	})

	// WebSocket路由(不需要JWT验证)
	r.GET("/ws/status", handler.HandleWebSocket)

	// API路由组
	api := r.Group("/api")
	{
		// 认证相关路由(不需要JWT验证)
		auth := api.Group("/auth")
		{
			auth.POST("/login", handler.Login)
			auth.POST("/refresh", handler.RefreshToken)
		}

		// 需要JWT验证的路由
		protected := api.Group("/")
		protected.Use(middleware.JWTAuth(handler.AuthService))
		{
			// L2TP服务器管理
			servers := protected.Group("/servers")
			{
				servers.GET("", handler.GetServers)
				servers.POST("", handler.CreateServer)
				servers.PUT("/:id", handler.UpdateServer)
				servers.DELETE("/:id", handler.DeleteServer)
				servers.POST("/:id/start", handler.StartServer)
				servers.POST("/:id/stop", handler.StopServer)
				servers.POST("/:id/restart", handler.RestartServer)
				servers.GET("/:id/status", handler.GetServerStatus)
				servers.GET("/:id/logs", handler.GetServerLogs)
			}

			// 流量统计
			traffic := protected.Group("/traffic")
			{
				traffic.GET("/stats", handler.GetTrafficStats)
			}

			// 系统管理
			system := protected.Group("/system")
			{
				system.GET("/status", handler.GetSystemStatus)
				system.POST("/backup", handler.BackupDatabase)
				system.POST("/restore", handler.RestoreDatabase)
			}
		}
	}

	return r
}

// getContentType 根据文件扩展名返回Content-Type
func getContentType(filepath string) string {
	switch {
	case filepath[len(filepath)-4:] == ".css":
		return "text/css"
	case filepath[len(filepath)-3:] == ".js":
		return "application/javascript"
	case filepath[len(filepath)-4:] == ".png":
		return "image/png"
	case filepath[len(filepath)-4:] == ".jpg" || filepath[len(filepath)-5:] == ".jpeg":
		return "image/jpeg"
	case filepath[len(filepath)-4:] == ".svg":
		return "image/svg+xml"
	case filepath[len(filepath)-4:] == ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
} 