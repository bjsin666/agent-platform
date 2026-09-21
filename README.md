# agent-platform — Go 通用 Agent 运行时 + 知识库问答 / 定时报告

一个 Go 实现的轻量 **Agent 运行时框架**：支持 ReAct 循环与 DAG 工作流两种执行模式、工具注册与并发治理、上下文管理，并在其上落地 **知识库问答** 与 **定时报告** 两个真实业务场景。模型服务用 Python（FastAPI + 本地 bge-small-zh），工程层全部在 Go。

## 功能特性

- **双执行模式**：Agent 模式（ReAct / Tool-Calling 循环）与 Workflow 模式（DAG：入度调度 + 并发执行 + 失败取消 + 环检测），统一节点抽象
- **工具系统**：JSON Schema 参数校验、全局信号量限流（默认并发 3）、单工具超时 kill、幂等重试、连续失败熔断
- **上下文管理**：tiktoken token 估算、滑动窗口截断、LLM 摘要压缩（带机械摘要兜底）
- **LLM 网关**：首 token / 总超时、重试 + jitter、熔断状态机、Redis 响应缓存（实测命中后 6s → 38ms）
- **知识库 RAG**：Markdown 标题切块（递归 + overlap）、混合检索（向量余弦 + MySQL FULLTEXT + 关键词加成）、引用溯源、异步入库
- **定时报告**：cron 解析、乐观锁防重复触发、DAG 编排（并行检索 → 汇总 → LLM 生成 → webhook 通知）、执行记录与 token 用量
- **治理与可观测**：API Key 认证、Redis 令牌桶限流、trace 全链路落库（llm/tool 事件）、用量聚合
- **工具联动**：把 mini-cache（分布式缓存）包装为 `cache_get` / `cache_set` 工具，Redis 作数据源 + 读穿缓存

## 架构

```
┌──────────┐      ┌────────────────────────────────────────────┐
│ 客户端     │      │ Go Agent 服务                               │
│ Web/CLI/ │─────▶│  API 层: Gin + API Key 认证 + 令牌桶限流      │
│ API      │  SSE │  ┌────────────────────────────────────────┐ │
│          │      │  │ Agent 运行时                             │ │
│          │      │  │  执行引擎: Agent(ReAct) + Workflow(DAG)   │ │
│          │      │  │  工具系统: 注册/校验/并发/超时/熔断        │ │
│          │      │  │  上下文管理: 窗口/截断/摘要压缩            │ │
│          │      │  │  LLM 网关: 超时/重试/熔断/缓存            │ │
│          │      │  │  工具: mini-cache(cache_get/set)         │ │
│          │      │  └────────────────────────────────────────┘ │
│          │      │  持久化: MySQL(会话/知识库/任务/用量/trace)    │
│          │      │  状态:   Redis(限流/LLM 缓存)                 │
│          │      │  异步:   Scheduler + Worker(定时报告/入库)    │
└──────────┘      └───────────────┬────────────────────────────┘
                                  │ HTTP /v1/embed(批量≤32)
                          ┌───────▼────────┐
                          │ embed-service   │
                          │ FastAPI + bge-small-zh(512 维,本地) │
                          └────────────────┘
```

## 技术栈

| 层 | 技术 |
|---|---|
| Go 后端 | Go 1.25+ / Gin / GORM / go-redis v9 / log/slog / testify |
| LLM | DeepSeek（OpenAI 兼容，tool calling + stream） |
| 模型服务 | Python 3.12 / FastAPI / uvicorn / sentence-transformers / bge-small-zh |
| 数据库 | MySQL 8（业务数据 + FULLTEXT 关键词检索） |
| 缓存 | Redis（限流、LLM 响应缓存）；mini-cache（联动工具） |
| 定时 | robfig/cron/v3 + MySQL 任务表乐观锁 |

## 快速开始

### 前置依赖
- Go 1.25+、Python 3.12+、本机 MySQL 8 与 Redis 服务
- bge-small-zh 模型放到 `embed-service/models/bge-small-zh/`

### 1. 配置
复制 `.env` 并填写：

```
DEEPSEEK_API_KEY=xxx            # LLM API Key
MYSQL_PASSWORD=xxx              # 本机 MySQL 密码
EMBED_SERVICE_URL=http://127.0.0.1:8001
AGENT_API_KEY=xxx               # 可选:API 访问认证(留空则跳过)
```

### 2. 启动模型服务
```bash
cd embed-service
python -m pip install -r requirements.txt
uvicorn main:app --host 127.0.0.1 --port 8001
```

### 3. 启动 Go 服务
```bash
go run ./cmd/server
# 默认 :8080
```

### 4. 演示页(可选)

