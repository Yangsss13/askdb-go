# AskDB-Go 架构说明

## 整体架构

```
Browser / curl
      │
      ▼
  ┌─────────┐
  │  API    │  cmd/api  (Gin, :8080)
  │  进程   │
  └────┬────┘
       │  发布任务消息（askdb.events exchange）
       ▼
  ┌──────────────┐
  │  RabbitMQ   │  消息中间件
  └──────┬───────┘
         │  askdb.query.execution queue
         ▼
  ┌─────────┐
  │  Worker │  cmd/worker
  │  进程   │
  └────┬────┘
       │
  ┌────┴────────────────────┐
  │                         │
  ▼                         ▼
MySQL (askdb_app)       MySQL (askdb_demo)
任务状态 / 应用数据      被查询的示例数据
  │
  ▼
Redis（阶段 4 启用）
短期结果缓存
```

---

## 阶段 4 查询结果缓存流程（历史基础流程；阶段 7 已扩展）

```
POST /api/v1/query-jobs   { "question": "查询所有商品" }
      │
      ▼  （同阶段 3）
API → RabbitMQ → Worker

Worker 查询成功后：
  1. 构造 CachedQueryResult { job_id, columns, rows, row_count, cached_at, expires_at }
  2. RedisStore.Set(key="askdb:query-result:{id}:v1", value=JSON, TTL=cfg.QueryResultTTL)
     失败 → 阶段 7 的 Retry/DLQ 流程：未达上限时 Confirm Retry + SetRetrying，达到上限时 Confirm DLQ + SetFailed(MAX_RETRIES_EXCEEDED)
             发布或状态写入失败 → 不 ACK（Consumer requeue）
  3. Redis 写入成功 → repo.SetSucceeded(含 result_expires_at)
     失败 → 不 ACK（Consumer 停止，孤立 Redis Key 由 TTL 清理）
  4. MySQL 更新成功 → ACK

GET /api/v1/query-jobs/:id/result
  ① repo.FindByID（MySQL 是状态唯一来源）
  ② 状态检查（pending/queued/generating/executing → 409 RESULT_NOT_READY）
  ③ failed → 409 QUERY_JOB_FAILED
  ④ succeeded + result_expires_at IS NULL → 503 RESULT_UNAVAILABLE
  ⑤ RedisStore.Get
     命中 → 200 {job_id, columns, rows, row_count, cached_at, expires_at}
     ErrResultNotFound + now ≥ result_expires_at → 410 RESULT_EXPIRED
     ErrResultNotFound + now < result_expires_at  → 503 RESULT_UNAVAILABLE
     ErrResultStoreUnavailable                    → 503 RESULT_STORE_UNAVAILABLE
     ErrResultCorrupted                           → 503 RESULT_CORRUPTED
```

```
POST /api/v1/query-jobs   { "question": "查询所有商品" }
      │
      ▼
Handler       校验问题（非空、≤500 字符），解析请求体
      │
      ▼
Service       在 askdb_app 创建 pending 任务
      │        条件更新 pending→queued（WHERE id=? AND status='pending'）
      │        发布消息到 RabbitMQ（仅含 job_id）
      │
      ▼
Handler       返回 HTTP 202 { job_id, status="queued", created_at }

                   ↓ （异步，独立 Worker 进程）

Worker        消费 askdb.query.execution 队列
              从 askdb_app 读取任务
              条件更新 queued→generating
              Fake LLM 返回固定 SQL
              条件更新 generating→executing
              QueryExecutor 只读查询 askdb_demo
              条件更新 executing→succeeded 或 failed
              最终状态写入成功后 ACK

GET /api/v1/query-jobs/:id  → 轮询持久化的任务状态
```

## 阶段 5 Worker 流程（当前实现）

