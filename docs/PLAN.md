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

### 阶段 4：Redis 结果缓存（当前）

**目标**：Worker 将完整查询结果写入 Redis，新增结果获取接口。

- [x] `000002_add_query_result_cache` migration：给 `query_jobs` 增加 `result_expires_at DATETIME(3) NULL`
- [x] `internal/queryresult`：`CachedQueryResult` 结构体、`RedisStore`（Set / Get）、哨兵错误（ErrResultNotFound / ErrResultCorrupted / ErrResultStoreUnavailable）
- [x] JSON 反序列化使用 `UseNumber()`，保证 int64 往返不升格为 float64
- [x] Worker 写入顺序：查询成功 → 构造 CachedQueryResult → Set Redis（带 TTL）→ 成功后 SetSucceeded（含 result_expires_at）→ ACK
- [x] Redis 写入失败：SetFailed（RESULT_CACHE_FAILED）→ ACK；SetFailed 也失败：不 ACK
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

### 阶段 5：SQL Guard 与查询资源限制（当前）

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

### 阶段 6A：用户认证、JWT 与查询任务归属（当前）

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
