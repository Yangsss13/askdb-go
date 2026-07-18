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
>
> 阶段 6A 新增 JWT 配置：`JWT_SECRET`（**仅 API 必填**，至少 32 字节）、`JWT_ISSUER`（可选，默认 `askdb-api`）、
> `JWT_ACCESS_TTL`（可选，默认 24h）。Worker **不需要也不接触** `JWT_SECRET`，未配置时仍可正常启动。
> 示例中的 secret 仅为本地开发占位，生产环境必须注入独立的高强度随机值。

### 3. 执行数据库迁移

```powershell
docker compose --profile migrate run --rm migrate
```

### 4. 启动 Worker 服务

```powershell
go run ./cmd/worker
```

### 5. 启动 API 服务（新 PowerShell 窗口）

```powershell
go run ./cmd/api
```

### 6. 验证健康状态

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

# 数据竞争检测
go test -race ./...
```

---

## 项目结构

```
cmd/api/          — API 进程入口（Gin HTTP 服务，:8080）
cmd/worker/       — Worker 进程入口（MQ 消费者）
internal/config   — 环境变量解析
internal/infra    — MySQL / Redis / RabbitMQ / 只读 askdb_demo 连接
internal/handler  — HTTP 处理器与 DTO
internal/queryjob — 查询任务模型、状态机、Repository、Service、Publisher、Consumer
internal/llm      — Fake LLM（固定问题 → 硬编码 SQL）
internal/queryexec— database/sql 只读查询与结果类型转换
internal/queryresult — Redis 结果缓存（序列化、读写、错误处理）
internal/sqlguard — SQL Guard（AST 校验、LIMIT 重写、表白名单；TiDB parser）
db/migrations/    — 版本化 SQL migration（query_jobs）
db/seed/          — Docker 容器初始化 SQL（建库、建用户、示例数据）
docs/             — 架构与阶段规划
docs/adr/         — 技术选型记录（ADR）
```

---

## 当前能力（阶段 6A）

在阶段 5 链路之上新增**用户认证与查询任务归属**：注册、登录、JWT Bearer 鉴权，查询任务按用户隔离。

打通 **RabbitMQ 异步 + SQL Guard + Redis 结果缓存**完整链路：

1. 客户端注册并登录，获取 JWT（HS256），后续携带 `Authorization: Bearer <token>` 访问受保护接口
2. API 校验 Bearer Token，从 `sub` 解析出用户 ID
3. API 接收问题，创建归属当前用户的任务，发布消息到 RabbitMQ，立即返回 **HTTP 202**
4. Worker 消费消息，调用 Fake LLM 生成 SQL
5. **SQL Guard** 通过 AST 解析验证并规范化 SQL（状态：`validating`）
6. Guard 拒绝的 SQL 直接标记为 failed，不执行查询
7. Guard 通过的 SQL 由 QueryExecutor 只读查询演示库
8. 结果序列化后检查大小限制（MAX_RESULT_BYTES），超限标记 failed
9. Worker 将完整结果写入 **Redis**（TTL 默认 15 分钟）
10. Redis 写入成功后，Worker 将 MySQL 任务更新为 succeeded，然后 ACK
11. 客户端通过 `GET /api/v1/query-jobs/:id` 轮询任务状态（仅本人任务可见）
12. 任务成功后，客户端调用 `GET /api/v1/query-jobs/:id/result` 获取完整结果（仅本人任务可读）

**MySQL 是任务状态的唯一事实来源**。Redis 仅作短期结果缓存。QueryExecutor 永远只接收 Guard 规范化后的 SQL，永远不执行原始 LLM 输出。

当前限制：

- SQL 由 **Fake LLM** 返回硬编码 SELECT，**未接入真实大模型**。
- 结果缓存到期（默认 15 分钟）后不支持重建，需重新提交任务。
- 发布消息不使用 Publisher Confirm，存在已知双写风险（见架构说明）。
- 不保证 Exactly Once 消费。
- SQL Guard 是纵深防御，不替代 askdb_reader 的数据库只读权限。
- **认证仅提供注册、登录与单一 access token（HS256）**：不支持刷新 Token、登出/吊销、RBAC 角色权限、OAuth 或第三方登录。
- Token 一经签发在有效期内始终有效，无法主动失效；`JWT_ACCESS_TTL` 默认 24h。
- `query_jobs.user_id` 对历史行为 NULL，这些遗留任务不属于任何用户，任何登录用户访问均返回 404。

Fake LLM 目前支持的固定问题：`查询所有商品`、`查询销量最高的商品`、`查询最近的订单`。

## 数据库迁移

```powershell
# 执行全部 up migration
docker compose --profile migrate run --rm migrate
```

迁移文件位于 `db/migrations/`，同时提供 up 与 down。

## 接口列表

| Method | Path | 描述 |
|---|---|---|
| GET | /healthz | 存活探针，永远 200（公开） |
| GET | /readyz | 就绪探针，依赖全部就绪返回 200，否则 503（公开） |
| POST | /api/v1/auth/register | 注册，成功 201，邮箱重复 409（公开） |
| POST | /api/v1/auth/login | 登录，成功 200 返回 JWT，凭证错误 401（公开） |
| POST | /api/v1/query-jobs | 提交自然语言问题，异步创建任务，返回 202（需 Bearer） |
| GET | /api/v1/query-jobs/:id | 轮询任务状态，succeeded 时包含 result_expires_at（需 Bearer） |
| GET | /api/v1/query-jobs/:id/result | 获取完整查询结果（columns / rows），仅 succeeded 且缓存有效时返回 200（需 Bearer） |

受保护接口需携带 `Authorization: Bearer <token>`。缺失、过期、算法非 HS256 或 issuer 不符均返回 **401**。任务按 `user_id` 隔离：访问不存在、他人或历史 NULL 归属的任务，一律返回 **404**（不区分，避免 IDOR 探测）。

认证错误码：

| HTTP | error | 含义 |
|---|---|---|
| 400 | INVALID_EMAIL | 邮箱格式非法 |
| 400 | INVALID_PASSWORD | 密码字节长度不在 8–72 |
| 409 | EMAIL_ALREADY_REGISTERED | 邮箱已注册 |
| 401 | INVALID_CREDENTIALS | 邮箱或密码错误（不区分，防枚举） |
| 401 | unauthorized | Token 缺失、过期、算法/issuer 不符 |

### 认证与任务归属（阶段 6A）

- 注册与登录为公开接口；三个 `query-jobs` 接口受 Bearer 中间件保护，缺失或非法 Token 返回 401。
- 登录成功返回 JWT（HS256，标准 `sub` 存用户 ID，含 `iss`/`iat`/`exp`）。
- 任务创建时写入 `user_id`；查询状态与结果先在 MySQL 校验归属，再读 Redis。
- **不存在的任务、他人任务、历史 `user_id=NULL` 任务对外一律返回 404**，不泄露存在性。
- 密码按字节长度限制 8～72，不做 trim；重复邮箱返回 409；凭证错误统一返回 401。
- **本阶段不支持刷新 Token、RBAC、OAuth**；Token 到期需重新登录。

### 请求示例

```powershell
# 注册（返回 201）
Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/auth/register `
  -ContentType "application/json" `
  -Body '{"email":"a@example.com","password":"pass1234"}'

