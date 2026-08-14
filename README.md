# AIM Feishu RM Assistant

基于飞书机器人的 RoboMaster 助手。接入 [RM Search](https://search.scutbot.cn) 生产环境接口, 提供关键词搜索与定时精选推送, 推送摘要由大模型生成。

## 功能

### 搜索

- 私聊机器人直接发送关键词即可搜索; 群聊中需要 @机器人。
- 搜索范围覆盖 RM Search 的全部内容源: 论坛文章、论坛问答、论坛专栏、官网公告、PDF 附件。
- 结果以交互卡片返回, 每页 5 条, 发送"下一页"/"上一页"/"第N页"翻页。
- 默认在结果之后追加一条 AI 总结 (先综述再分条), 可发送"关闭总结"/"开启总结"切换。

### 定时推送

- 用户可通过自然语言订阅: 如"订阅每日推送"、"订阅每周推送"、"订阅每天晚上9点推送"、"退订"。
- 每日推送: 每天在设定时间 (默认 20:00) 推送过去 24 小时的内容。
- 每周推送: 每周一在设定时间推送上一个自然周 (周一 0 点至周日 24 点) 的内容。
- 推送卡片分为"官网公告"与"论坛精选"两个板块, 论坛精选由大模型从候选池 (最新 100 条) 中挑选最有价值的条目, 并生成综述与分条摘要。
- 支持私聊订阅与群订阅 (在群里 @机器人 发送订阅指令即为该群订阅)。
- 大模型不可用时自动降级为原始条目列表, 并附提示。

## 飞书应用创建指引

1. 前往[飞书开放平台](https://open.feishu.cn/app)创建一个企业自建应用。
2. 在"添加应用能力"中启用**机器人**。
3. 在"权限管理"中开通以下权限:
   - `im:message` (获取与发送单聊、群组消息)
   - `im:message.group_at_msg` (接收群聊中 @机器人消息)
   - `im:chat` (获取群信息, 群订阅需要)
4. 在"事件与回调"中选择**使用长连接接收事件**, 订阅事件 `im.message.receive_v1` (接收消息)。本服务使用 WebSocket 长连接, 无需公网地址。
5. 发布应用版本并通过审核 (测试阶段可设置可用范围为部分成员)。
6. 将"凭证与基础信息"页的 App ID 与 App Secret 填入环境变量。

注: 官方 Go SDK 目前未实现长连接上的卡片回调, 因此机器人不使用卡片按钮, 全部交互通过聊天消息完成。

## 配置

复制 `.env.example` 为 `.env` 并填写:

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `FEISHU_APP_ID` | 飞书应用 App ID | 必填 |
| `FEISHU_APP_SECRET` | 飞书应用 App Secret | 必填 |
| `RMSEARCH_BASE_URL` | RM Search 部署地址 | `https://search.scutbot.cn` |
| `LLM_BASE_URL` | OpenAI 兼容接口地址 | `https://api.openai.com/v1` |
| `LLM_API_KEY` | 大模型 API Key | 空 (为空时摘要降级) |
| `LLM_MODEL` | 模型名 | `gpt-4o-mini` |
| `SQLITE_PATH` | SQLite 数据库路径 | `./data/assistant.db` |
| `PUSH_DEFAULT_HOUR` | 新订阅默认推送小时 | `20` |
| `PUSH_DEFAULT_MINUTE` | 新订阅默认推送分钟 | `0` |
| `TZ` | 时区覆盖, 留空跟随宿主机 | 空 |

大模型仅用于摘要: 搜索结果的总结、推送条目的挑选与摘要。模型不会调用任何工具, 所有内容均来自 RM Search 接口返回。

## 运行

### Docker Compose (推荐)

```bash
cp .env.example .env
# 填写 FEISHU_APP_ID / FEISHU_APP_SECRET / LLM_API_KEY
docker compose up -d --build
```

时区处理 (同时支持 macOS 与 Debian Linux):

- compose 已默认以只读方式挂载宿主机的 `/etc/localtime` 与 `/etc/timezone`, 程序按 `TZ 环境变量 → /etc/localtime (符号链接名, 或与镜像内 tz 数据库字节比对) → /etc/timezone → UTC` 的优先级解析, macOS Docker Desktop 与 Debian 宿主机均可直接跟随系统时区。
- 在 `.env` 里设置 `TZ=Asia/Shanghai` 可显式覆盖; 留空则跟随宿主机。
- 注意: macOS Docker Desktop 会合成一个内容为 VM 默认值 (`Etc/UTC`) 的 `/etc/timezone`, 因此解析优先级刻意将 `/etc/localtime` 排在它之前 (见 `internal/tz`)。
- 启动日志会打印 `timezone resolved: ... (via ...)` 及来源, 便于确认配置生效。

### 本地运行

```bash
cp .env.example .env
export $(grep -v '^#' .env | xargs)
go run .
```

## 目录结构

- `internal/config` 环境变量配置
- `internal/rmsearch` RM Search (Meilisearch 代理) 客户端
- `internal/llm` OpenAI 兼容摘要客户端
- `internal/store` SQLite 持久化 (设置、订阅、推送记录)
- `internal/bot` 飞书 WebSocket 接入、消息处理、交互卡片、意图解析
- `internal/push` 定时调度与日报/周报生成

## 开发

```bash
gofmt -l .
go vet ./...
go test ./...
go build ./...
```

提交遵循 Conventional Commits, 分支与仓库命名遵循团队规范 (aim-common-rules)。
