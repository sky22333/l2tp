package services

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"gorm.io/gorm"
	"l2tp-manager/internal/database"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/dokodemo"
	"github.com/xtls/xray-core/proxy/freedom"
	
	// 导入Xray-core所有组件实现，自动注册到全局注册表
	_ "github.com/xtls/xray-core/main/distro/all"
)

// RoutingService Xray-core驱动的路由服务
type RoutingService struct {
	db             *gorm.DB
	servers        map[int]*database.L2TPServer // 监听端口 -> 服务器信息
	serverMutex    sync.RWMutex
	trafficStats   map[string]*TrafficStats // 流量统计
	statsMutex     sync.RWMutex
	xrayInstances  map[int]*core.Instance    // 端口 -> Xray实例
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// XrayForwarder Xray转发器
type XrayForwarder struct {
	listenPort int
	targetHost string
	targetPort int
	instance   *core.Instance
	stats      *TrafficStats
}

// TrafficStats 流量统计
type TrafficStats struct {
	BytesSent       int64
	BytesReceived   int64
	PacketsSent     int64
	PacketsReceived int64
	LastUpdate      time.Time
	mutex           sync.RWMutex
}

// NewRoutingService 创建路由服务
func NewRoutingService() *RoutingService {
	ctx, cancel := context.WithCancel(context.Background())
	return &RoutingService{
		servers:       make(map[int]*database.L2TPServer),
		trafficStats:  make(map[string]*TrafficStats),
		xrayInstances: make(map[int]*core.Instance),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// SetDatabase 设置数据库连接
func (r *RoutingService) SetDatabase(db *gorm.DB) {
	r.db = db
	r.loadServers()
}

// Start 启动路由服务
func (r *RoutingService) Start() {
	log.Println("启动Xray-core UDP转发服务...")
	
	// 加载服务器配置
	r.loadServers()
	
	// 启动所有活跃服务器的转发器
	r.serverMutex.RLock()
	for port, server := range r.servers {
		if server.Status == "running" {
			r.startXrayForwarder(port, server)
		}
	}
	r.serverMutex.RUnlock()
	
	// 启动监控协程
	r.wg.Add(1)
	go r.monitorRoutine()
	
	log.Println("Xray-core UDP转发服务启动完成")
}

// Stop 停止路由服务
func (r *RoutingService) Stop() {
	log.Println("正在停止Xray-core UDP转发服务...")
	
	r.cancel()
	
	// 停止所有Xray实例
	for port, instance := range r.xrayInstances {
		if instance != nil {
			instance.Close()
			log.Printf("停止端口 %d 的Xray实例", port)
		}
	}
	
	r.wg.Wait()
	log.Println("Xray-core UDP转发服务已停止")
}

// startXrayForwarder 启动Xray转发器
func (r *RoutingService) startXrayForwarder(listenPort int, server *database.L2TPServer) error {
	// 检查端口是否被占用
	if err := r.checkPortAvailable(listenPort); err != nil {
		return fmt.Errorf("端口 %d 不可用: %v", listenPort, err)
	}
	
	// 检查是否已存在并清理
	if instance, exists := r.xrayInstances[listenPort]; exists {
		log.Printf("端口 %d 的Xray实例已存在，先停止旧实例", listenPort)
		if instance != nil {
			if err := instance.Close(); err != nil {
				log.Printf("关闭旧Xray实例失败: %v", err)
			}
		}
		delete(r.xrayInstances, listenPort)
	}
	
	// 创建流量统计（估算模式）
	statsKey := fmt.Sprintf("%s:%d", server.Host, listenPort)
	r.statsMutex.Lock()
	if _, exists := r.trafficStats[statsKey]; !exists {
		r.trafficStats[statsKey] = &TrafficStats{
			LastUpdate: time.Now(),
		}
	}
	r.statsMutex.Unlock()
	
	// 创建Xray配置
	config := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: fmt.Sprintf("dokodemo-in-%d", listenPort),
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortList: &xnet.PortList{Range: []*xnet.PortRange{
						{From: uint32(listenPort), To: uint32(listenPort)},
					}},
					Listen: xnet.NewIPOrDomain(xnet.AnyIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address: xnet.NewIPOrDomain(xnet.ParseAddress(server.Host)),
					Port:    uint32(1701), // 固定转发到1701端口
					NetworkList: &xnet.NetworkList{
						Network: []xnet.Network{xnet.Network_UDP, xnet.Network_TCP}, // 支持TCP和UDP
					},
					FollowRedirect: false,
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				Tag: "direct",
				ProxySettings: serial.ToTypedMessage(&freedom.Config{
					DomainStrategy: freedom.Config_USE_IP,
				}),
			},
		},
	}
	
	// 创建Xray实例
	instance, err := core.New(config)
	if err != nil {
		return fmt.Errorf("创建Xray实例失败: %v", err)
	}
	
	// 启动Xray实例
	if err := instance.Start(); err != nil {
		// 确保清理失败的实例
		if closeErr := instance.Close(); closeErr != nil {
			log.Printf("清理失败的Xray实例出错: %v", closeErr)
		}
		return fmt.Errorf("启动Xray实例失败: %v", err)
	}
	
	// 验证实例是否正常运行
	if err := r.verifyXrayInstance(listenPort, 3*time.Second); err != nil {
		instance.Close()
		return fmt.Errorf("验证Xray实例失败: %v", err)
	}
	
	r.xrayInstances[listenPort] = instance
	
	log.Printf("Xray转发器启动成功: 0.0.0.0:%d -> %s:1701", listenPort, server.Host)
	
	// 启动流量监控协程
	go r.monitorTraffic(statsKey, listenPort)
	
	return nil
}

// stopXrayForwarder 停止Xray转发器
func (r *RoutingService) stopXrayForwarder(listenPort int) error {
	instance, exists := r.xrayInstances[listenPort]
	if !exists {
		log.Printf("警告: 端口 %d 的Xray实例不存在，可能已被清理", listenPort)
		return nil // 不返回错误，因为目标已达成
	}
	
	if instance != nil {
		if err := instance.Close(); err != nil {
			log.Printf("关闭端口 %d 的Xray实例时出错: %v", listenPort, err)
			// 即使关闭失败，也要清理映射
		}
	}
	
	delete(r.xrayInstances, listenPort)
	log.Printf("Xray转发器已停止: :%d", listenPort)
	
	// 等待一段时间确保端口释放
	time.Sleep(100 * time.Millisecond)
	
	return nil
}

// updateStats 更新流量统计
func (r *RoutingService) updateStats(statsKey string, bytesSent, bytesReceived, packetsSent, packetsReceived int64) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	
	if stats, exists := r.trafficStats[statsKey]; exists {
		stats.mutex.Lock()
		stats.BytesSent += bytesSent
		stats.BytesReceived += bytesReceived
		stats.PacketsSent += packetsSent
		stats.PacketsReceived += packetsReceived
		stats.LastUpdate = time.Now()
		stats.mutex.Unlock()
	}
}