# 登录取 Token（返回 200）
$login = Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/auth/login `
  -ContentType "application/json" `
  -Body '{"email":"a@example.com","password":"pass1234"}'
$token = $login.token
$headers = @{ Authorization = "Bearer $token" }

# 提交问题（返回 202，需携带 Token）
$resp = Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/query-jobs `
  -ContentType "application/json" -Headers $headers `
  -Body '{"question":"查询所有商品"}'
$jobId = $resp.job_id

# 轮询任务状态
Start-Sleep 2
Invoke-RestMethod -Uri "http://localhost:8080/api/v1/query-jobs/$jobId" -Headers $headers

# 获取完整结果
Invoke-RestMethod -Uri "http://localhost:8080/api/v1/query-jobs/$jobId/result" -Headers $headers
```

202 响应：

```json
{
  "job_id": 1,
  "status": "queued",
  "created_at": "2026-07-17T04:48:06Z"
}
```

succeeded 轮询响应（含缓存到期时间）：

```json
{
  "job_id": 1,
  "question": "查询所有商品",
  "status": "succeeded",
  "generated_sql": "SELECT id, name, ...",
  "row_count": 10,
  "execution_duration_ms": 7,
  "result_expires_at": "2026-07-17T05:03:06Z",
  "created_at": "2026-07-17T04:48:06Z",
  "finished_at": "2026-07-17T04:48:07Z"
}
```

结果接口成功响应：

```json
{
  "job_id": 1,
  "columns": ["id", "name", "category", "price", "stock", "created_at"],
  "rows": [[1, "Wireless Mouse", "Electronics", "29.99", 150, "2026-07-16T09:30:35Z"]],
  "row_count": 10,
  "cached_at": "2026-07-17T04:48:07Z",
  "expires_at": "2026-07-17T05:03:07Z"
}
```

结果接口错误码：

| HTTP | error_code | 含义 |
|---|---|---|
| 400 | INVALID_JOB_ID | ID 非法 |
| 404 | JOB_NOT_FOUND | 任务不存在 |
| 409 | RESULT_NOT_READY | 任务仍在处理中 |
| 409 | QUERY_JOB_FAILED | 任务执行失败 |
| 410 | RESULT_EXPIRED | 结果缓存已到期 |
| 503 | RESULT_UNAVAILABLE | 缓存提前丢失或 result_expires_at 为空 |
| 503 | RESULT_STORE_UNAVAILABLE | Redis 不可用 |
| 503 | RESULT_CORRUPTED | Redis 中数据损坏 |

其余错误码：`INVALID_QUESTION`（400）、`PUBLISH_FAILED`（503）、`JOB_NOT_FOUND`（404）、`UNSUPPORTED_QUESTION`（failed 任务）、`QUERY_EXECUTION_FAILED`（failed 任务）、`INTERNAL_ERROR`（500）。所有错误响应不泄露连接细节或底层错误。

---

## 文档

- [阶段规划](docs/PLAN.md)
- [架构说明](docs/ARCHITECTURE.md)

---

## 阶段 6B: 数据源管理与安全

### 新增环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DATA_SOURCE_KEY` | 必填 | base64 编码的 32 字节 AES-256-GCM 加密主密钥 |
| `ALLOWED_DB_PORTS` | `3306` | 允许连接的数据库端口，逗号分隔 |
| `PRIVATE_HOST_ALLOWLIST` | 空（拒绝私有地址） | 允许连接的私有 CIDR，Docker 开发示例：`172.17.0.0/16` |
| `DATA_SOURCE_CONNECT_TIMEOUT` | `5s` | 数据源连通性测试超时时间 |

### 新增 API 端点

| Method | Path | 描述 |
|---|---|---|
| POST | /api/v1/data-sources | 创建数据源（加密存储凭证） |
| GET | /api/v1/data-sources | 列出当前用户的数据源 |
| PUT | /api/v1/data-sources/:id | 更新数据源 |
| DELETE | /api/v1/data-sources/:id | 软删除数据源 |
| POST | /api/v1/data-sources/:id/test | 测试数据源连通性 |

### 查询任务变更

- 提交 `POST /api/v1/query-jobs` 时可携带 `data_source_id` 字段指定动态数据源。
- `data_source_id` 为 NULL 的历史任务及新任务（不传该字段），Worker 继续走静态 `readerDB` 路径，行为与阶段 6A 一致。
