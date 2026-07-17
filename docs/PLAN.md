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

### 阶段 3：RabbitMQ 异步查询链路（当前）

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

**当前限制**：
- 完整查询结果（columns / rows）不持久化，不缓存；轮询只能获得元数据。
- 发布消息不使用 Publisher Confirm；`PublishWithContext` 返回 nil 不等于 Broker 已持久化确认。
- 存在已知双写风险（见架构说明）。
- 无 Retry Queue、DLQ、幂等消费表、Transactional Outbox。

**不实现**：Redis 结果缓存、Retry Queue、DLQ、processed_messages 幂等表、Publisher Confirm、Transactional Outbox、SQL Guard、JWT、用户系统、数据源管理、真实 LLM、前端

---

### 阶段 4：Redis 结果缓存（计划）

- Worker 将完整查询结果（columns + rows）写入 Redis，TTL 5 分钟
- GET /api/v1/query-jobs/:id 在 succeeded 时附带完整结果

---

### 阶段 5：前端（可选）

- 简单 Web UI，提交问题并展示结果