启动 Go 服务后，浏览器打开 [http://127.0.0.1:8080/demo/](http://127.0.0.1:8080/demo/)：
健康状态、知识库问答（流式）、文档管理、定时报告任务与执行记录一页可视化，无需前端构建。

### 5. 快速验证
```bash
curl http://127.0.0.1:8080/health                    # mysql/redis/embed 三项状态

# 上传知识库文档
curl -X POST http://127.0.0.1:8080/v1/kb/documents \
  -F "file=@scripts/demo-docs/产品说明.md"

# 创建会话并流式问答
curl -X POST http://127.0.0.1:8080/v1/sessions -H "Content-Type: application/json" \
  -d '{"title":"demo"}'
# 用返回的 session_id 调 /v1/sessions/{id}/chat,SSE 流式返回

# 创建每分钟执行的定时报告(console 渠道:无需外部接收端,报告写入日志与执行记录)
curl -X POST http://127.0.0.1:8080/v1/report-tasks -H "Content-Type: application/json" \
  -d '{"name":"周报","cron_expr":"*/1 * * * *","kb_ids":[1,2],"prompt_template":"生成产品周报","notify":{"type":"console"}}'
```

## Docker 部署(容器化)

前置：WSL2 或 Linux 服务器上安装 Docker（Docker Desktop 勾选 WSL2 后端，或 WSL 内 `curl -fsSL https://get.docker.com | sh`）。

```bash
# 1. 先在本机执行 go mod vendor(把本地模块 mini-cache 等依赖打进 vendor/)
go mod vendor

# 2. 确保 .env 已填 DEEPSEEK_API_KEY / MYSQL_PASSWORD

# 3. 一条命令构建并启动全栈(mysql / redis / embed / backend)
docker compose up -d --build

# 4. 浏览器打开演示页
#    http://localhost:8080/demo/
```

- 端口：容器 MySQL 映射 `3307`、Redis 映射 `6380`（本机 Windows 服务占用 3306/6379，避免冲突；停用本机服务后可改回）
- 敏感配置：`.env` 通过 `env_file` 注入容器，**不进入镜像**
- 首次构建较慢：Python 镜像要装 torch/sentence-transformers（约 2~3GB），属正常
- 查看状态：`docker compose ps`；日志：`docker compose logs -f backend`
- 停止：`docker compose down`（加 `-v` 会连数据卷一起删）

## API 一览

```
POST /v1/sessions                     创建会话
POST /v1/sessions/{id}/chat           SSE 流式问答
GET  /v1/sessions/{id}/messages       会话历史
POST /v1/kb/documents                 上传文档(异步入库)
GET  /v1/kb/documents                 文档列表
DELETE /v1/kb/documents/{id}          删除文档(级联删 chunks)
POST /v1/report-tasks                 创建定时报告任务
GET  /v1/report-tasks                 任务列表
GET  /v1/report-tasks/{id}/runs       执行记录
GET  /v1/traces/{run_id}              trace 回放
GET  /health                          健康检查
```

## 目录结构

```
cmd/server/           入口:装配依赖、启动 HTTP + Scheduler
internal/agent/       engine(ReAct+DAG) / tools / context / llm
internal/kb/          知识库:切块、混合检索、异步入库
internal/report/      定时报告:调度器、DAG 执行、webhook 通知
internal/api/         Gin 路由、认证、限流、SSE
internal/store/       MySQL(GORM) + Redis 封装
internal/trace/       trace 事件记录与落库
internal/usage/       用量聚合
internal/cachetool/   mini-cache 工具(cache_get/cache_set)
embed-service/        Python embedding 服务
scripts/loadtest/     压测脚本(真实记录,不伪造)
scripts/demo-docs/    示例知识库文档
```

## 实测数据(本机,真实记录)

| 场景 | 结果 |
|---|---|
| /health 压测(并发 20,500 请求) | QPS 1249, p95 23ms, p99 26ms |
| 创建会话(并发 5,50 请求) | QPS 4882, p95 3.9ms |
| LLM 响应缓存命中 | 相同报告生成 6s → 38ms |
| 知识库向量缓存 | 启动加载全量向量,内存余弦检索 |

## 设计要点(面试可深聊)

1. **为什么自研 Agent 运行时**：Go 生态缺成熟的 Agent 编排层(LangGraph 是 Python/JS)；需要"工作流 + Agent"混合控制；手写核心循环才能讲透内部机制
2. **工具治理**：熔断检查放在信号量之前(避免暂停工具占满并发槽)；参数校验失败不计入熔断(调用方问题)
3. **上下文压缩**：智能模式(LLM 摘要)失败自动降级机械模式(裁剪工具日志),保证链路不中断
4. **调度器防重复**：乐观锁(`WHERE next_run_at=旧值`)保证多实例/重启不重复触发
5. **混合检索取舍**：数据量千级,内存余弦 + FULLTEXT 已够;量级上来再演进向量数据库(pgvector/Milvus)
6. **mini-cache 联动**：mini-cache 是 getter 型读穿缓存(无 Set),故 Redis 作数据源 + cache_set 失效,各取其长