```
Worker        消费 askdb.query.execution 队列
              从 askdb_app 读取任务
              条件更新 queued→generating
              Fake LLM 返回原始 SQL（未验证）
              条件更新 generating→validating
              SQL Guard 校验 + 规范化 SQL（基于 AST，非字符串操作）
              Guard 拒绝 → SetFailed(SQL_VALIDATION_FAILED) → ACK
              条件更新 validating→executing
              QueryExecutor 只接收 Guard 的 NormalizedSQL
              序列化结果并检查 MAX_RESULT_BYTES
              超限 → SetFailed(RESULT_TOO_LARGE) → ACK
              Redis Set（带 TTL）
              MySQL SetSucceeded（含 result_expires_at，generated_sql = NormalizedSQL）→ ACK
```

分层职责（阶段 5 新增）：

| 层 | 职责 | 数据库 / 依赖 |
|---|---|---|
| Handler | HTTP 输入输出、参数校验、DTO 转换 | — |
| Service (API) | 校验、创建任务、条件更新 queued、发布消息 | askdb_app（GORM）+ RabbitMQ |
| ResultService | 读任务状态、读 Redis 结果、映射错误码 | askdb_app（GORM）+ Redis |
| WorkerService | 读任务、生成 SQL、Guard 验证、执行、大小检查、写 Redis、持久终态 | askdb_app（GORM）+ askdb_demo + Redis |
| SQLGuard | AST 解析、白名单校验、LIMIT 重写、SQL 规范化 | TiDB parser（无数据库依赖） |
| Repository | 持久化 query_job（条件更新，含 result_expires_at） | askdb_app（GORM） |
| RedisStore | JSON 序列化/反序列化，Marshal/SetRaw/Get | Redis（go-redis/v9） |
| Publisher | 序列化消息、发布到 RabbitMQ | RabbitMQ（独立 Channel） |
| Consumer | 消费消息、委托 WorkerService、ACK/NACK | RabbitMQ（独立 Channel） |
| QueryExecutor | 执行只读查询（接收 NormalizedSQL）、类型转换 | askdb_demo（database/sql, askdb_reader） |
| FakeLLMClient | 固定问题 → 硬编码 SQL | 无外部调用 |

---

## 阶段 6A：用户认证与查询归属（当前实现）

### 认证流程

```
POST /api/v1/auth/register  { email, password }
  email 去首尾空格并转小写；password 按字节长度 8–72（不 trim）
  bcrypt(DefaultCost) 哈希 → 写入 users
  唯一约束冲突（errno 1062）→ 409 EMAIL_ALREADY_REGISTERED

POST /api/v1/auth/login  { email, password }
  FindByEmail；账号不存在 → dummy bcrypt 比较（抵消时序枚举）
  凭证错误统一 → 401 INVALID_CREDENTIALS
  成功 → JWTManager.Sign → 200 { token, expires_at }

受保护接口（三个 query-jobs 路由）
  middleware.Bearer 解析 Authorization: Bearer <token>
  JWTManager.Verify：仅 HS256，严格校验 sub（正整数）/iss/iat/exp
  校验失败 → 401 unauthorized；成功 → c.Set("userID", uid)
```

### 任务归属与 IDOR 防护

```
POST /query-jobs      Service 将 userID 写入 query_jobs.user_id
GET  /query-jobs/:id  FindByID → ownsJob(callerID) → 否则 404
GET  /query-jobs/:id/result
  ① repo.FindByID（MySQL）
  ② 归属校验 ownsJob → 否则 404（Redis 未触碰）
  ③ 状态校验（非终态 409 / failed 409）
  ④ result_expires_at 校验
  ⑤ 通过后才 RedisStore.Get
```

不存在的任务、他人任务、历史 `user_id=NULL` 任务对外一律返回 **404**，不区分，防止存在性探测。**Redis 读取严格晚于 MySQL 归属校验。**

### JWT 与配置边界

