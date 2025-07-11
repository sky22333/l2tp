package services

import (
	"encoding/json"
	"fmt"
	"time"
	"errors"

	"l2tp-manager/internal/database"

	"gorm.io/gorm"
)

// L2TPService L2TP服务管理
type L2TPService struct {
	db        *gorm.DB
	wsManager *WSManager
}

// NewL2TPService 创建新的L2TP服务
func NewL2TPService(db *gorm.DB, wsManager *WSManager) *L2TPService {
	return &L2TPService{
		db:        db,
		wsManager: wsManager,
	}
}

// L2TPUser L2TP用户结构
type L2TPUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateServer 创建L2TP服务器
func (s *L2TPService) CreateServer(server *database.L2TPServer) error {
	// 使用事务确保数据一致性
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 检查端口是否已被使用
		var count int64
		result := tx.Model(&database.L2TPServer{}).Where("l2tp_port = ?", server.L2TPPort).Count(&count)
		if result.Error != nil {
			return result.Error
		}
		if count > 0 {
			return fmt.Errorf("中转端口 %d 已被使用", server.L2TPPort)
		}

		// 设置默认状态
		server.Status = "stopped"
		server.CreatedAt = time.Now()
		server.UpdatedAt = time.Now()

		return tx.Create(server).Error
	})
	
	// 如果创建成功，通过WebSocket推送服务器创建通知
	if err == nil && s.wsManager != nil {
		s.wsManager.BroadcastServerCreated(server, fmt.Sprintf("服务器 \"%s\" 已创建", server.Name))
	}
	
	return err
}

// GetServers 获取所有L2TP服务器
func (s *L2TPService) GetServers() ([]database.L2TPServer, error) {
	var servers []database.L2TPServer
	result := s.db.Find(&servers)
	if result.Error != nil {
		return nil, result.Error
	}
	
	// 更新过期状态
	for i := range servers {
		servers[i].IsExpired = time.Now().After(servers[i].ExpireDate)
	}

	return servers, nil
}

// GetServer 根据ID获取服务器
func (s *L2TPService) GetServer(id uint) (*database.L2TPServer, error) {
	var server database.L2TPServer
	result := s.db.First(&server, id)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("服务器不存在")
		}
		return nil, result.Error
	}

	server.IsExpired = time.Now().After(server.ExpireDate)
	return &server, nil
}

// UpdateServer 更新L2TP服务器
func (s *L2TPService) UpdateServer(id uint, server *database.L2TPServer) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 检查服务器是否存在
		var existingServer database.L2TPServer
		result := tx.First(&existingServer, id)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return fmt.Errorf("服务器不存在")
			}
			return result.Error
		}

		// 如果更改了端口，检查新端口是否可用
		if server.L2TPPort != existingServer.L2TPPort {
			var count int64
			result := tx.Model(&database.L2TPServer{}).Where("l2tp_port = ? AND id != ?", server.L2TPPort, id).Count(&count)
			if result.Error != nil {
				return result.Error
			}
			if count > 0 {
				return fmt.Errorf("中转端口 %d 已被使用", server.L2TPPort)
			}
		}

		server.ID = id
		server.UpdatedAt = time.Now()
		return tx.Save(server).Error
	})
	
	if err == nil && s.wsManager != nil {
		s.wsManager.BroadcastServerUpdated(server, fmt.Sprintf("服务器 \"%s\" 已更新", server.Name))
	}
	
	return err
}

// DeleteServer 删除L2TP服务器
func (s *L2TPService) DeleteServer(id uint) error {
	server, err := s.GetServer(id)
	serverName := "未知服务器"
	if err == nil {
		serverName = server.Name
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 先停止服务
		if err := s.StopServer(id); err != nil {
			// 即使停止失败也继续删除数据库记录
		}

		// 删除流量日志
		result := tx.Where("server_id = ?", id).Delete(&database.TrafficLog{})
		if result.Error != nil {
			return result.Error
		}

		// 删除服务器记录
		result = tx.Delete(&database.L2TPServer{}, id)
		if result.Error != nil {
			return result.Error
		}
		
		if result.RowsAffected == 0 {
			return fmt.Errorf("服务器不存在或已被删除")
		}

		return nil
	})

	if err == nil && s.wsManager != nil {
		s.wsManager.BroadcastServerStatus(id, "deleted", fmt.Sprintf("服务器 \"%s\" 已删除", serverName))
	}

	return err
}

