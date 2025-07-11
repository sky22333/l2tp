package api

import (
	"fmt"
	"l2tp-manager/internal/database"
	"l2tp-manager/internal/services"
	"net/http"
	"strconv"
	"time"
	"os"
	"path/filepath"
	"io"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Handler API处理器
type Handler struct {
	AuthService    *services.AuthService
	L2TPService    *services.L2TPService
	RoutingService *services.RoutingService
	WSManager      *services.WSManager
	DB             *gorm.DB
}

// NewHandler 新API处理器
func NewHandler(authService *services.AuthService, l2tpService *services.L2TPService, routingService *services.RoutingService, wsManager *services.WSManager, db *gorm.DB) *Handler {
	return &Handler{
		AuthService:    authService,
		L2TPService:    l2tpService,
		RoutingService: routingService,
		WSManager:      wsManager,
		DB:             db,
	}
}

// LoginRequest 登录请求结构
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应结构
type LoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Token   string `json:"token,omitempty"`
	User    User   `json:"user,omitempty"`
}

// User 用户信息结构
type User struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// ApiResponse 通用API响应结构
type ApiResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Login 用户登录
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, LoginResponse{
			Success: false,
			Message: "请求参数错误",
		})
		return
	}

	// 从数据库验证用户
	var user database.User
	result := h.DB.Where("username = ?", req.Username).First(&user)
	if result.Error != nil {
		c.JSON(http.StatusUnauthorized, LoginResponse{
			Success: false,
			Message: "用户名或密码错误",
		})
		return
	}

	// 验证密码（生产环境应该使用bcrypt）
	if user.Password != req.Password {
		c.JSON(http.StatusUnauthorized, LoginResponse{
			Success: false,
			Message: "用户名或密码错误",
		})
		return
	}

	// 生成JWT令牌
	token, err := h.AuthService.GenerateToken(user.ID, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, LoginResponse{
			Success: false,
			Message: "生成令牌失败",
		})
		return
	}

	c.JSON(http.StatusOK, LoginResponse{
		Success: true,
		Message: "登录成功",
		Token:   token,
		User: User{
			ID:       user.ID,
			Username: user.Username,
		},
	})
}

// RefreshToken 刷新令牌
func (h *Handler) RefreshToken(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, ApiResponse{
			Success: false,
			Message: "缺少认证令牌",
		})
		return
	}

	token := authHeader[7:]
	newToken, err := h.AuthService.RefreshToken(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, ApiResponse{
			Success: false,
			Message: "令牌刷新失败",
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "令牌刷新成功",
		Data:    gin.H{"token": newToken},
	})
}

// GetServers 获取所有L2TP服务器
func (h *Handler) GetServers(c *gin.Context) {
	servers, err := h.L2TPService.GetServers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: "获取服务器列表失败",
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "获取成功",
		Data:    servers,
	})
}

// CreateServer 创建L2TP服务器
func (h *Handler) CreateServer(c *gin.Context) {
	var server database.L2TPServer
	if err := c.ShouldBindJSON(&server); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	// 验证必填字段
	if server.Name == "" || server.Host == "" || server.Username == "" || server.Password == "" {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "请填写完整的服务器信息",
		})
		return
	}

	// 验证中转端口
	if server.L2TPPort <= 0 {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "请输入有效的中转端口",
		})
		return
	}

	// 创建服务器
	if err := h.L2TPService.CreateServer(&server); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	// 添加到路由服务
	h.RoutingService.AddL2TPServer(&server)

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器创建成功",
		Data:    server,
	})
}

// UpdateServer 更新L2TP服务器
func (h *Handler) UpdateServer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	var server database.L2TPServer
	if err := c.ShouldBindJSON(&server); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	// 更新服务器
	if err := h.L2TPService.UpdateServer(uint(id), &server); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器更新成功",
	})
}

// StartServer 启动L2TP服务器
func (h *Handler) StartServer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	// 获取服务器信息
	server, err := h.L2TPService.GetServer(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, ApiResponse{
			Success: false,
			Message: "服务器不存在",
		})
		return
	}

	// 检查服务器状态
	if server.Status == "running" {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "服务器已在运行中",
		})
		return
	}

	// 启动服务器
	if err := h.L2TPService.StartServer(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("启动失败: %v", err),
		})
		return
	}

	// 更新路由服务状态
	h.RoutingService.UpdateServerStatus(uint(id), "running")

	// 等待一段时间再检查状态
	time.Sleep(2 * time.Second)

	// 验证服务器是否真的启动了
	status, err := h.L2TPService.GetServerStatus(uint(id))
	if err != nil {
		c.JSON(http.StatusOK, ApiResponse{
			Success: true,
			Message: "服务器启动命令已发送，但无法验证状态",
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器启动成功",
		Data:    status,
	})
}