- `TokenManager`（签发）在 user 包声明，`Verifier`（校验）在 middleware 包声明，均由 `auth.JWTManager` 实现。
- JWT 配置：`JWT_SECRET`（≥32 字节）、`JWT_ISSUER`（默认 askdb-api）、`JWT_ACCESS_TTL`（默认 24h）。
- **JWT 是 API 专属**：`config.Load()` 不校验 JWT，由 API 的 `cfg.ValidateJWT()` 单独校验。**Worker 不接触也不需要 JWT_SECRET**。
- `query_jobs.user_id` 为可空列（兼容历史行），带索引与 `ON DELETE RESTRICT` 外键引用 `users(id)`。

### users 表分层职责

| 层 | 职责 | 依赖 |
|---|---|---|
| AuthHandler | 注册/登录 HTTP、错误码映射（400/401/409） | — |
| AuthService | email 规范化、密码校验、bcrypt、dummy 比较、签发 | user Repository + TokenManager |
| user.Repository | 持久化 users，唯一冲突映射 ErrDuplicateEmail | askdb_app（GORM） |
| auth.JWTManager | HS256 签发与严格校验 | golang-jwt/jwt/v5 |
| middleware.Bearer | 解析 Bearer、校验、注入 userID | Verifier |

**本阶段不实现**：刷新 Token、RBAC、OAuth。Token 到期需重新登录。

---

## RabbitMQ Topology

| 项 | 值 |
|---|---|
| Exchange | `askdb.events`（direct, durable） |
| Queue | `askdb.query.execution`（durable） |
| Routing Key | `query.execution.requested` |
| Consumer Tag | `worker-query-consumer` |
| Prefetch | 1 |
| Delivery Mode | Persistent |
| Auto ACK | false |

Publisher 和 Consumer 均在启动时幂等声明相同 Topology。

---

## 任务状态机（阶段 5）

```
pending → queued → generating → validating → executing → succeeded
   ↘        ↘          ↘             ↘            ↘
                                                        failed
```

所有中间状态均持久化（共 6 次 DB 写入）。Repository 所有状态更新使用 `WHERE id=? AND status=?` 条件更新并检查 RowsAffected，防止终态回退和并发覆盖。

---

## 阶段 7：RabbitMQ Retry、DLQ 与消费者幂等

### 任务状态机（阶段 7 扩展）

```
pending → queued → generating → validating → executing → succeeded
   ↘        ↘          ↘             ↘            ↘
                    retrying ←────────────────────────
                        ↓（attempt 匹配后重新进入 generating）
                        └→ generating（下一次 attempt）
                   失败或达上限 →  failed
```

`retrying` 是**非终态**暂存状态，job 停在此状态直到 Retry Queue TTL 到期、消息经 DLX 回流主队列、Worker 重新消费。

### RabbitMQ 拓扑（阶段 7 新增，不改现有队列声明参数）

| 项 | 值 |
|---|---|
| **主 Exchange** | `askdb.events`（direct, durable）— 未改动 |
| **主 Queue** | `askdb.query.execution`（durable）— 未改动 |
| **主 Routing Key** | `query.execution.requested`— 未改动 |
| Retry Exchange | `askdb.retry`（direct, durable） |
| Retry Queue | `askdb.query.retry`（durable, `x-message-ttl=RETRY_DELAY` 转换后的毫秒值, `x-dead-letter-exchange=askdb.events`, `x-dead-letter-routing-key=query.execution.requested`） |
| Retry Routing Key | `query.execution.retry` |
| DLQ Exchange | `askdb.dlq`（direct, durable） |
| DLQ Queue | `askdb.query.dlq`（durable, 无 TTL/DLX） |
| DLQ Routing Key | `query.execution.dlq` |

Retry Queue TTL 到期 → DLX 路由至 `askdb.events` → 已有 binding 投入主队列，**不修改主队列声明参数**，避免 `PRECONDITION_FAILED`。

### Publisher Confirm（Outbox Dispatcher + Worker Retry/DLQ 发布）

所有发布均使用**独立 confirm-mode channel**（`ch.Confirm(false)`），启用 `mandatory=true` 并同时监听：

