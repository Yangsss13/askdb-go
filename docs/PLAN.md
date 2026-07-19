# AskDB-Go 开发计划

## 项目目标

演示 Go 后端全链路能力：HTTP API → 消息队列 → 异步 Worker → 数据库查询 → 结果缓存。

---

## 阶段规划

### 阶段 1：基础工程（已完成）

**目标**：能运行的骨架，所有基础设施连通。

- [x] Go Module 初始化
- [x] 环境变量配置 + `log/slog` 结构化日志
- [x] MySQL / Redis / RabbitMQ 连接
- [x] `GET /healthz` 和 `GET /readyz`
- [x] API 和 Worker 优雅关闭
- [x] Docker Compose（MySQL 8.0 + Redis 7 + RabbitMQ 3.13）
- [x] askdb_app 和 askdb_demo 数据库
- [x] askdb_demo 示例数据（products / orders / order_items）

**不实现**：用户/JWT、查询任务、LLM、SQL Guard、前端

---

### 阶段 2：同步查询任务与 Fake LLM（已完成）

**目标**：打通同步查询链路。本阶段暂不使用 RabbitMQ 分发业务任务，也不使用 Redis 缓存查询结果。

- [x] 版本化 SQL migration 创建 `query_jobs`（golang-migrate，Docker profile 执行）
- [x] `POST /api/v1/query-jobs`：校验问题 → 创建 pending 任务 → Fake LLM 生成固定 SQL → 只读查询 askdb_demo → 更新终态 → 同步返回结果
- [x] `GET /api/v1/query-jobs/:id`：返回持久化的任务信息（不含完整结果集）
- [x] Fake LLM：三个固定问题映射到硬编码 SELECT，用户输入不拼接进 SQL
- [x] QueryExecutor：`database/sql` + askdb_reader 只读账号查询 askdb_demo，连接池与 GORM 隔离
- [x] 任务状态机（pending → generating → executing → succeeded / failed）与稳定错误码

---

### 阶段 3：RabbitMQ 异步查询链路（已完成）

**目标**：将同步查询改造为 RabbitMQ 生产/消费的异步链路。本阶段不使用 Redis 缓存完整查询结果。

- [x] `POST /api/v1/query-jobs` 返回 HTTP 202，不再同步执行查询
- [x] API 校验问题 → 创建 pending 任务 → 条件更新 queued → 发布消息 → 返回 202
- [x] Worker 消费消息 → 读取任务 → 更新 generating → Fake LLM → 更新 executing → 查询 → 更新终态
- [x] 状态机新增 `queued` 状态，全部中间状态均持久化（5 次 DB 写入）
- [x] Repository 条件状态更新（`WHERE id=? AND status=?` + RowsAffected 检查），防止终态回退和并发覆盖
- [x] Consumer 手动 ACK：仅最终状态成功写入 MySQL 后 ACK；MySQL 写入失败时停止 Consumer、关闭 Channel 使消息重入队
- [x] Publisher 使用独立 Channel，Consumer 使用独立 Channel，Health Check Channel 保持独立
- [x] `GET /api/v1/query-jobs/:id` 可轮询任务状态；succeeded 返回 generated_sql / row_count / execution_duration_ms
- [x] API 和 Worker 优雅关闭（Publisher.Close + Consumer.Stop + 30s 超时）
- [x] 全部单元测试，`go test -race ./...` 通过

---

### 阶段 4：Redis 结果缓存（已完成）

**目标**：Worker 将完整查询结果写入 Redis，新增结果获取接口。

