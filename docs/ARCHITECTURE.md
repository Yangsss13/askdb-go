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
       │  发布任务消息（阶段 2）
       ▼
  ┌──────────────┐
  │  RabbitMQ   │  消息中间件
  └──────┬───────┘
         │
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
Redis
短期结果缓存
```

---

> 说明：上图是项目最终形态（含 RabbitMQ 异步链路和 Redis 缓存）。
> **当前（阶段 2）为同步查询**，尚未接入消息队列和结果缓存，见下节。

---

## 阶段 2 同步查询流程（当前实现）

阶段 2 在 API 进程内同步完成整个查询，不经过 RabbitMQ，也不缓存结果：

```
POST /api/v1/query-jobs   { "question": "查询所有商品" }
      │
      ▼
Handler       校验问题（非空、≤500 字符），解析请求体
      │
      ▼
Service       在 askdb_app 创建 pending 任务（GORM）
      │        ├─ Fake LLM 按固定问题返回硬编码 SELECT（含 LIMIT 100）
      │        └─ QueryExecutor 用 askdb_reader 只读查询 askdb_demo（database/sql）
      │
      ▼
Service       更新任务终态：succeeded（行数、耗时、finished_at）
      │                     或 failed（稳定 error_code + 安全 error_message）
      ▼
Handler       同步返回结果 DTO（columns + rows + 元数据）

GET /api/v1/query-jobs/:id  → 返回持久化的任务信息（不含完整结果集）
```

分层职责：

| 层 | 职责 | 数据库 / 依赖 |
|---|---|---|
| Handler | HTTP 输入输出、参数校验、DTO 转换 | — |
| Service | 编排流程、状态推进、错误归类 | 依赖 Repository / LLMClient / QueryExecutor |
| Repository | 持久化 query_job | askdb_app（GORM） |
| QueryExecutor | 执行只读查询、类型转换、行数上限 | askdb_demo（database/sql, askdb_reader） |
| FakeLLMClient | 固定问题 → 硬编码 SQL | 无外部调用 |

设计约束：接口定义在消费侧（Repository / LLMClient / QueryExecutor 在 Service 侧声明）；GORM 模型不直接作为 HTTP 响应；无全局可变状态；依赖通过参数注入，不使用 DI 框架。

**任务状态机**：`pending → generating → executing → succeeded`，任一处理中状态可转 `failed`。为减少同步链路的数据库往返，只在创建时写 `pending`、结束时写终态两次；中间状态在代码中建模并单元测试，终态必须设置 `finished_at`，成功须设置 `row_count` 和 `execution_duration_ms`，失败须设置稳定 `error_code`。

---

## 关键设计决策

### 1. 两个数据库，两种访问方式

| 数据库 | 用途 | Go 访问方式 | 原因 |
|---|---|---|---|
| askdb_app | 应用数据（任务、日志） | GORM | 结构已知，ORM 提升开发效率 |
| askdb_demo | 被查询的演示数据 | database/sql | 动态 SQL，不能用 ORM 映射 |

### 2. Redis 不是唯一状态来源

Redis 保存短期结果缓存（TTL 5分钟）。任务最终状态始终写入 MySQL。
用户轮询时先查 Redis，缓存未命中再查 MySQL。这样即使 Redis 重启，任务不会丢失。

### 3. 一个 API 进程 + 一个 Worker 进程

两个进程共享同一个代码仓库（模块化单体），但独立部署、独立扩容。
不拆微服务是为了降低运维复杂度，同时保持进程间职责清晰。

### 4. 优雅关闭顺序

```
收到 SIGTERM
  → 停止接受新请求（HTTP: Shutdown with timeout）
  → 等待进行中的请求/消费完成（最长 15s）
  → 关闭 RabbitMQ channel → connection
  → 关闭 Redis
  → 关闭 MySQL（底层 *sql.DB）
  → 退出 0
```

### 5. 安全设计（阶段 1）

- root 账号只在 Docker 容器初始化时使用，应用不用 root
- askdb_app 用户只能访问 askdb_app
- askdb_reader 用户只能 SELECT askdb_demo
- DSN 和密码不出现在日志中
- .env 被 .gitignore 忽略
- /readyz 的错误信息不泄露连接细节

---

## 目录结构说明

```
cmd/           — 进程入口，只做启动/关闭编排，不含业务逻辑
internal/      — 包内部实现，外部不可直接引用
  config/      — 所有配置集中一处，main 调用一次
  infra/       — 基础设施连接，每个文件对应一个外部依赖
  handler/     — HTTP 处理器，依赖通过参数注入（无全局变量）
db/seed/       — SQL 脚本，只在 Docker 初始化时运行一次
docs/          — 文档，不影响编译
```
