# AskDB-Go 开发计划

## 项目目标

演示 Go 后端全链路能力：HTTP API → 消息队列 → 异步 Worker → 数据库查询 → 结果缓存。

---

## 阶段规划

### 阶段 1：基础工程（当前）

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

### 阶段 2：同步查询任务与 Fake LLM（当前）

**目标**：打通同步查询链路。本阶段暂不使用 RabbitMQ 分发业务任务，也不使用 Redis 缓存查询结果。

- [x] 版本化 SQL migration 创建 `query_jobs`（golang-migrate，Docker profile 执行）
- [x] `POST /api/v1/query-jobs`：校验问题 → 创建 pending 任务 → Fake LLM 生成固定 SQL → 只读查询 askdb_demo → 更新终态 → 同步返回结果
- [x] `GET /api/v1/query-jobs/:id`：返回持久化的任务信息（不含完整结果集）
- [x] Fake LLM：三个固定问题映射到硬编码 SELECT，用户输入不拼接进 SQL
- [x] QueryExecutor：`database/sql` + askdb_reader 只读账号查询 askdb_demo，连接池与 GORM 隔离
- [x] 任务状态机（pending → generating → executing → succeeded / failed）与稳定错误码

**当前限制**：查询为同步执行；SQL 来自 Fake LLM，不接入真实模型；不使用消息队列，不缓存结果。

**不实现**：RabbitMQ 业务消息、Redis 结果缓存、完整 SQL Guard、SQL AST、JWT、用户系统、数据源管理、Outbox、重试、死信队列、真实 LLM、前端

---

### 阶段 3：异步链路、稳定性和观测性（计划）

- 将同步查询改造为 RabbitMQ 生产/消费的异步链路
- Redis 短期缓存查询结果
- Outbox 模式（防止任务发布丢失）

- Outbox 模式（防止任务发布丢失）
- 请求追踪 / 中间件日志
- 错误重试和死信队列
- Prometheus 指标

---

### 阶段 4：前端（可选）

- 简单 Web UI，提交问题并展示结果