- [x] `000002_add_query_result_cache` migration：给 `query_jobs` 增加 `result_expires_at DATETIME(3) NULL`
- [x] `internal/queryresult`：`CachedQueryResult` 结构体、`RedisStore`（Set / Get）、哨兵错误（ErrResultNotFound / ErrResultCorrupted / ErrResultStoreUnavailable）
- [x] JSON 反序列化使用 `UseNumber()`，保证 int64 往返不升格为 float64
- [x] Worker 写入顺序：查询成功 → 构造 CachedQueryResult → Set Redis（带 TTL）→ 成功后 SetSucceeded（含 result_expires_at）→ ACK
- [x] Redis 写入失败（阶段 4 基线）：曾使用 SetFailed（RESULT_CACHE_FAILED）→ ACK；该路径已由阶段 7 的 Retry/DLQ 流程覆盖
- [x] `GET /api/v1/query-jobs/:id/result`：先查 MySQL 状态，再读 Redis；区分 410（到期）/ 503（提前丢失）/ 503（不可用）
- [x] `GET /api/v1/query-jobs/:id` succeeded 时新增 `result_expires_at`，不含完整 rows
- [x] `QUERY_RESULT_TTL` 环境变量配置缓存 TTL，默认 15m，必须 > 0
- [x] 全部单元测试（含写入顺序、错误码映射、JSON 类型往返），`go test -race ./...` 通过

**数据一致性约束**：
- MySQL 是任务状态唯一事实来源；Redis 仅作短期可丢失缓存
- 结果到期（HTTP 410）不等于任务失败；MySQL succeeded 状态不被修改
- Redis 清空或重启不会改变 MySQL 中已完成的任务状态
- Redis 写入成功但 MySQL 更新失败时，孤立 Redis Key 由 TTL 自动清理
- 不保证 Exactly Once；不实现缓存重建

**不实现**：Retry Queue、DLQ、processed_messages 幂等表、Publisher Confirm、Transactional Outbox、SQL Guard、缓存重建、结果分页、JWT、用户系统、真实 LLM、前端

---

### 阶段 5：SQL Guard 与查询资源限制（已完成）

**目标**：在 Worker 查询链路中加入 SQL Guard，所有 LLM 生成的 SQL 必须经过 AST 验证和规范化后才能执行。

- [x] Parser 技术选型：`github.com/pingcap/tidb/pkg/parser` + `test_driver`，依赖轻量（8个间接依赖，均 Apache 2.0），完整 MySQL 8.0 支持含 CTE，ADR 记录于 `docs/adr/0001-sql-parser-selection.md`
- [x] 新增 `validating` 状态，状态机更新为 `pending→queued→generating→validating→executing→succeeded`
- [x] `internal/sqlguard/`：Guard 接口、AST 访问者、表名/Schema 白名单、函数白名单、LIMIT 读写、拒绝规则（INTO/锁/变量/多语句/DDL/DML/危险函数/WITH RECURSIVE）
- [x] Guard 在 generating→validating 后运行，成功后才能 validating→executing
- [x] QueryExecutor 只接收 Guard 的 NormalizedSQL，**永远不执行原始 LLM 输出**
- [x] MySQL `generated_sql` 保存规范化后的实际执行 SQL
- [x] Guard 拒绝：SetFailed(SQL_VALIDATION_FAILED) → ACK；SetFailed 也失败：不 ACK
- [x] Guard 非拒绝运行时错误（如 ctx 取消）：原样返回，不伪装为业务失败
- [x] MAX_RESULT_BYTES 限制 Redis 写入的 JSON Payload 大小，超限：SetFailed(RESULT_TOO_LARGE) → ACK
- [x] MAX_QUERY_ROWS 替代硬编码 100，同时作为 Guard LIMIT 上限和 QueryExecutor 第二层防御
- [x] 序列化唯一一次（同时用于大小检查和 Redis 写入）
- [x] 全部单元测试（含Guard规则、状态转换、拒绝时不调用 Executor/Redis、RESULT_TOO_LARGE），`go test -race ./...` 通过
- [x] Fuzz Test：304,000+ 次执行无 panic

**SQL Guard 允许范围**：单条 SELECT / 非递归 CTE / JOIN / GROUP BY / ORDER BY / 常用聚合函数 / 子查询 / 受控 UNION / askdb_demo.{products, orders, order_items} 或不带 Schema 的同名表 / 无表的常量 SELECT