- `Basic.Return`：消息无法路由时触发，broker 随后发送 Nack。
- `DeferredConfirmation.Done()`：broker ACK 后关闭。
- 超时：`MQ_CONFIRM_TIMEOUT`（默认 5s）。

互斥锁（`sync.Mutex`）序列化同一 Channel 上的并发发布，避免多 goroutine 串扰。Confirm/Return 失败时不 ACK 原消息。

### 消息结构（Body 不变，attempt 放 AMQP Header）

```json
{
  "message_id": "<16字节随机hex，重试保留原值>",
  "type": "query.execution.requested",
  "version": 1,
  "occurred_at": "2026-07-19T...",
  "payload": { "job_id": 42 }
}
AMQP Header: x-attempt = int32（从 0 开始；初始发布无此 Header，消费时解析为 0）
```

**Body 中永远不含 question、SQL、DSN、密码、Token 或密钥；只携带消息元数据和 `job_id`。**

### 消费者幂等协议（processed_messages）

`processed_messages` 表提供**消息级幂等**，配合 `query_jobs` 的 CAS 状态更新提供**业务级幂等**。

| 字段 | 说明 |
|---|---|
| `message_id` | PK，标识具体的消息副本 |
| `message_type` + `job_id` | 联合唯一约束，拦截同一 job 的重复发布 |
| `status` | `processing` / `retry_scheduled` / `completed` |
| `lease_token` | 不可猜测随机串，所有写操作均校验此字段，防止旧 Worker 覆盖新 Worker |
| `lease_expires_at` | 代码固定 30s 租约；Worker 每 `leaseTTL/3`（约 10s）续租；到期后其他 Worker 可 CAS 接管；当前没有对应环境变量 |

**Claim 协议**（事务 + `FOR UPDATE`）：

1. 不存在 → INSERT（ClaimGranted）
2. `status=completed` → ClaimAlreadyDone → ACK（不重复执行）
3. `status=retry_scheduled` → UPDATE 为 processing（ClaimResumed）→ 继续处理
4. `status=processing`, lease 未过期 → ClaimLeaseHeld → 同 attempt 发往 Retry Queue（不增加 attempt）→ ACK
5. `status=processing`, lease 已过期 → CAS 更换 lease_token（ClaimTakenOver）→ 继续处理
6. 不同 message_id 同 job_id → ClaimConflict → DLQ → ACK

**租约续期**：处理期间每 `leaseTTL/3`（≈10s）续租；失去租约（ErrLeaseLost）时取消 ctx、设置 leaseLost 标志，处理返回后 NACK requeue。

### ACK/NACK/DLQ 决策矩阵

| 场景 | 决策 |
|---|---|
| 成功 → MarkCompleted 成功 | ACK |
| MarkCompleted 失败 | NACK requeue |
| ErrRetryScheduled（WorkerService 已发 Retry + SetRetrying）→ MarkRetryScheduled 成功 | ACK |
| MarkRetryScheduled 失败 | NACK requeue |
| ErrDLQScheduled（WorkerService 已发 DLQ + SetFailed）→ MarkCompleted 成功 | ACK |
| ErrJobNotFound → DLQ confirm 成功 → MarkCompleted 成功 | ACK |
| DLQ confirm 失败（任何来源） | NACK requeue |
| malformed / 未知版本 / job_id=0 / 无效 x-attempt → DLQ confirm 成功 | ACK |
| ClaimAlreadyDone | ACK（不执行） |
| ClaimLeaseHeld → Retry confirm 成功（同 attempt） | ACK |
| ClaimLeaseHeld → Retry confirm 失败 | NACK requeue |
| ClaimConflict → DLQ confirm 成功 | ACK |
| Lease 丢失（leaseLost=true） | NACK requeue |
| MySQL 终态写入失败 | NACK requeue（绝不 ACK） |

### 可靠性保证与已知限制

