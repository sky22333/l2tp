package config

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"os"
	"strconv"
)

// Config 应用配置结构
type Config struct {
	Port         string
	DatabasePath string
	JWTSecret    string
	Production   bool
	LogLevel     string
}

// Load 加载配置
func Load() *Config {
	return &Config{
		Port:         getEnv("PORT", "8080"),
		DatabasePath: getEnv("DATABASE_PATH", "./l2tp_manager.db"),
		JWTSecret:    getJWTSecret(),
		Production:   getEnvBool("PRODUCTION", false),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
	}
}

// getJWTSecret 获取JWT密钥，如果环境变量未设置则自动生成
func getJWTSecret() string {
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		log.Printf("使用环境变量JWT_SECRET")
		return secret
	}
	
	secret := generateRandomSecret(32)
	log.Printf("JWT密钥自动生成成功")
	return secret
}

// generateRandomSecret 生成指定长度的随机密钥
func generateRandomSecret(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		log.Fatalf("随机密钥生成失败，程序退出: %v", err)
	}
	return base64.URLEncoding.EncodeToString(bytes)
}

// getEnv 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool 获取布尔型环境变量
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
} 