**SQL Guard 拒绝范围**：WITH RECURSIVE / 多条语句 / INSERT/UPDATE/DELETE/REPLACE/DDL/TCL / CALL/SET/USE/SHOW/EXPLAIN / SELECT...INTO / FOR UPDATE/LOCK / 用户/会话/系统变量 / 禁止的 Schema（mysql/information_schema/performance_schema/sys/askdb_app） / 非白名单表 / 函数白名单外的所有函数（含 SLEEP/BENCHMARK/LOAD_FILE/GET_LOCK 等）/ Parser 无法解析的输入

**数据一致性**：Guard 不替代 askdb_reader 只读账号，两者共同作为纵深防御。不声称 Exactly Once。

**不实现**：真实 LLM、Retry Queue、DLQ、Outbox、JWT、前端、复杂 SQL 自动修复、结果分页

---

### 阶段 6A：用户认证、JWT 与查询任务归属（已完成）

**目标**：引入用户系统，查询任务按用户隔离。注册/登录签发 JWT，受保护接口用 Bearer 中间件鉴权。

- [x] `000003_create_users` migration：`users` 表（id / email 唯一 / password_hash / 时间戳）
- [x] `000004_add_user_id_to_query_jobs` migration：`query_jobs.user_id` 可空（兼容历史行）+ 索引 + 外键 `ON DELETE RESTRICT`；down 按外键→索引→字段顺序删除
- [x] `internal/user/`：User 模型、Repository（唯一约束 1062 → 409）、AuthService（注册/登录）、AuthHandler
- [x] `internal/auth/`：JWTManager（HS256 签发与校验），API 专属，Worker 不导入
- [x] bcrypt DefaultCost 哈希；密码按字节限制 8～72，不 trim；账号不存在执行 dummy bcrypt 防枚举
- [x] JWT 身份存标准 `sub`，严格校验 `sub`/`iss`/`iat`/`exp`，仅接受 HS256
- [x] `internal/middleware/`：Bearer 中间件，依赖验证接口，缺失/过期/算法/issuer 不符均 401
- [x] `POST /api/v1/auth/register`（201/400/409）、`POST /api/v1/auth/login`（200/401）为公开接口
- [x] 三个 `query-jobs` 接口受 Bearer 保护；创建写入 `user_id`
- [x] Get/GetResult 先在 MySQL 校验归属，跨用户与历史 NULL 任务统一返回 404；GetResult 归属+状态+过期校验通过后才读 Redis
- [x] JWT 配置（`JWT_SECRET`≥32 字节 / `JWT_ISSUER` / `JWT_ACCESS_TTL`）；`JWT_SECRET` 仅 API 必需，Worker 无此校验
- [x] RabbitMQ 消息仍只含 job_id，Worker 行为不变
- [x] 全部单元测试（注册/重复邮箱/密码边界/登录成败/JWT 算法·issuer·过期·缺 iat/Bearer 缺失/本人·跨用户 404/跨用户不读 Redis），`go test -race ./...` 通过

**安全约束**：日志与响应不含密码、哈希、Token 或底层数据库错误；404 对不存在/他人/NULL 归属任务一致，避免 IDOR 探测。

**不实现**：数据源管理、RBAC、刷新 Token、OAuth、Retry Queue、DLQ、Outbox、真实 LLM、前端

---

### 阶段 6B 实施摘要

**实现内容：**

- [x] `internal/crypto`：AES-256-GCM 加密/解密，密文带 `v1:` 版本前缀，AAD 绑定数据源 ID 防止密文移植
- [x] `internal/netguard`：两阶段 IP 校验（DNS 解析 + 固定 IP 拨号），防 DNS Rebinding；`AllInAllowlist` CIDR 白名单校验
- [x] `internal/datasource`：DataSource 模型/Repository/Service，两步事务加密写入，软删除，`FOR SHARE` 删除保护锁
- [x] Worker 动态路径：`DataSourceOpener` 接口 + `dsServiceOpener` 适配器；有 `data_source_id` 时动态建连，`MaxOpenConns=1`
- [x] `000005_create_data_sources` / `000006_add_data_source_id_to_query_jobs` migration 已应用

