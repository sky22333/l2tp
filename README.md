## L2TP


## 项目结构

```
l2tp/
├── main.go                     # 程序入口
├── go.mod                      # Go模块依赖
├── docker-compose.yml          # Docker编排配置
├── Dockerfile                  # Docker构建文件
├── README.md                   # 项目说明
├── internal/                   # 内部代码包
│   ├── api/                    # API接口层
│   │   └── handler.go          # HTTP处理器
│   ├── config/                 # 配置管理
│   │   └── config.go           # 配置加载
│   ├── database/               # 数据库层
│   │   └── database.go         # 数据模型和连接
│   ├── middleware/             # 中间件
│   │   ├── auth.go             # JWT认证
│   │   └── cors.go             # 跨域处理
│   ├── router/                 # 路由配置
│   │   └── router.go           # 路由定义
│   └── services/               # 业务逻辑层
│       ├── auth.go             # 认证服务
│       ├── l2tp.go             # L2TP服务管理
│       ├── routing.go          # UDP转发服务
│       ├── ssh.go              # SSH远程管理
│       └── websocket.go        # WebSocket实时通知
└── public/                     # 前端文件
    ├── index.html              # 主页面
    └── static/                 # 静态资源
        ├── css/style.css       # 样式文件
        └── js/app.js           # 前端逻辑
```

## 快速开始

### 环境要求
- Go 1.24
- Docker (用于容器管理)
- SSH访问权限到目标服务器

### 安装运行

```
services:
  l2tp-manager:
    image: ghcr.io/sky22333/l2tp:latest
    container_name: l2tp-manager
    restart: always
    network_mode: host
    environment:
      - ADMIN_USERNAME=admin123
      - ADMIN_PASSWORD=admin123
```


1. **克隆并编译**
```bash
go mod tidy
go build -o l2tp-manager
```

2. **启动服务**
```bash
./l2tp-manager
```

3. **访问管理面板**
- 地址: http://localhost:8080
- 用户名: admin
- 密码: admin123



> 内部使用 `siomiz/softethervpn:4.38-alpine` 镜像