// AddL2TPServer 添加L2TP服务器
func (r *RoutingService) AddL2TPServer(server *database.L2TPServer) {
	r.serverMutex.Lock()
	defer r.serverMutex.Unlock()
	
	r.servers[server.L2TPPort] = server
	log.Printf("添加服务器到路由服务: %s (%s:%d)", 
		server.Name, server.Host, server.L2TPPort)
	
	// 如果服务器状态为运行中，立即启动转发器
	if server.Status == "running" {
		if err := r.startXrayForwarder(server.L2TPPort, server); err != nil {
			log.Printf("启动新服务器转发器失败: %v", err)
		}
	}
}

// RemoveL2TPServer 移除L2TP服务器
func (r *RoutingService) RemoveL2TPServer(l2tpPort int) {
	r.serverMutex.Lock()
	defer r.serverMutex.Unlock()
	
	if server, exists := r.servers[l2tpPort]; exists {
		// 停止转发器
		if err := r.stopXrayForwarder(l2tpPort); err != nil {
			log.Printf("停止服务器转发器失败: %v", err)
		}
		
		// 从映射中移除
		delete(r.servers, l2tpPort)
		
		// 清理流量统计
		statsKey := fmt.Sprintf("%s:%d", server.Host, l2tpPort)
		r.statsMutex.Lock()
		delete(r.trafficStats, statsKey)
		r.statsMutex.Unlock()
		
		log.Printf("从路由服务移除服务器: %s (%s:%d)", 
			server.Name, server.Host, l2tpPort)
	}
}