**已知限制：**

- `RegisterDialContext` 条目随动态连接增长，不清理（连接数有界，内存可控）
- `AllowedTables` 白名单固定，暂不支持用户自定义配置
- 无密钥轮换 UI；轮换需手动重加密并更换 `DATA_SOURCE_KEY`

---

### 阶段 7：RabbitMQ Retry、DLQ 与消费者幂等（已完成）

**目标**：在不修改现有主队列声明参数的前提下，实现 Retry Queue（固定 TTL + DLX 回流）、Dead-Letter Queue、Publisher Confirm（mandatory=true）和基于 `processed_messages` 的消费者幂等协议。

- [x] Migration 000007：`query_jobs` 新增 `attempt_count`（TINYINT）、`next_retry_at`（DATETIME(3)）
- [x] Migration 000008：创建 `processed_messages` 表（PK: message_id；唯一约束: message_type+job_id；状态机: processing/retry_scheduled/completed；lease_token + lease_expires_at）
- [x] 新增 `retrying` 状态；合法迁移路径：`generating/validating/executing → retrying → generating`
- [x] 消息 Body 不变；`x-attempt`（int32）放 AMQP Header，严格校验类型/负数/溢出
- [x] API 初始发布和 Worker Retry/DLQ 发布使用独立 confirm-mode channel；`mandatory=true`；同时检查 Basic.Return 和 DeferredConfirmation；互斥序列化；超时 `MQ_CONFIRM_TIMEOUT`
- [x] Retry Queue：`askdb.query.retry`（durable, x-message-ttl, x-dead-letter-exchange → 主 Exchange/Route）；不改主队列声明
- [x] DLQ：独立 `askdb.dlq` / `askdb.query.dlq`（durable, 无 TTL/DLX）；达到最大重试次数后 Confirm DLQ，再 SetFailed
- [x] `GORMProcessedMessageRepository`：Claim（事务 + `clause.Locking{FOR UPDATE}`）、Renew、MarkRetryScheduled、MarkCompleted，均用 lease_token CAS
- [x] WorkerService 接受 `ProcessRequest`（含 MessageID、Attempt）；`isRetryableError` / `isDeterministicFailure` 基于 `errors.Is/As`
- [x] `scheduleRetryOrFail`：attempt < maxRetries → Retry Confirm → SetRetrying → ErrRetryScheduled；否则 → DLQ Confirm → SetFailed → ErrDLQScheduled
- [x] Consumer：Claim → Lease 续租（固定 `leaseTTL=30s`，每 `leaseTTL/3≈10s`）→ Process → MarkRetryScheduled/MarkCompleted → ACK；Lease 丢失 → NACK requeue；过期 Lease 可 CAS 接管
- [x] malformed/未知版本/job_id=0/无效 x-attempt/ErrJobNotFound/ClaimConflict → DLQ → ACK；DLQ confirm 失败 → NACK requeue
- [x] 配置：`MQ_CONFIRM_TIMEOUT`（默认 5s）、`RETRY_MAX_ATTEMPTS`（默认 3）、`RETRY_DELAY`（默认 30s）
- [x] ACK 前置条件：Retry/DLQ 发布 Confirm 与必要的 `query_jobs`、`processed_messages` 状态写入均成功；否则 NACK/requeue
- [x] 消息 Body 仍只含消息元数据和 `job_id`，不含问题、SQL、DSN、密码、Token 或密钥
- [x] `go test -race ./...` 通过；单元测试覆盖 Confirm/Return 失败、重复 message_id、ClaimConflict、Lease 续期/接管/失去、stale/future attempt、最大重试、malformed/未知版本、终态重投不执行、两 Worker 并发 Claim

**可靠性边界（At-Least-Once）**：
- Retry/DLQ Confirm 到 MySQL 写入之间的崩溃窗口允许重复消息；幂等层处理。
- DLQ 可能重复（DLQ Confirm 成功但 SetFailed 失败）；文档明确。
- **Transactional Outbox / Exactly-Once 留到阶段 8。**

