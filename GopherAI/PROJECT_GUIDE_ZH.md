# GopherAI 项目导读（快速上手）


## 1. 项目定位与整体能力

`GopherAI` 是一个前后端分离的 AI 应用平台，核心能力包括：

- 用户注册/登录（验证码 + JWT）
- 多会话聊天（普通/流式 SSE）
- 多模型接入（工厂模式）
- 图片识别（ONNX Runtime）
- 文档上传与 RAG 检索增强
- TTS 语音合成任务创建与查询

后端技术栈：`Go + Gin + GORM + MySQL + Redis + RabbitMQ`  
前端技术栈：`Vue3 + Vue Router + Axios + Element Plus`

## 2. 目录结构（精简）

```text
GopherAI-v2/
├── main.go
├── config/
│   ├── config.go
│   └── config.toml
├── router/
│   ├── router.go
│   ├── user.go
│   ├── AI.go
│   ├── Image.go
│   └── File.go
├── controller/
│   ├── common.go
│   ├── user/
│   ├── session/
│   ├── image/
│   ├── file/
│   └── tts/
├── service/
│   ├── user/
│   ├── session/
│   ├── image/
│   └── file/
├── dao/
│   ├── user/
│   ├── session/
│   └── message/
├── model/
├── middleware/jwt/
├── common/
│   ├── aihelper/
│   ├── mysql/
│   ├── redis/
│   ├── rabbitmq/
│   ├── rag/
│   ├── image/
│   ├── tts/
│   ├── email/
│   └── mcp/
├── utils/
└── vue-frontend/
    ├── src/main.js
    ├── src/router/index.js
    ├── src/utils/api.js
    └── src/views/*.vue
```

## 3. 后端架构与模块职责

### 3.1 分层关系

`router -> controller -> service -> dao/common -> mysql/redis/rabbitmq/AI provider`

- `router`：定义 URL 和路由分组，决定哪些接口走 JWT 鉴权。
- `controller`：参数校验、取 `Gin Context`、统一响应格式。
- `service`：业务编排层，连接 DAO 与基础组件。
- `dao`：数据库读写封装（用户、会话、消息）。
- `common`：基础设施与跨业务组件（AI 工厂、MQ、缓存、RAG、图像识别、邮件、TTS 等）。

### 3.2 启动流程（main）

`main.go` 启动顺序：

1. 读取 `config/config.toml`
2. 初始化 MySQL
3. 从数据库读取历史消息，初始化内存 `AIHelperManager`
4. 初始化 Redis
5. 初始化 RabbitMQ
6. 初始化 Gin 路由并启动 HTTP 服务

这意味着聊天会话的上下文不仅来自 DB，也会在服务启动后进入内存管理器，提升后续会话访问效率。

### 3.3 路由与鉴权

统一前缀：`/api/v1`

- 公开接口（无需 JWT）：`/user/*`
  - `/user/register`
  - `/user/login`
  - `/user/captcha`
- 鉴权接口（JWT）：
  - `/AI/*`（会话聊天、历史、流式、TTS）
  - `/image/*`（图片识别）
  - `/file/*`（RAG 文件上传）

JWT 中间件从 `Authorization: Bearer <token>`（或 query token）解析用户名并写入上下文。

## 4. 前端架构与模块职责

### 4.1 入口与路由

- 入口：`vue-frontend/src/main.js`
- 页面路由：`vue-frontend/src/router/index.js`
  - 登录注册：`/login`, `/register`
  - 业务页：`/menu`, `/ai-chat`, `/image-recognition`（需要 token）

### 4.2 API 请求策略

- Axios 封装：`src/utils/api.js`
  - `baseURL` 为 `/api`
  - 自动注入 `Authorization` 头
  - 401 自动清理 token 并跳转登录
- 开发代理：`vue.config.js`
  - `/api` 代理到 `http://localhost:9090`
  - 并改写成 `/api/v1`

所以前端写 `/AI/chat/send`，开发态最终会访问后端 `/api/v1/AI/chat/send`。

## 5. 核心功能调用流程

## 5.1 登录

1. 前端 `Login.vue` 调 `POST /user/login`
2. `controller/user` 接收参数
3. `service/user.Login` 调 `dao/user` 查用户
4. 比对密码（MD5）
5. 生成 JWT 返回

## 5.2 注册 + 验证码

1. `POST /user/captcha`：生成验证码，写 Redis，并通过邮件发送
2. `POST /user/register`：
   - 校验用户是否存在
   - 校验 Redis 验证码
   - 生成 11 位用户名并入库
   - 邮件发送用户名
   - 返回 JWT

## 5.3 聊天（普通模式）

1. 前端发 `POST /AI/chat/send-new-session` 或 `/AI/chat/send`
2. JWT 中间件解析用户
3. `service/session` 获取或创建会话 + AIHelper
4. `AIHelper.GenerateResponse` 调模型生成回复
5. 用户消息和 AI 消息通过 RabbitMQ 异步入库
6. 返回 AI 文本

## 5.4 聊天（流式 SSE）

1. 前端 `fetch` 调 `/api/AI/chat/send-stream(-new-session)`（页面里写了带 `/api` 的绝对路径）
2. 后端设置 SSE 响应头
3. 逐块返回 `data: ...`
4. 结束时发送 `data: [DONE]`
5. 同时依然走消息持久化逻辑（MQ 异步入库）

## 5.5 图片识别

1. 前端 `ImageRecognition.vue` 上传图片
2. 后端 `service/image` 调 `common/image` 执行 ONNX 推理
3. 返回分类结果

注意：模型路径当前在代码中是硬编码的 Linux 路径，跨环境部署时要调整。

## 5.6 RAG 文件上传

1. 前端 `AIChat.vue` 上传 `.md/.txt`
2. 后端 `service/file.UploadRagFile` 校验、落盘到 `uploads/<username>/`
3. 清理旧文件和旧索引（每用户仅保留一个文档）
4. 调 `common/rag` 建立向量索引

## 5.7 TTS 语音

1. 前端创建任务：`POST /AI/chat/tts`
2. 前端轮询查询：`GET /AI/chat/tts/query?task_id=...`
3. 返回音频 URL 后播放

## 6. AI 模型扩展机制（工厂模式）

`common/aihelper/factory.go` 内通过 `modelType` 映射模型构造器：

- `1`：OpenAI/百炼聊天模型
- `2`：RAG 模型
- `3`：MCP 模型
- `4`：Ollama（预留）

新增模型时通常只需要：

1. 实现 `AIModel` 接口
2. 在工厂注册新的 `modelType`
3. 前端下拉框增加选项

## 7. 数据与状态管理设计

- 会话/消息有两层状态：
  - 内存态：`AIHelperManager` 维护 `user -> session -> helper`
  - 持久态：MySQL 存储 `user/session/message`
- 消息写库走 RabbitMQ 异步，降低主请求阻塞。
- 服务启动时会把历史消息灌回内存 helper，恢复上下文。


- 图片识别模型路径为硬编码 Linux 路径，Windows 本地跑需要改配置或改代码。
- 会话列表读取依赖内存管理器，和纯 DB 查询策略有差异，排查会话问题时要关注“是否已加载到内存”。
- 流式接口在前端使用 `fetch`，与 Axios 请求链路不同（拦截器不生效），因此鉴权头是手动设置。

---

