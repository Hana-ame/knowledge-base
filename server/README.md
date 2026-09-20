# Knowledge Base Multi-Portal Server (`kb-server`)

轻量级、零依赖（纯 Go 标准库）、高性能的 **知识库多入口 HTTP 服务**。

实现 **「单一真实源底层存储（Single Master KB） + 多入口切片暴露（Multi-Portal Slices） + S3 风格双模式分桶（Dual Bucketing）」** 的现代知识库服务架构。

---

## 📑 目录规范与架构理念

### 1. 单一真实源（Single Source of Truth）
* **底层知识库物理唯一**：所有 Markdown 知识笔记统一存放在中央 `notes/` 目录中，保持最低的维护与写入成本，避免多份知识副本不同步。
* **全局全文检索**：支持跨全库所有笔记的一次性全局秒级搜索（`GET /api/search?q=...`）。

### 2. 多入口切片独立暴露（Multi-Portal Architecture）
* 服务端可同时挂载任意数量的独立入口（Portal）。
* 每个入口在文件系统中对应 `portals/<portal-name>/` 下的一个独立子目录，包含 4 个核心规范组件：
  1. **`filelist.txt`（白名单范围）**：定义当前入口允许暴露给外部的文件相对路径清单（支持通配符，如 `notes/proj-*.md`、`notes/agent-*.md`）。
  2. **`INDEX.md`（专属索引）**：针对该入口暴露的文件子集精简修订的导航索引。
  3. **`AGENTS.md`（行为守则指南）**：当前入口专属的 Agent 交互规范、提问纪律与 RAG 操作约束。
  4. **`uploads/`（独立经验沉淀）**：该入口专用的上传保存目录，外部 Agent 回传的实测踩坑记录与知识纠错均独立落盘在此目录中。
  5. *(可选)* **`domain.txt`（专属域名绑定）**：为当前入口指定独立绑定的主机域名（如 `docs.company.local`）。

---

## 🛡️ 核心安全与隔离规范

### 1. 严格白名单边界（Strict Whitelist Boundary）
> **规则：只有路径严格命中 `filelist.txt` 时，才允许读取全文；未列入的文件一律返回 `403 Forbidden`。**
* 即使外部 Agent 猜测到了主库其他敏感笔记的相对路径（如 `notes/secret.md`），只要未在当前入口的 `filelist.txt` 中登记，服务端将坚决阻断并返回标准 403 错误：
  ```json
  {
    "error": "Forbidden",
    "message": "Access denied: path 'notes/secret.md' does not match 'agent-guidelines/filelist.txt'. Full text cannot be served.",
    "portal": "agent-guidelines",
    "requested": "notes/secret.md"
  }
  ```
* 严密防御任何 `../` 路径跨目录穿透攻击。

### 2. RAG 检索边界隔离（Scoped RAG Boundary）
* 当通过入口发起 RAG 知识检索（`GET /{portal}/rag?q=...`）时，服务端在**打开文件读取前**即执行白名单过滤。
* 只有属于当前入口 `filelist.txt` 范围内的文件才会被扫描和切片，**绝不发生未授权知识外溢进 LLM 上下文的安全事故**。

### 3. 数据与源码分离纪律
* 编译二进制（`kb-server`、`*.exe`）及打包归档严禁进入 Git。
* 私有知识库笔记（`notes/`）、索引（`indexes/`）及入口私有数据目录严禁进入公共 Git 仓库，仅推送 Go 服务核心源码与 CI 配置。

---

## 🪣 S3 风格双模式分桶（Dual Bucketing）

支持类似 AWS S3 的两种访问路由，对人类开发者与自动化 Agent 均极其友好：

### 模式 A：Path 风格分桶（Path-Style Bucketing）
通过 URL 第一段路径直接指定 Portal 名称：
* `GET /{portal}/`：获取该入口的完整引导文档（包含守则、索引与授权清单）
* `GET /{portal}/rag?q={query}`：在该入口白名单内进行 RAG 切片检索
* `GET /{portal}/{relPath}`：读取白名单内的授权文档原文（未授权则 403）
* `POST /{portal}/upload`：向该入口的 `uploads/` 目录上传经验文档

