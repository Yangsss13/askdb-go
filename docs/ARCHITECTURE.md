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

## 阶段 3 异步查询流程（当前实现）

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

分层职责：

| 层 | 职责 | 数据库 / 依赖 |
|---|---|---|
| Handler | HTTP 输入输出、参数校验、DTO 转换 | — |
| Service (API) | 校验、创建任务、条件更新 queued、发布消息 | askdb_app（GORM）+ RabbitMQ |
| WorkerService | 读任务、生成 SQL、执行查询、持久化终态 | askdb_app（GORM）+ askdb_demo（database/sql）|
| Repository | 持久化 query_job（条件更新） | askdb_app（GORM） |
| Publisher | 序列化消息、发布到 RabbitMQ | RabbitMQ（独立 Channel） |
| Consumer | 消费消息、委托 WorkerService、ACK/NACK | RabbitMQ（独立 Channel） |
| QueryExecutor | 执行只读查询、类型转换 | askdb_demo（database/sql, askdb_reader） |
| FakeLLMClient | 固定问题 → 硬编码 SQL | 无外部调用 |

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

## 任务状态机（阶段 3）

```
pending → queued → generating → executing → succeeded
   ↘        ↘          ↘            ↘
                                          failed
```

所有中间状态均持久化（共 5 次 DB 写入）。Repository 所有状态更新使用 `WHERE id=? AND status=?` 条件更新并检查 RowsAffected，防止终态回退和并发覆盖。

---

## 关键设计决策

### 1. 两个数据库，两种访问方式

| 数据库 | 用途 | Go 访问方式 | 原因 |
|---|---|---|---|
| askdb_app | 应用数据（任务、日志） | GORM | 结构已知，ORM 提升开发效率 |
| askdb_demo | 被查询的演示数据 | database/sql | 动态 SQL，不能用 ORM 映射 |

### 2. Redis 不是唯一状态来源

Redis 保存短期结果缓存（TTL 5分钟，阶段 4 启用）。任务最终状态始终写入 MySQL。

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

只有任务最终状态成功写入 MySQL 后才 ACK。MySQL 写入失败时停止 Consumer 并关闭 Channel，使未 ACK 消息由 RabbitMQ 重新入队。

| 场景 | 处理 |
|---|---|
| 消息格式错误 | Nack(no-requeue) |
| type/version 不支持 | Nack(no-requeue) |
| job_id 为 0 | Nack(no-requeue) |
| job_id 不存在 | Nack(no-requeue) |
| 任务已是终态（重复消息） | Ack（不重复执行） |
| 业务失败（LLM/执行失败） | 持久化 failed 后 Ack |
| MySQL 写入失败 | 停止 Consumer，不 ACK |

### 6. 安全设计

- root 账号只在 Docker 容器初始化时使用
- askdb_app 用户只能访问 askdb_app
- askdb_reader 用户只能 SELECT askdb_demo
- DSN、密码、RabbitMQ URL 不出现在日志中
- 消息信封只含 job_id 和元数据，不含 question / SQL / 凭证
- /readyz 的错误信息不泄露连接细节

---

## 已知风险（阶段 3）

以下风险在当前阶段存在，将在后续可靠性阶段修复：

| 风险 | 场景 | 修复阶段 |
|---|---|---|
| 双写不一致 | `queued` 写入成功后 API 在 Publish 前崩溃，任务停留在 `queued` 但无消息 | 阶段 8（Outbox） |
| Publisher Confirm 缺失 | `PublishWithContext` 返回 nil 不等于 Broker 已持久化确认 | 后续可靠性阶段 |
| 消息重复投递 | Worker 处理完毕但 ACK 前崩溃，消息重新投递（只读查询天然幂等，可接受） | 阶段 7（幂等表） |
| 无 DLQ / Retry Queue | 消息解析失败后直接丢弃（Nack no-requeue） | 阶段 7 |

**不声称实现 Exactly Once 语义。**

---

## 目录结构说明

```
cmd/           — 进程入口，只做启动/关闭编排，不含业务逻辑
  api/         — HTTP API 进程（Gin, :8080）
  worker/      — MQ 消费者进程
internal/      — 包内部实现，外部不可直接引用
  config/      — 所有配置集中一处，main 调用一次
  infra/       — 基础设施连接，每个文件对应一个外部依赖
  handler/     — HTTP 处理器，依赖通过参数注入（无全局变量）
  queryjob/    — 查询任务模型、状态机、Repository、Service、Publisher、Consumer
  llm/         — Fake LLM（固定问题 → 硬编码 SQL）
  queryexec/   — database/sql 只读查询与类型转换
db/seed/       — SQL 脚本，只在 Docker 初始化时运行一次
db/migrations/ — 版本化 SQL migration（query_jobs）
docs/          — 文档，不影响编译
```