// UpdateServerStatus 更新服务器状态
func (r *RoutingService) UpdateServerStatus(serverID uint, status string) {
	r.serverMutex.Lock()
	defer r.serverMutex.Unlock()
	
	// 查找服务器
	var targetServer *database.L2TPServer
	var targetPort int
	
	for port, server := range r.servers {
		if server.ID == serverID {
			targetServer = server
			targetPort = port
			break
		}
	}
	
	if targetServer == nil {
		log.Printf("警告: 找不到服务器 ID %d", serverID)
		return
	}
	
	// 更新状态
	targetServer.Status = status
	
	// 根据状态启动或停止转发器
	if status == "running" {
		if err := r.startXrayForwarder(targetPort, targetServer); err != nil {
			log.Printf("启动服务器 %d 转发器失败: %v", serverID, err)
		} else {
			log.Printf("服务器 %d Xray转发器已启动", serverID)
		}
	} else if status == "stopped" {
		if err := r.stopXrayForwarder(targetPort); err != nil {
			log.Printf("停止服务器 %d 转发器失败: %v", serverID, err)
		} else {
			log.Printf("服务器 %d Xray转发器已停止", serverID)
		}
	}
	
	// 更新数据库中的服务器信息
	if r.db != nil {
		r.db.Model(&database.L2TPServer{}).Where("id = ?", serverID).Update("status", status)
	}
}

// loadServers 加载服务器配置
func (r *RoutingService) loadServers() {
	if r.db == nil {
		return
	}
	
	var servers []database.L2TPServer
	if err := r.db.Find(&servers).Error; err != nil {
		log.Printf("加载服务器配置失败: %v", err)
		return
	}
	
	r.serverMutex.Lock()
	defer r.serverMutex.Unlock()
	
	// 清空现有配置
	r.servers = make(map[int]*database.L2TPServer)
	
	// 加载服务器
	for i := range servers {
		server := &servers[i]
		r.servers[server.L2TPPort] = server
		log.Printf("加载服务器: %s (0.0.0.0:%d -> %s:1701)", 
			server.Name, server.L2TPPort, server.Host)
	}
	
	log.Printf("已加载 %d 个服务器配置", len(servers))
}

// GetTrafficStats 获取流量统计
func (r *RoutingService) GetTrafficStats() map[string]*TrafficStats {
	r.statsMutex.RLock()
	defer r.statsMutex.RUnlock()
	
	// 返回副本
	stats := make(map[string]*TrafficStats)
	for k, v := range r.trafficStats {
		v.mutex.RLock()
		stats[k] = &TrafficStats{
			BytesSent:       v.BytesSent,
			BytesReceived:   v.BytesReceived,
			PacketsSent:     v.PacketsSent,
			PacketsReceived: v.PacketsReceived,
			LastUpdate:      v.LastUpdate,
		}
		v.mutex.RUnlock()
	}
	
	return stats
}