**本阶段不实现**：Transactional Outbox、Exactly Once、真实 LLM、RBAC、前端、密钥轮换 UI

---

### 阶段 8：Transactional Outbox（已完成；At-Least-Once）

**目标**：围绕数据库事务与消息发布一致性实现 Transactional Outbox；本阶段明确不实现 Exactly Once。

- [x] Transactional Outbox：创建任务、`pending→queued` 与待发布事件写入同一 MySQL 事务
- [x] Outbox Dispatcher：API 进程后台运行，多实例 `SKIP LOCKED` 竞争、Lease 接管、优雅关闭与过期恢复
- [x] RabbitMQ Confirm、`mandatory=true`、Basic.Return、稳定 `message_id/occurred_at/payload` 与数据库指数退避
- [x] RabbitMQ 不可用时仍可提交并返回 202；Worker Retry/DLQ 保持阶段 7 语义
- [x] 仅清理保留期后的 `published` 事件，不永久丢弃未发布事件

**可靠性边界**：Confirm 成功但标记 `published` 前崩溃允许重复发布，由阶段 7 消费者幂等兜底；系统仍是 At-Least-Once，不实现 Exactly Once。真实 LLM 与前端不在本阶段范围内。
### Phase 8 implementation status: Transactional Outbox

- [x] Migration `000009_create_outbox_events`: durable event payload, unique `message_id`, status, retry time, lease token/expiry, and published retention cleanup.
- [x] One MySQL transaction creates the query job, moves `pending` to `queued`, and inserts the initial `query.execution.requested` event; rollback removes all three effects.
- [x] API submission no longer publishes RabbitMQ directly. The API can accept and return 202 while RabbitMQ is unavailable; the in-process Dispatcher reconnects asynchronously.
- [x] Dispatcher uses `FOR UPDATE SKIP LOCKED`, short claim transactions, lease-token CAS, expired `publishing` takeover, graceful shutdown, Confirm, mandatory routing, and Basic.Return handling.
- [x] Publish failure uses database `next_retry_at` with capped exponential backoff and never permanently discards an unpublished event. Published rows are cleaned in batches after `OUTBOX_PUBLISHED_RETAIN`.
- [x] Payload contains only `job_id`; `message_id` and `occurred_at` are stable on republish. Worker Retry/DLQ behavior remains Phase 7 behavior.

The end-to-end contract remains **At-Least-Once**. A crash after RabbitMQ Confirm
but before `published` can republish the same event; `processed_messages` provides
consumer idempotency. This phase does **not** implement Exactly Once, a real LLM,
or a frontend.

## Phase 9: OpenAI-compatible LLM

- [x] Provider switch: `LLM_PROVIDER=fake|openai-compatible`; API, migrations,
  and Fake mode do not require `LLM_API_KEY`; only the real Worker validates it.
- [x] Operator-only URL validation rejects userinfo, query, fragment, and all
  redirects. HTTPS is the default; explicitly enabled local HTTP requires every
  resolved address to be loopback and pins actual dials to those addresses.
- [x] The real client uses standard `net/http`, context timeout, closed and
  bounded response bodies, safe typed errors, and no sensitive payload logging.
- [x] Schema input is limited to parameterized metadata for `products`,
  `orders`, and `order_items`: name, type, nullability, and primary-key status;
  table/column/serialized-byte limits and stable ordering are enforced.
- [x] Pipeline is Schema → LLM → SQL Guard → `NormalizedSQL` → Executor;
  messages remain `job_id`-only and Phase 7 Retry/DLQ plus Phase 8 Outbox remain.
- [x] Strict response contract: exactly one choice, `finish_reason=stop`, and
  one JSON object containing only `sql`; Markdown, truncation, extra content,
  empty SQL, and oversized bodies fail closed.

Compatibility limits: non-streaming Chat Completions, MySQL dialect, and the
three allowlisted tables only. No conversation history, streaming, Tool
Calling, Embedding, model administration, or other SQL dialects. No migration
is required; all LLM settings are environment configuration.
