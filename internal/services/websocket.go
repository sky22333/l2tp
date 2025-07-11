package services

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Client WebSocket客户端信息
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

// WSManager WebSocket管理器
type WSManager struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
	mutex      sync.RWMutex
}

// StatusMessage 状态消息结构
type StatusMessage struct {
	Type     string      `json:"type"`
	ServerID uint        `json:"server_id"`
	Status   string      `json:"status"`
	Message  string      `json:"message,omitempty"`
	Data     interface{} `json:"data,omitempty"`
}

var (
	upgrader = websocket.Upgrader{
		// 使用默认的同源策略检查
	}
	wsManager *WSManager
)

// NewWSManager 创建WebSocket管理器
func NewWSManager() *WSManager {
	return &WSManager{
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte),
	}
}

// Start 启动WebSocket管理器
func (manager *WSManager) Start() {
	for {
		select {
		case client := <-manager.register:
			manager.mutex.Lock()
			manager.clients[client] = true
			manager.mutex.Unlock()
			log.Printf("WebSocket客户端已连接，当前连接数: %d", len(manager.clients))
			
		case client := <-manager.unregister:
			manager.mutex.Lock()
			if _, ok := manager.clients[client]; ok {
				delete(manager.clients, client)
				close(client.send)
			}
			manager.mutex.Unlock()
			log.Printf("WebSocket客户端已断开，当前连接数: %d", len(manager.clients))
			
		case message := <-manager.broadcast:
			manager.mutex.RLock()
			for client := range manager.clients {
				select {
				case client.send <- message:
				default:
					delete(manager.clients, client)
					close(client.send)
				}
			}
			manager.mutex.RUnlock()
		}
	}
}

// HandleWebSocket 处理WebSocket连接
func (manager *WSManager) HandleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}

	// 创建客户端
	client := &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}
	
	// 注册客户端
	manager.register <- client

	// 启动消息发送和接收协程
	go manager.writeMessages(client)
	go manager.readMessages(client)
}

// writeMessages 发送消息到客户端
func (manager *WSManager) writeMessages(client *Client) {
	defer func() {
		client.conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.send:
			if !ok {
				client.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			
			if err := client.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("WebSocket发送消息失败: %v", err)
				return
			}
		}
	}
}

// readMessages 接收客户端消息
func (manager *WSManager) readMessages(client *Client) {
	defer func() {
		manager.unregister <- client
		client.conn.Close()
	}()

	for {
		_, _, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket读取消息错误: %v", err)
			}
			break
		}
	}
}

// BroadcastServerStatus 广播服务器状态变化
func (manager *WSManager) BroadcastServerStatus(serverID uint, status, message string) {
	statusMsg := StatusMessage{
		Type:     "server_status",
		ServerID: serverID,
		Status:   status,
		Message:  message,
	}

	data, err := json.Marshal(statusMsg)
	if err != nil {
		log.Printf("序列化状态消息失败: %v", err)
		return
	}

	select {
	case manager.broadcast <- data:
	default:
		log.Println("WebSocket广播通道已满，跳过消息")
	}
}

// BroadcastServerCreated 广播服务器创建
func (manager *WSManager) BroadcastServerCreated(server interface{}, message string) {
	statusMsg := StatusMessage{
		Type:    "server_created",
		Message: message,
		Data:    server,
	}

	data, err := json.Marshal(statusMsg)
	if err != nil {
		log.Printf("序列化服务器创建消息失败: %v", err)
		return
	}

	select {
	case manager.broadcast <- data:
	default:
		log.Println("WebSocket广播通道已满，跳过消息")
	}
}

// BroadcastServerUpdated 广播服务器更新
func (manager *WSManager) BroadcastServerUpdated(server interface{}, message string) {
	statusMsg := StatusMessage{
		Type:    "server_updated",
		Message: message,
		Data:    server,
	}

	data, err := json.Marshal(statusMsg)
	if err != nil {
		log.Printf("序列化服务器更新消息失败: %v", err)
		return
	}

	select {
	case manager.broadcast <- data:
	default:
		log.Println("WebSocket广播通道已满，跳过消息")
	}
}

// GetWSManager 获取全局WebSocket管理器
func GetWSManager() *WSManager {
	if wsManager == nil {
		wsManager = NewWSManager()
		go wsManager.Start()
	}
	return wsManager
} 