### 模式 B：Domain 风格分桶（Virtual-Hosted-Style Bucketing）
通过 HTTP 请求头 `Host` 的子域名或专属域名直达分桶，URL 路径直接映射为对象路径：
* **子域名模式**：如 `Host: agent-guidelines.kb.local:8080`
* **独立域名模式**：在入口目录放置 `domain.txt`（如 `sec.internal`）
* 访问形式：
  * `GET /`：直达当前入口的引导页
  * `GET /rag?q={query}`：直接在该入口内发起 RAG 检索
  * `GET /{relPath}`：直接读取该入口下的文件全文
  * `POST /upload`：直接上传至该入口

---

## 🤖 Agent 专用固定入口规范

| 功能 | 端点路径 (Path-Style) | 请求方式 | 返回格式 | 说明 |
| :--- | :--- | :--- | :--- | :--- |
| **System Prompt 注入** | `/{portal}/` 或 `/{portal}/entry` | `GET` | Markdown 或 JSON | 注入 Agent 当前环境的行为守则与受限索引 |
| **RAG 知识检索** | `/{portal}/rag?q={query}` | `GET` | Markdown 片段或 JSON | 仅在 `filelist.txt` 内提取高相关性段落 |
| **文件正文读取** | `/{portal}/{relPath}` | `GET` | Markdown 原文 | 白名单受限读取，非授权路径返回 403 |
| **经验教训回传** | `/{portal}/upload` | `POST` | JSON 响应 | Agent 遇到踩坑经验回传至 `uploads/` 优化知识库 |

### Agent 接入代码示例：

```bash
# 1. 获取 System Prompt 上下文
curl http://127.0.0.1:8080/agent-guidelines/

# 2. 问答前先查询 RAG
curl "http://127.0.0.1:8080/agent-guidelines/rag?q=任务执行超时"

# 3. 读取授权文件
curl http://127.0.0.1:8080/agent-guidelines/notes/sop-session-task-execution.md

# 4. 回传实测教训优化知识库
curl -X POST "http://127.0.0.1:8080/agent-guidelines/upload" \
  -H "Content-Type: application/json" \
  -d '{"title":"超时重试排查总结","content":"实测超时应采用指数退避..."}'
```

---

## 🚪 多入口管理（新增与热重载）

### 方式一：文件系统直接创建（免重启热重载）
1. 在 `portals/` 目录下创建新文件夹：`mkdir -p portals/api-dev`
2. 写入 `portals/api-dev/filelist.txt`、`INDEX.md` 与 `AGENTS.md`
3. 触发热加载 API：`curl http://127.0.0.1:8080/api/portals/reload`（无需重启服务）

### 方式二：API 动态创建
直接发送 POST 请求，服务端自动建目录、落盘配置并立即生效：
```bash
curl -X POST "http://127.0.0.1:8080/api/portals/create" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "crawler-bot",
    "filelist": ["notes/tool-forum-*.md", "notes/site-image-*.md"],
    "agents": "# 爬虫 Agent 守则\n1. 遵守频控",
    "index": "# 爬虫专属索引\n- [论坛采集](file/notes/tool-forum-collect.md)"
  }'
```

---

## 🛠️ 编译、运行与 CI/CD

### 本地编译运行
```bash
cd server

# 运行单元测试
go test -v ./...

# 编译单二进制
go build -trimpath -ldflags="-s -w" -o kb-server .

# 启动服务
./kb-server -port 8080 -listen 0.0.0.0 -kb .. -portals ../portals
```

### GitHub Actions CI 自动化构建
本项目包含 `.github/workflows/ci-release.yml`：
* **每次 push 到 main**：自动执行单元测试与全平台编译校验。
* **推送 `v*` 标签**：自动并行交叉编译 6 大主流平台二进制（Windows amd64/arm64、Linux amd64/arm64、macOS amd64/arm64），打包归档并附带 SHA256 校验和自动发布 GitHub Release。