// StartServer 启动L2TP服务器
func (s *L2TPService) StartServer(id uint) error {
	server, err := s.GetServer(id)
	if err != nil {
		return err
	}

	if server.Status == "running" {
		return fmt.Errorf("服务器已在运行中")
	}

	if server.Status == "starting" {
		return fmt.Errorf("服务器正在启动中，请稍候")
	}

	// 检查服务器是否过期
	if time.Now().After(server.ExpireDate) {
		return fmt.Errorf("服务器已过期，无法启动")
	}

	// 先更新状态为"启动中"
	if err := s.updateServerStatus(id, "starting"); err != nil {
		return fmt.Errorf("更新服务器状态失败: %v", err)
	}

	// 异步启动服务器，避免阻塞前端请求
	go s.asyncStartServer(id, server)

	return nil
}

// asyncStartServer 异步启动服务器
func (s *L2TPService) asyncStartServer(id uint, server *database.L2TPServer) {
	sshService := NewSSHService()
	
	// 创建详细状态回调函数
	detailCallback := func(step string, success bool, message string) {
		if s.wsManager != nil {
			// 发送详细的进度更新
			var status string
			if success {
				status = "starting"
			} else {
				status = "error"
			}
			
			detailMessage := fmt.Sprintf("[%s] %s", step, message)
			s.wsManager.BroadcastServerStatus(id, status, detailMessage)
		}
	}
	
	// 启动容器
	if err := sshService.StartL2TPContainerWithCallback(server, detailCallback); err != nil {
		s.updateServerStatus(id, "error")
		return
	}
	
	// 容器启动验证完成，立即更新状态为运行中
	s.updateServerStatus(id, "running")
}

// StopServer 停止L2TP服务器
func (s *L2TPService) StopServer(id uint) error {
	server, err := s.GetServer(id)
	if err != nil {
		return err
	}

	if server.Status == "stopped" {
		return fmt.Errorf("服务器已停止")
	}

	if server.Status == "stopping" {
		return fmt.Errorf("服务器正在停止中，请稍候")
	}

	// 先更新状态为"停止中"
	if err := s.updateServerStatus(id, "stopping"); err != nil {
		return fmt.Errorf("更新服务器状态失败: %v", err)
	}

	// 异步停止服务器
	go s.asyncStopServer(id, server)

	return nil
}

// asyncStopServer 异步停止服务器
func (s *L2TPService) asyncStopServer(id uint, server *database.L2TPServer) {
	sshService := NewSSHService()
	
	// 创建详细状态回调函数
	detailCallback := func(step string, success bool, message string) {
		if s.wsManager != nil {
			// 发送详细的进度更新
			var status string
			if success {
				status = "stopping"
			} else {
				status = "error"
			}
			
			detailMessage := fmt.Sprintf("[%s] %s", step, message)
			s.wsManager.BroadcastServerStatus(id, status, detailMessage)
		}
	}
	
	// 停止容器
	if err := sshService.StopL2TPContainerWithCallback(server, detailCallback); err != nil {
		s.updateServerStatus(id, "error")
		return
	}
	
	// 容器停止操作完成，立即更新状态为已停止
	s.updateServerStatus(id, "stopped")
}

// RestartServer 重启L2TP服务器
func (s *L2TPService) RestartServer(id uint) error {
	server, err := s.GetServer(id)
	if err != nil {
		return err
	}

	// 如果服务器正在运行，先停止它
	if server.Status == "running" {
		if err := s.StopServer(id); err != nil {
			return err
		}
		
		// 异步等待停止完成后启动
		go s.asyncRestartServer(id)
		return nil
	}

	// 如果服务器已经停止，直接启动
	return s.StartServer(id)
}

// asyncRestartServer 异步重启服务器
func (s *L2TPService) asyncRestartServer(id uint) {
	server, err := s.GetServer(id)
	if err != nil {
		return
	}

	sshService := NewSSHService()
	
	// 创建详细状态回调函数用于停止过程
	stopDetailCallback := func(step string, success bool, message string) {
		if s.wsManager != nil {
			var status string
			if success {
				status = "stopping"
			} else {
				status = "error"
			}
			
			detailMessage := fmt.Sprintf("[重启-停止:%s] %s", step, message)
			s.wsManager.BroadcastServerStatus(id, status, detailMessage)
		}
	}
	
	// 先停止容器
	if err := sshService.StopL2TPContainerWithCallback(server, stopDetailCallback); err != nil {
		s.updateServerStatus(id, "error")
		return
	}
	
	// 容器停止完成，短暂等待确保清理完成后重新启动
	go func() {
		time.Sleep(1 * time.Second)
		s.StartServer(id)
	}()
}

