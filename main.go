package main

import (
	"context"
	"embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"l2tp-manager/internal/api"
	"l2tp-manager/internal/config"
	"l2tp-manager/internal/database"
	"l2tp-manager/internal/router"
	"l2tp-manager/internal/services"

	"github.com/gin-gonic/gin"
)

//go:embed public/*
var staticFiles embed.FS

func main() {
	// 加载配置
	cfg := config.Load()

	// 初始化数据库
	db, err := database.Initialize(cfg.DatabasePath)
	if err != nil {
		log.Fatal("数据库初始化失败:", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}()

	// 初始化服务
	authService := services.NewAuthService(cfg.JWTSecret)
	wsManager := services.GetWSManager()
	l2tpService := services.NewL2TPService(db, wsManager)
	routingService := services.NewRoutingService()
	
	// 设置路由服务的数据库连接
	routingService.SetDatabase(db)
	
	// 启动UDP转发服务
	go routingService.Start()

	// 初始化API处理器
	apiHandler := api.NewHandler(authService, l2tpService, routingService, wsManager, db)

	// 设置Gin模式
	if cfg.Production {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建路由器
	r := router.Setup(apiHandler, staticFiles)

	// 创建HTTP服务器
	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	// 启动服务器
	go func() {
		log.Printf("L2TP中转管理面板启动在端口 %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("服务器启动失败:", err)
		}
	}()

	// 等待中断信号关闭服务器
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭服务器...")

	// 设置5秒超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	routingService.Stop()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("服务器强制关闭:", err)
	}

	log.Println("服务器已关闭")
} 