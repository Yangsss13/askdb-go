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

> 阶段 2 新增两个变量：`MYSQL_READER_DSN`（必填，askdb_reader 连接 askdb_demo）和
> `QUERY_TIMEOUT`（可选，默认 5s）。若沿用旧的 `.env`，需补上 `MYSQL_READER_DSN`，否则 API 启动会失败。
> 真实环境变量优先，`.env` 仅作为本地开发兜底，且始终被 Git 忽略。

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
cmd/api/          — API 进程入口（Gin HTTP 服务）
cmd/worker/       — Worker 进程入口（MQ 消费者，当前为空壳）
internal/config   — 环境变量解析
internal/infra    — MySQL / Redis / RabbitMQ / 只读 askdb_demo 连接
internal/handler  — HTTP 处理器与 DTO
internal/queryjob — 查询任务模型、状态机、仓储、Service 编排
internal/llm      — Fake LLM（固定问题 → 硬编码 SQL）
internal/queryexec— database/sql 只读查询与结果类型转换
db/migrations/    — 版本化 SQL migration（query_jobs）
db/seed/          — Docker 容器初始化 SQL（建库、建用户、示例数据）
docs/             — 架构与阶段规划
```

---

## 当前能力（阶段 2）

打通**同步**自然语言查询链路：提交问题 → Fake LLM 生成固定 SQL → 只读查询演示库 → 同步返回结果。

- 查询**同步执行**（在 API 进程内完成），尚未使用 RabbitMQ 异步分发。
- SQL 由 **Fake LLM** 按固定问题返回硬编码 SELECT，**未接入真实大模型**；用户输入不会拼接进 SQL。
- 查询结果**不缓存到 Redis**；任务最终状态持久化在 `askdb_app.query_jobs`。
- 演示数据（askdb_demo）通过 `database/sql` + 只读账号 `askdb_reader` 查询，连接池与 GORM 隔离。

Fake LLM 目前支持的固定问题：`查询所有商品`、`查询销量最高的商品`、`查询最近的订单`。

## 数据库迁移

使用版本化 SQL migration（golang-migrate），通过 Docker Compose 的 `migrate` profile 手动执行，无需在本机安装 migrate CLI：

```powershell
# 执行 up migration（创建 query_jobs 表）
docker compose --profile migrate run --rm migrate
```

迁移文件位于 `db/migrations/`，同时提供 up 与 down。

## 接口列表

| Method | Path | 描述 |
|---|---|---|
| GET | /healthz | 存活探针，永远 200 |
| GET | /readyz | 就绪探针，依赖全部就绪返回 200，否则 503 |
| POST | /api/v1/query-jobs | 提交自然语言问题，同步返回查询结果 |
| GET | /api/v1/query-jobs/:id | 查询已持久化的任务信息（不含完整结果集） |

### 请求示例

```powershell
# 提交问题（成功返回 200，包含 generated_sql、columns、rows 等）
curl -X POST http://localhost:8080/api/v1/query-jobs `
  -H "Content-Type: application/json" `
  -d '{"question":"查询所有商品"}'

# 查询任务详情
curl http://localhost:8080/api/v1/query-jobs/1
```

成功响应（节选）：

```json
{
  "job_id": 1,
  "question": "查询所有商品",
  "status": "succeeded",
  "generated_sql": "SELECT id, name, category, price, stock, created_at FROM products ORDER BY id LIMIT 100",
  "columns": ["id", "name", "category", "price", "stock", "created_at"],
  "rows": [[1, "Wireless Mouse", "Electronics", "29.99", 150, "2024-01-10 09:15:00"]],
  "row_count": 10,
  "execution_duration_ms": 7,
  "created_at": "2026-07-16T10:00:00Z",
  "finished_at": "2026-07-16T10:00:00Z"
}
```

错误码：`INVALID_QUESTION`（400）、`UNSUPPORTED_QUESTION`（422，任务保存为 failed）、`JOB_NOT_FOUND`（404）、`QUERY_EXECUTION_FAILED`（503，任务保存为 failed）、`INTERNAL_ERROR`（500）。错误响应只包含稳定错误码和安全说明，不泄露连接细节或底层错误。

---

## 文档

- [阶段规划](docs/PLAN.md)
- [架构说明](docs/ARCHITECTURE.md)