// GetServerStatus 获取服务器实时状态
func (s *L2TPService) GetServerStatus(id uint) (map[string]interface{}, error) {
	server, err := s.GetServer(id)
	if err != nil {
		return nil, err
	}

	status := map[string]interface{}{
		"id":         server.ID,
		"name":       server.Name,
		"host":       server.Host,
		"l2tp_port":  server.L2TPPort,
		"status":     server.Status,
		"is_expired": server.IsExpired,
		"uptime":     "0s",
		"clients":    0,
		"container_status": "unknown",
		"last_updated": server.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	// 根据不同状态处理
	switch server.Status {
	case "running":
		// 获取容器详细状态
		sshService := NewSSHService()
		containerStatus, err := sshService.GetContainerStatus(server)
		if err != nil {
			// 无法获取容器状态，可能容器已停止但数据库状态未更新
			status["container_status"] = "error"
			status["error"] = fmt.Sprintf("容器状态异常: %v", err)
			
			// 异步更新数据库状态，避免阻塞状态查询
			go s.updateServerStatus(id, "error")
			status["status"] = "error"
		} else {
			// 验证容器是否真正运行
			if running, ok := containerStatus["running"].(bool); ok && running {
				// 合并容器状态信息
				for key, value := range containerStatus {
					status[key] = value
				}
				status["container_status"] = "running"
			} else {
				// 容器未运行，状态不同步
				status["container_status"] = "stopped"
				status["error"] = "容器已停止，状态不同步"
				go s.updateServerStatus(id, "stopped")
				status["status"] = "stopped"
			}
		}
		
	case "starting":
		status["container_status"] = "starting"
		status["message"] = "容器正在启动中，请稍候..."
		
	case "stopping":
		status["container_status"] = "stopping"
		status["message"] = "容器正在停止中，请稍候..."
		
	case "error":
		status["container_status"] = "error"
		status["message"] = "服务启动失败，请检查服务器配置或重试"
		
	case "stopped":
		status["container_status"] = "stopped"
		status["message"] = "服务已停止"
		
	default:
		status["container_status"] = "unknown"
		status["message"] = "未知状态"
	}

	return status, nil
}

// ParseUsers 解析用户配置字符串
func (s *L2TPService) ParseUsers(usersStr string) ([]L2TPUser, error) {
	var users []L2TPUser
	if usersStr == "" {
		return users, nil
	}
	
	err := json.Unmarshal([]byte(usersStr), &users)
	return users, err
}

// FormatUsers 格式化用户配置为字符串
func (s *L2TPService) FormatUsers(users []L2TPUser) (string, error) {
	if len(users) == 0 {
		return "", nil
	}
	
	data, err := json.Marshal(users)
	return string(data), err
}

// updateServerStatus 更新服务器状态
func (s *L2TPService) updateServerStatus(id uint, status string) error {
	result := s.db.Model(&database.L2TPServer{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now(),
		})
	
	if result.Error != nil {
		return result.Error
	}
	
	if result.RowsAffected == 0 {
		return fmt.Errorf("服务器不存在或状态未更新")
	}
	
	// 通过WebSocket推送状态变化
	if s.wsManager != nil {
		message := getStatusMessage(status)
		s.wsManager.BroadcastServerStatus(id, status, message)
	}
	
	return nil
}

// getStatusMessage 获取状态对应的消息
func getStatusMessage(status string) string {
	switch status {
	case "running":
		return "服务器运行正常"
	case "stopped":
		return "服务器已停止"
	case "starting":
		return "服务器正在启动..."
	case "stopping":
		return "服务器正在停止..."
	case "error":
		return "服务器启动失败"
	default:
		return "状态未知"
	}
}

// GetTrafficLogs 获取流量日志
func (s *L2TPService) GetTrafficLogs(serverID uint, limit int) ([]database.TrafficLog, error) {
	var logs []database.TrafficLog
	query := s.db.Where("server_id = ?", serverID).Order("created_at DESC")
	
	if limit > 0 {
		query = query.Limit(limit)
	}
	
	result := query.Find(&logs)
	return logs, result.Error
}

// GetTrafficStats 获取流量统计
func (s *L2TPService) GetTrafficStats(serverID uint) (map[string]interface{}, error) {
	var totalBytes int64
	var totalCount int64
	
	// 获取总流量
	result := s.db.Model(&database.TrafficLog{}).Where("server_id = ?", serverID).
		Select("COALESCE(SUM(bytes), 0) as total_bytes, COUNT(*) as total_count").
		Row().Scan(&totalBytes, &totalCount)
	
	if result != nil {
		return nil, result
	}
	
	// 获取今日流量
	var todayBytes int64
	var todayCount int64
	today := time.Now().Truncate(24 * time.Hour)
	
	result = s.db.Model(&database.TrafficLog{}).Where("server_id = ? AND created_at >= ?", serverID, today).
		Select("COALESCE(SUM(bytes), 0) as today_bytes, COUNT(*) as today_count").
		Row().Scan(&todayBytes, &todayCount)
	
	if result != nil {
		return nil, result
	}
	
	return map[string]interface{}{
		"total_bytes": totalBytes,
		"total_count": totalCount,
		"today_bytes": todayBytes,
		"today_count": todayCount,
	}, nil
} 