// GetSystemStatus 获取系统状态
func (r *RoutingService) GetSystemStatus() map[string]interface{} {
	r.serverMutex.RLock()
	totalServers := len(r.servers)
	runningServers := 0
	for _, server := range r.servers {
		if server.Status == "running" {
			runningServers++
		}
	}
	activeForwarders := len(r.xrayInstances)
	r.serverMutex.RUnlock()
	
	return map[string]interface{}{
		"total_servers":      totalServers,
		"running_servers":    runningServers,
		"active_forwarders":  activeForwarders,
		"active_connections": r.GetActiveConnections(),
		"forwarder_type":     "xray-dokodemo",
		"protocol_support":   []string{"UDP", "TCP", "L2TP", "IPSec"},
		"fullcone_nat":       true,
		"uptime":            time.Now().Format("2006-01-02 15:04:05"),
	}
}

// monitorRoutine 监控协程
func (r *RoutingService) monitorRoutine() {
	defer r.wg.Done()
	
	ticker := time.NewTicker(15 * time.Second) // 更频繁的健康检查
	defer ticker.Stop()
	
	log.Println("Xray实例监控协程已启动")
	
	for {
		select {
		case <-r.ctx.Done():
			log.Println("Xray实例监控协程正在退出")
			return
		case <-ticker.C:
			// 定期检查服务器状态和Xray实例健康状况
			r.checkXrayInstances()
		}
	}
}

// checkXrayInstances 检查Xray实例健康状况
func (r *RoutingService) checkXrayInstances() {
	r.serverMutex.RLock()
	defer r.serverMutex.RUnlock()
	
	for port, server := range r.servers {
		if server.Status == "running" {
			if instance, exists := r.xrayInstances[port]; !exists || instance == nil {
				log.Printf("检测到端口 %d 的Xray实例异常，尝试重启", port)
				if err := r.startXrayForwarder(port, server); err != nil {
					log.Printf("重启端口 %d 的Xray实例失败: %v", port, err)
				}
			} else {
				// 检查端口是否仍然可用（实例可能异常但未清理）
				if err := r.verifyXrayInstance(port, 1*time.Second); err != nil {
					log.Printf("端口 %d 的Xray实例健康检查失败，尝试重启: %v", port, err)
					instance.Close()
					delete(r.xrayInstances, port)
					if err := r.startXrayForwarder(port, server); err != nil {
						log.Printf("重启端口 %d 的Xray实例失败: %v", port, err)
					}
				}
			}
		}
	}
}

// checkPortAvailable 检查端口是否可用
func (r *RoutingService) checkPortAvailable(port int) error {
	// 检查UDP端口
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("UDP端口 %d 被占用", port)
	}
	udpConn.Close()
	
	// 检查TCP端口
	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	
	tcpListener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("TCP端口 %d 被占用", port)
	}
	tcpListener.Close()
	
	return nil
}

// verifyXrayInstance 验证Xray实例是否正常运行
func (r *RoutingService) verifyXrayInstance(port int, timeout time.Duration) error {
	// 简单的UDP连接测试
	conn, err := net.DialTimeout("udp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return fmt.Errorf("无法连接到端口 %d: %v", port, err)
	}
	defer conn.Close()
	
	// 发送测试数据
	testData := []byte("test")
	conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(testData); err != nil {
		return fmt.Errorf("无法写入测试数据到端口 %d: %v", port, err)
	}
	
	return nil
}

// monitorTraffic 监控流量（估算模式）
func (r *RoutingService) monitorTraffic(statsKey string, port int) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			// 简单的流量估算（基于连接活跃度）
			r.estimateTraffic(statsKey, port)
		}
	}
}

// estimateTraffic 估算流量数据
func (r *RoutingService) estimateTraffic(statsKey string, port int) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	
	if stats, exists := r.trafficStats[statsKey]; exists {
		stats.mutex.Lock()
		// 模拟流量增长（实际应该基于实际监控数据）
		stats.LastUpdate = time.Now()
		stats.mutex.Unlock()
	}
}

// GetActiveConnections 获取活跃连接数
func (r *RoutingService) GetActiveConnections() int {
	r.serverMutex.RLock()
	defer r.serverMutex.RUnlock()
	
	activeCount := 0
	for _, instance := range r.xrayInstances {
		if instance != nil {
			activeCount++
		}
	}
	
	return activeCount
} 