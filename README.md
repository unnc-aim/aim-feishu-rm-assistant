# AIM Feishu RM Assistant

基于飞书机器人的 RoboMaster 助手。接入 rm-search (默认随 compose 内置私有部署, 也可指向[生产环境](https://search.scutbot.cn)), 提供关键词搜索与定时精选推送, 推送摘要由大模型生成。

## 功能

### 搜索

- 私聊机器人直接发送关键词即可搜索; 群聊中需要 @机器人。
- 搜索范围覆盖 RM Search 的全部内容源: 论坛文章、论坛问答、论坛专栏、官网公告、PDF 附件。
- 结果以交互卡片返回, 每页 5 条, 发送"下一页"/"上一页"/"第N页"翻页。
- 默认在结果之后追加一条 AI 总结 (先综述再分条), 可发送"关闭总结"/"开启总结"切换。

### 定时推送

- 用户可通过自然语言订阅: 如"订阅每日推送"、"订阅每周推送"、"订阅每天晚上9点推送"、"退订"。订阅后首次推送在下一个时间点触发 (不补推旧窗口)。
- 每日推送: 每天在设定时间 (默认 20:00) 推送过去 24 小时的内容, 卡片下方附"本周动态" (本周一 0 点至今) 及其总结。
- 每周推送: 每周一在设定时间推送上一个自然周 (周一 0 点至周日 24 点) 的内容, 卡片下方附"本月动态" (本月 1 日至今) 及其总结。
- 主窗口没有新内容时自动逐级放宽: 本周动态 → 近 7 天 → 本月动态 → 近 30 天, 取第一个有内容的窗口展示; 近 30 天都没有新内容时推送"最近一个月都没有新的动态"提醒。
- 推送卡片每个窗口分为"官网公告"与"论坛精选"两个板块, 论坛精选由大模型从候选池 (最新 100 条) 中挑选最有价值的条目, 并生成综述与分条摘要; 空窗口跳过大模型调用, 秒级返回。
- 支持私聊订阅与群订阅 (在群里 @机器人 发送订阅指令即为该群订阅)。
- 大模型不可用时自动降级为原始条目列表, 并附提示。
- 发送"测试推送"或"立即推送"可立即生成最近 24 小时的推送, 用于验证推送链路; 启动日志会打印每个订阅的下次触发时间 (本地时区), 便于排查时区问题。

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
| `RMSEARCH_BASE_URL` | rm-search 地址 (compose 内默认内置实例, 可指向外部部署如 `https://search.scutbot.cn`) | `http://rm-search:8080` |
| `LLM_BASE_URL` | OpenAI 兼容接口地址 | `https://api.openai.com/v1` |
| `LLM_API_KEY` | 大模型 API Key | 空 (为空时摘要降级) |
| `LLM_MODEL` | 模型名 | `gpt-4o-mini` |
| `SQLITE_PATH` | SQLite 数据库路径 | `./data/assistant.db` |
| `LOG_DIR` | 日志目录, 按日轮转为 `assistant-YYYY-MM-DD.log`, 所有用户消息与操作均写入 | `./data/logs` |
| `PUSH_DEFAULT_HOUR` | 新订阅默认推送小时 | `20` |
| `PUSH_DEFAULT_MINUTE` | 新订阅默认推送分钟 | `0` |
| `TZ` | 时区覆盖, 留空跟随宿主机 | 空 |

注: `SQLITE_PATH` / `LOG_DIR` 在 Docker 下无需设置 (镜像默认 `/data/assistant.db` 与 `/data/logs`); 表中默认值为裸 `go run` 场景。

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

### 全量部署 (连带私有 rm-search)

主 `docker-compose.yml` 一并起动机器人和一套私有 rm-search (PostgreSQL + Meilisearch + rm-search), 不依赖 search.scutbot.cn:

```bash
# .env 中额外设置 POSTGRES_PASSWORD 和 MEILI_MASTER_KEY (32+ 字符)
# 全部服务均从镜像运行 (rm-search 内嵌建表 DDL, 启动自动建表), 无需任何仓库检出
docker compose up -d
```

启动后 rm-search 的定时任务每分钟自动增量同步论坛三类帖和公告; **历史帖子由启动即常驻的后台回填循环持续爬取** (有序队列逐帖推进水位, 失败全体冷却 5 分钟后续爬, 触底后标记完成并永久停止), 每天 0/6/12/18 点另有追新任务补齐最新帖子。等不及可手动加速 (与自动水位线兼容):

```bash
# 公告全量 (ID 1~3000 覆盖全部历史; 索引设置随 rm-search 启动自动应用, 无需手动步骤)
docker compose run --rm \
  --entrypoint /usr/local/bin/crawl rm-search \
  --announce-start 1 --announce-end 3000
# 帖子全量 (后台, 断点可续; 可先爬近期 --posts-start 1700000)
nohup docker compose run --rm \
  --entrypoint /usr/local/bin/crawl rm-search \
  --posts-start 0 --posts-end 2000000 --posts-goroutines 50 \
  >> crawl.log 2>&1 &
# 回填完成后全量灌索引
docker compose run --rm \
  --entrypoint /usr/local/bin/recreate-index rm-search
```

数据落在 `./data/full/` (pg、meili)。assistant 与 rm-search 镜像均从 GHCR 拉取 (两个仓库的 CI 自动构建); 升级时 `docker compose pull && docker compose up -d`。本机开发 (如 arm64 Mac) 可将 compose 中 assistant 的 `image:` 换回 `build: .` 本地构建。也可以直接使用 rm-search 仓库 `deploy/` 目录的独立部署 (见其 README)。

## 目录结构

- `internal/config` 环境变量配置
- `internal/rmsearch` RM Search (Meilisearch 代理) 客户端
- `internal/llm` OpenAI 兼容摘要客户端
- `internal/store` SQLite 持久化 (设置、订阅、推送记录)
- `internal/log` 按日轮转的文件日志
- `internal/tz` 时区解析 (TZ / /etc/localtime / /etc/timezone 优先级)
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