- **At-Least-Once**：Retry/DLQ Confirm 到 MySQL 更新之间崩溃会产生重复消息；`processed_messages` 幂等层处理重复。
- **DLQ 可能重复**：DLQ Confirm 成功但 SetFailed 失败时，消息 NACK requeue，下次消费会再发一次 DLQ。DLQ 消费者必须能处理重复消息。
- **Transactional Outbox**：阶段 8 已实现；Confirm 后标记 `published` 前的崩溃窗口仍允许重复发布。
- **Exactly-Once**：未实现；阶段 7 `processed_messages` 仅提供消费幂等兜底。
- **重试时重新执行完整 pipeline**：每次 retry 从 `generating`（LLM）重新开始，不从中断点续接。
- **context.DeadlineExceeded**（查询超时）视为可重试错误；`context.Canceled`（租约丢失/优雅停机）通过 publish 失败路径 NACK，不会产生无限重试。

---

## 关键设计决策

### 1. 两个数据库，两种访问方式

| 数据库 | 用途 | Go 访问方式 | 原因 |
|---|---|---|---|
| askdb_app | 应用数据（任务、日志） | GORM | 结构已知，ORM 提升开发效率 |
| askdb_demo | 被查询的演示数据 | database/sql | 动态 SQL，不能用 ORM 映射 |

### 2. Redis 不是任务状态来源

Redis 保存短期结果缓存（TTL 默认 15 分钟，由 `QUERY_RESULT_TTL` 配置）。任务最终状态始终写入 MySQL。Redis 被清空或重启不会改变 MySQL 中已完成的任务状态。结果到期（HTTP 410）不等于任务失败。

### 3. 一个 API 进程 + 一个 Worker 进程

两个进程共享同一个代码仓库（模块化单体），但独立部署、独立扩容。

### 4. 优雅关闭顺序

**API 进程：**
```
收到 SIGTERM
  → HTTP Server.Shutdown（最多 15s）
  → Publisher.Close()（关闭 Publisher Channel）
  → mq.Close()（Health Channel + Connection）
  → Redis.Close()
  → ReaderDB.Close()
  → MySQL.Close()
  → 退出 0
```

**Worker 进程：**
```
收到 SIGTERM
  → Consumer.Stop()（Channel.Cancel → wg.Wait → Channel.Close，最多 30s）
  → mq.Close()（Health Channel + Connection）
  → Redis.Close()
  → ReaderDB.Close()
  → MySQL.Close()
  → 退出 0
```

### 5. ACK 规则

只有必要的 RabbitMQ 发布 Confirm（初始任务、Retry 或 DLQ）以及对应的 MySQL 状态写入成功后才 ACK。Retry 路径要求 `SetRetrying` 与 `MarkRetryScheduled` 成功；DLQ 路径要求 `SetFailed` 与 `MarkCompleted` 成功。任一必要步骤失败则 NACK/requeue，使消息不会在状态未持久化时被确认。

| 场景 | 处理 |
|---|---|
| 消息格式错误 | Nack(no-requeue) |
| type/version 不支持 | Nack(no-requeue) |
| job_id 为 0 | Nack(no-requeue) |
| job_id 不存在 | Nack(no-requeue) |
| 任务已是终态（重复消息） | Ack（不重复执行） |
| 业务失败（LLM/执行失败） | 持久化 failed 后 Ack |
| Redis 写入失败（阶段 7） | 未达上限：Confirm Retry + SetRetrying 后 ACK；达到上限：Confirm DLQ + SetFailed(MAX_RETRIES_EXCEEDED) 后 ACK；任一步失败则 NACK requeue |
| Redis 成功但 MySQL succeeded 失败（阶段 4） | 不 ACK，孤立 Redis Key 由 TTL 清理 |
| Guard 拒绝（阶段 5） | SetFailed(SQL_VALIDATION_FAILED) → ACK；SetFailed 也失败 → 不 ACK |
| 结果超限（阶段 5）| SetFailed(RESULT_TOO_LARGE) → ACK；SetFailed 也失败 → 不 ACK |
| MySQL 写入失败 | 停止 Consumer，不 ACK |

