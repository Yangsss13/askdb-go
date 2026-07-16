# AskDB-Go

> Go 后端项目，演示自然语言查询数据库的核心链路。

## ⚠️ 生产环境警告

本项目中所有密码（MySQL、RabbitMQ 等）均为**本地开发专用默认值**，绝不能用于任何共享或生产环境。生产环境请通过环境变量或密钥管理系统注入凭证。

---

## 技术栈

| 组件 | 版本 |
|---|---|
| Go | 1.26+ |
| Gin | v1 |
| GORM | v2 |
| MySQL | 8.0 |
| Redis | 7 |
| RabbitMQ | 3.13 |
| Docker Compose | v2 |

---

## 快速开始（本地开发）

### 前提条件

- Go 1.21+（需支持 `log/slog`）
- Docker Desktop

### 1. 启动基础设施（Windows PowerShell）

```powershell
# 启动 MySQL、Redis、RabbitMQ
docker compose up -d

# 等待所有服务健康（约 30 秒）
docker compose ps
```

### 2. 配置环境变量

```powershell
# 复制示例配置
Copy-Item .env.example .env
# .env 已包含本地开发默认值，无需修改
```

### 3. 启动 API 服务

```powershell
go run ./cmd/api
```

### 4. 启动 Worker 服务（新 PowerShell 窗口）

```powershell
go run ./cmd/worker
```

### 5. 验证健康状态

```powershell
# 存活探针（永远 200）
Invoke-WebRequest -Uri http://localhost:8080/healthz | Select-Object -ExpandProperty Content

# 就绪探针（依赖全部就绪后返回 200）
Invoke-WebRequest -Uri http://localhost:8080/readyz | Select-Object -ExpandProperty Content
```

---

## 停止服务

```powershell
# 停止容器（保留数据）
docker compose stop

# 停止容器并删除数据卷（完全重置）
docker compose down -v
```

---

## 运行测试

```powershell
# 标准测试
go test ./...

# 数据竞争检测（需要 CGO，Windows 上可能需要额外工具链）
go test -race ./...
```

---

## 项目结构

```
cmd/api/        — API 进程入口（Gin HTTP 服务）
cmd/worker/     — Worker 进程入口（MQ 消费者，阶段 1 为空壳）
internal/config — 环境变量解析
internal/infra  — MySQL / Redis / RabbitMQ 连接
internal/handler— HTTP 处理器
db/seed/        — Docker 容器初始化 SQL（建库、建用户、示例数据）
docs/           — 架构与阶段规划
```

---

## 接口列表（阶段 1）

| Method | Path | 描述 |
|---|---|---|
| GET | /healthz | 存活探针，永远 200 |
| GET | /readyz | 就绪探针，依赖全部就绪返回 200，否则 503 |

---

## 文档

- [阶段规划](docs/PLAN.md)
- [架构说明](docs/ARCHITECTURE.md)
