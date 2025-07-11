package database

import (
	"os"
	"time"

	"gorm.io/gorm"
	"github.com/glebarez/sqlite"
)

// L2TPServer L2TP落地机模型
type L2TPServer struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"not null" json:"name"`                    // 备注名称
	Host        string    `gorm:"not null" json:"host"`                    // 落地机地址
	Port        int       `gorm:"default:22" json:"port"`                  // SSH端口
	Username    string    `gorm:"not null" json:"username"`                // SSH用户名
	Password    string    `gorm:"not null" json:"password"`                // SSH密码
	L2TPPort    int       `gorm:"column:l2tp_port;not null;unique" json:"l2tp_port"`        // 中转机监听端口
	PSK         string    `gorm:"not null" json:"psk"`                     // 预共享密钥
	Users       string    `gorm:"type:text" json:"users"`                  // 用户配置(JSON格式)
	Status      string    `gorm:"default:'stopped'" json:"status"`         // 服务状态
	ExpireDate  time.Time `gorm:"column:expire_date" json:"expire_date"`   // 到期时间
	IsExpired   bool      `gorm:"-" json:"is_expired"`                     // 是否已过期(运行时计算)
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TrafficLog 流量日志
type TrafficLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ClientIP  string    `gorm:"column:client_ip;not null" json:"client_ip"`
	ServerID  uint      `gorm:"column:server_id;not null" json:"server_id"`
	SrcPort   int       `gorm:"column:src_port" json:"src_port"`
	DstPort   int       `gorm:"column:dst_port" json:"dst_port"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// User 管理员用户
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"unique;not null" json:"username"`
	Password  string    `gorm:"not null" json:"-"`                // 不在JSON中返回密码
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// Initialize 初始化数据库连接和表结构
func Initialize(databasePath string) (*gorm.DB, error) {

	dsn := databasePath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-64000)&_pragma=foreign_keys(1)"
	
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// 启用事务模式
		SkipDefaultTransaction: false,
		// 启用预编译语句缓存
		PrepareStmt: true,
	})
	if err != nil {
		return nil, err
	}

	// 获取底层sql.DB来设置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	// 设置连接池
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 自动迁移表结构
	err = db.AutoMigrate(
		&L2TPServer{},
		&TrafficLog{},
		&User{},
	)

	if err != nil {
		return nil, err
	}

	// 创建默认管理员用户
	createDefaultUser(db)

	return db, nil
}

// createDefaultUser 创建默认管理员用户
func createDefaultUser(db *gorm.DB) {
	var count int64
	db.Model(&User{}).Count(&count)
	
	if count == 0 {
		// 从环境变量读取用户名和密码，如果未设置则使用默认值
		username := os.Getenv("ADMIN_USERNAME")
		if username == "" {
			username = "admin"
		}
		password := os.Getenv("ADMIN_PASSWORD")
		if password == "" {
			password = "admin123"
		}
		
		// 这里应该使用bcrypt哈希密码
		defaultUser := User{
			Username: username,
			Password: password,
		}
		db.Create(&defaultUser)
	}
}


// BeforeUpdate GORM v2 钩子函数
func (l *L2TPServer) BeforeUpdate(tx *gorm.DB) error {
	l.IsExpired = time.Now().After(l.ExpireDate)
	return nil
}

// BeforeCreate GORM v2 钩子函数
func (l *L2TPServer) BeforeCreate(tx *gorm.DB) error {
	l.IsExpired = time.Now().After(l.ExpireDate)
	return nil
}

// BackupDatabase 备份数据库
func BackupDatabase(db *gorm.DB, backupPath string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	
	// SQLite备份逻辑
	backupSQL := "VACUUM INTO '" + backupPath + "'"
	return sqlDB.QueryRow(backupSQL).Err()
}

// RestoreDatabase 恢复数据库
func RestoreDatabase(backupPath, targetPath string) error {
	// 暂未实现
	return nil
} 