### 6. 安全设计

- root 账号只在 Docker 容器初始化时使用
- askdb_app 用户只能访问 askdb_app
- askdb_reader 用户只能 SELECT askdb_demo
- DSN、密码、RabbitMQ URL 不出现在日志中
- 消息信封只含 job_id 和元数据，不含 question / SQL / DSN / 密码 / Token / 密钥
- /readyz 的错误信息不泄露连接细节
- 密码经 bcrypt 哈希存储，password_hash 不出现在日志或响应中
- JWT_SECRET / Token 不出现在日志中；底层数据库错误不透出到客户端
- Worker 不接触 JWT_SECRET（API 专属校验）

---

## 已知风险（阶段 4）

以下风险在当前阶段存在，将在后续可靠性阶段修复：

| 风险 | 场景 | 修复阶段 |
|---|---|---|
| 双写不一致 | `queued` 与初始事件在同一事务，提交后由 Dispatcher 发布 | 阶段 8 已修复 |
| Publisher Confirm 缺失 | Dispatcher 使用 Confirm、mandatory 与 Basic.Return | 阶段 8 已修复 |
| 消息重复投递 | Worker 处理完毕但 ACK 前崩溃，消息重新投递，产生孤立 Redis Key | 阶段 7（幂等表） |
| 孤立 Redis Key | Redis 写入成功但 MySQL succeeded 更新失败，消息重投时再次写 Redis | TTL 自动清理（约 15 分钟） |
| 无 DLQ / Retry Queue | 消息解析失败后直接丢弃（Nack no-requeue） | 阶段 7 |
| 结果不可重建 | 缓存到期后需重新提交任务才能取回结果 | 阶段 5+ |

**不声称实现 Exactly Once 语义。**

---

## 目录结构说明

```
cmd/           — 进程入口，只做启动/关闭编排，不含业务逻辑
  api/         — HTTP API 进程（Gin, :8080）
  worker/      — MQ 消费者进程
internal/      — 包内部实现，外部不可直接引用
  config/      — 所有配置集中一处，main 调用一次；ValidateJWT 仅 API 调用
  infra/       — 基础设施连接，每个文件对应一个外部依赖
  handler/     — HTTP 处理器，依赖通过参数注入（无全局变量）
  auth/        — JWT 签发与校验（HS256，API 专属，Worker 不导入）
  user/        — User 模型、Repository、AuthService、AuthHandler
  middleware/  — Bearer 鉴权中间件
  queryjob/    — 查询任务模型、状态机、Repository、Service、Publisher、Consumer
  llm/         — Fake LLM（固定问题 → 硬编码 SQL）
  queryexec/   — database/sql 只读查询与类型转换
  queryresult/ — Redis 结果缓存（CachedQueryResult、RedisStore、错误哨兵）
  sqlguard/    — SQL Guard（AST 校验、LIMIT 重写、白名单规则；依赖 TiDB parser）
db/seed/       — SQL 脚本，只在 Docker 初始化时运行一次
db/migrations/ — 版本化 SQL migration（query_jobs）
docs/          — 文档，不影响编译
```

---

## 阶段 6B 新增组件

### internal/crypto

AES-256-GCM 对称加密，密文格式 `v1:<base64(nonce+ciphertext)>`。AAD 绑定数据源 ID，防止密文在不同记录间移植。

### internal/netguard

两阶段 IP 校验：先 DNS 解析所有 A/AAAA 记录，再用固定 IP 拨号（绕过系统 Resolver），防止 DNS Rebinding 攻击。`AllInAllowlist` 校验所有解析地址均在配置的 CIDR 白名单内。

### internal/datasource

| 层 | 职责 |
|---|---|
| DataSource 模型 | DSN 加密字段、软删除（deleted_at）、所属用户 |
| Repository | CRUD + 软删除 + `FOR SHARE` 锁防删除竞争 |
| Service | 两步事务：先持久化占位行获取 ID，再加密（AAD=ID）更新密文 |