// StopServer 停止L2TP服务器
func (h *Handler) StopServer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	// 获取服务器信息
	server, err := h.L2TPService.GetServer(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, ApiResponse{
			Success: false,
			Message: "服务器不存在",
		})
		return
	}

	// 检查服务器状态
	if server.Status == "stopped" {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "服务器已停止",
		})
		return
	}

	// 停止服务器
	if err := h.L2TPService.StopServer(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("停止失败: %v", err),
		})
		return
	}

	// 更新路由服务状态
	h.RoutingService.UpdateServerStatus(uint(id), "stopped")

	// 等待一段时间再检查状态
	time.Sleep(1 * time.Second)

	// 验证服务器是否真的停止了
	status, err := h.L2TPService.GetServerStatus(uint(id))
	if err != nil {
		c.JSON(http.StatusOK, ApiResponse{
			Success: true,
			Message: "服务器停止命令已发送，但无法验证状态",
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器停止成功",
		Data:    status,
	})
}

// RestartServer 重启L2TP服务器
func (h *Handler) RestartServer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	if err := h.L2TPService.RestartServer(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器重启成功",
	})
}

// GetServerStatus 获取服务器状态
func (h *Handler) GetServerStatus(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	status, err := h.L2TPService.GetServerStatus(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, ApiResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "获取状态成功",
		Data:    status,
	})
}

// GetServerLogs 获取服务器日志
func (h *Handler) GetServerLogs(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	// 获取行数参数
	linesStr := c.DefaultQuery("lines", "100")
	lines, err := strconv.Atoi(linesStr)
	if err != nil {
		lines = 100
	}

	// 获取服务器信息
	server, err := h.L2TPService.GetServer(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, ApiResponse{
			Success: false,
			Message: "服务器不存在",
		})
		return
	}

	// 获取日志
	sshService := services.NewSSHService()
	logs, err := sshService.GetServerLogs(server, lines)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("获取日志失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "获取日志成功",
		Data:    gin.H{"logs": logs},
	})
}

// GetTrafficStats 获取流量统计
func (h *Handler) GetTrafficStats(c *gin.Context) {
	stats := h.RoutingService.GetTrafficStats()
	
	// 格式化数据
	formattedStats := make(map[string]interface{})
	totalBytes := int64(0)
	totalPackets := int64(0)
	
	for key, stat := range stats {
		formattedStats[key] = map[string]interface{}{
			"bytes_sent":       stat.BytesSent,
			"bytes_received":   stat.BytesReceived,
			"packets_sent":     stat.PacketsSent,
			"packets_received": stat.PacketsReceived,
			"last_update":      stat.LastUpdate,
		}
		totalBytes += stat.BytesSent + stat.BytesReceived
		totalPackets += stat.PacketsSent + stat.PacketsReceived
	}
	
	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "获取统计成功",
		Data: gin.H{
			"stats":        formattedStats,
			"total_bytes":  totalBytes,
			"total_packets": totalPackets,
		},
	})
}

// GetSystemStatus 获取系统状态
func (h *Handler) GetSystemStatus(c *gin.Context) {
	status := h.RoutingService.GetSystemStatus()
	
	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "获取系统状态成功",
		Data:    status,
	})
}

// BackupDatabase 备份数据库（前端未实现）
func (h *Handler) BackupDatabase(c *gin.Context) {
	// 创建备份文件名
	timestamp := time.Now().Format("20060102_150405")
	backupPath := fmt.Sprintf("backup_%s.db", timestamp)
	
	// 执行备份
	err := database.BackupDatabase(h.DB, backupPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("备份失败: %v", err),
		})
		return
	}
	
	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "数据库备份成功",
		Data:    gin.H{"backup_file": backupPath},
	})
}

// RestoreDatabase 恢复数据库
func (h *Handler) RestoreDatabase(c *gin.Context) {
	// 处理文件上传
	file, header, err := c.Request.FormFile("backup_file")
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "上传文件失败",
		})
		return
	}
	defer file.Close()
	
	// 创建临时文件
	tempPath := filepath.Join(os.TempDir(), header.Filename)
	tempFile, err := os.Create(tempPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: "创建临时文件失败",
		})
		return
	}
	defer tempFile.Close()
	defer os.Remove(tempPath)
	
	// 复制文件内容
	_, err = io.Copy(tempFile, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: "保存文件失败",
		})
		return
	}
	
	// 执行恢复
	err = database.RestoreDatabase(tempPath, "l2tp_manager.db")
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("恢复失败: %v", err),
		})
		return
	}
	
	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "数据库恢复成功",
	})
} 

// DeleteServer 删除L2TP服务器
func (h *Handler) DeleteServer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Success: false,
			Message: "无效的服务器ID",
		})
		return
	}

	// 获取服务器信息
	server, err := h.L2TPService.GetServer(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, ApiResponse{
			Success: false,
			Message: "服务器不存在",
		})
		return
	}

	// 如果服务器正在运行，先停止它
	if server.Status == "running" {
		if err := h.L2TPService.StopServer(uint(id)); err != nil {
			c.JSON(http.StatusInternalServerError, ApiResponse{
				Success: false,
				Message: fmt.Sprintf("停止服务器失败: %v", err),
			})
			return
		}
	}

	// 从路由服务移除
	h.RoutingService.RemoveL2TPServer(server.L2TPPort)

	// 删除服务器
	if err := h.L2TPService.DeleteServer(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{
			Success: false,
			Message: fmt.Sprintf("删除失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, ApiResponse{
		Success: true,
		Message: "服务器删除成功",
	})
}

// HandleWebSocket 处理WebSocket连接
func (h *Handler) HandleWebSocket(c *gin.Context) {
	h.WSManager.HandleWebSocket(c)
} 