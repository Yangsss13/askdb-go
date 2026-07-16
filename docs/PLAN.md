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

### 阶段 2：查询任务核心链路（计划）

- 用户提交自然语言问题 → 创建 Task 记录 → 发布到 RabbitMQ
- Worker 消费消息 → Fake LLM 生成 SQL → SQL 安全检查
- 执行只读查询（database/sql，连 askdb_demo）
- 结果写入 Redis（短期缓存）和 MySQL（最终状态）
- 用户轮询接口获取结果

---

### 阶段 3：稳定性和观测性（计划）

- Outbox 模式（防止任务发布丢失）
- 请求追踪 / 中间件日志
- 错误重试和死信队列
- Prometheus 指标

---

### 阶段 4：前端（可选）

- 简单 Web UI，提交问题并展示结果