### Worker 动态路径

```
job.DataSourceID != nil
  → DataSourceOpener.Open(ctx, dataSourceID) 动态建连（MaxOpenConns=1）
  → QueryExecutor 使用动态连接执行查询
  → 连接在本次任务处理完后关闭

job.DataSourceID == nil → 沿用静态 readerDB 路径（与阶段 6A 一致）
```

`DataSourceOpener` 为接口，`dsServiceOpener` 为生产适配器，测试可注入 mock。
## Phase 8: Transactional Outbox and Dispatcher

This section supersedes the historical API-direct-publish flow shown above.

`POST /api/v1/query-jobs` validates ownership, then executes one MySQL
transaction: insert `query_jobs` as `pending`, conditionally transition it to
`queued`, and insert the matching `outbox_events` row. The row stores a unique
`message_id`, `event_type`, version, `occurred_at`, and JSON `payload` containing
only `job_id`. Any transaction error rolls back the job, status, and event.

The API does not publish the initial event in the request goroutine. An
in-process Dispatcher in every API instance claims due `pending` rows and
expired `publishing` rows with `FOR UPDATE SKIP LOCKED`, sets a fresh
`lease_token`/`lease_expires_at`, commits, and publishes outside the claim
transaction. Active leases are not claimable; expired leases are taken over and
old tokens fail the final CAS. RabbitMQ connectivity is lazy/reconnectable, so
successful database commits still return 202 while `/readyz` returns 503.

The Dispatcher uses the existing `mandatory=true`, Basic.Return, and Publisher
Confirm path. Only a successful Confirm is followed by CAS `published`; failures
clear the lease and schedule capped exponential backoff through `next_retry_at`.
It does not use the Worker Retry Queue. A crash after Confirm and before the CAS
can duplicate the stable message; Phase 7 `processed_messages` prevents duplicate
execution. The system is At-Least-Once, not Exactly Once. Shutdown stops new
claims, waits for in-flight work, and leaves recoverable leases for takeover.

## Phase 9: OpenAI-compatible LLM

The Worker selects `fake` or `openai-compatible` from `LLM_PROVIDER`. API and
migration processes do not construct an LLM client and do not validate
`LLM_API_KEY`; Fake mode performs no network call. The real Worker validates
the key and the operator-provided URL. URL userinfo, query, fragment, and all
redirects are rejected. HTTPS is required unless local HTTP is explicitly
enabled; then all DNS results must be loopback and the transport pins its
dials to those validated IPs to prevent DNS rebinding.

The real client sends standard non-streaming Chat Completions requests with a
fixed MySQL system prompt. The question is delimited as untrusted content.
Responses are bounded and closed, must have exactly one choice with
`finish_reason=stop`, and message content must be exactly one JSON object with
only `sql`. No Markdown, extra fields, trailing data, empty SQL, truncation,
or oversized body is accepted. Errors are typed and classified with
`errors.Is/As`; network/timeout/429/5xx retry, while 401/403, other 4xx and
invalid output fail deterministically. Logs and error summaries exclude keys,
Authorization, question, prompt, response body, raw SQL, and DSNs.

Before LLM generation, Worker reads only current-database metadata for the
fixed tables `products`, `orders`, and `order_items`: column name, type,
nullability, and primary-key status. Queries are parameterized, stable and
bounded. The mandatory execution pipeline is:

`Schema → LLM → SQL Guard → NormalizedSQL → Executor`

Only `NormalizedSQL` is persisted. The read-only database account remains a
separate security boundary. Retry/DLQ and Outbox payloads remain `job_id`-only;
their Phase 7/8 At-Least-Once semantics are unchanged. No migration is needed.

Known compatibility limits are non-streaming Chat Completions, MySQL syntax,
the three allowlisted tables, and environment-only provider configuration.
