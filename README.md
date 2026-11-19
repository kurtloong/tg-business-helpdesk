# Telegram Bot 多客户客服系统

**首次使用请先看 [从这里开始](docs/START_HERE.md)。** 本项目具备试运行所需的持久队列与运维入口；生产上线仍需完成真实 Telegram 验收、监控接入和备份恢复演练。

客户私聊 Bot 或在客户群发消息 → 私有客服超级群中的独立 Topic → 白名单客服 B 回复 → Bot 发回原私聊、原客户群或原群话题。

使用普通 BotFather 机器人即可，不需要 A 账号、Business/Secretary Mode、Premium 或 Business 授权。客户看到的是机器人身份。Go + Gin + Gorm + PostgreSQL 实现，保留文本、图片、视频、语音、文件双向同步、持久化去重重试、Webhook 安全和运维入口。

私聊按数字 chat ID 分配 Topic；客户群按「群 chat ID＋原话题 ID」分配 Topic，普通群每群一个。白名单客服可处理该群所有 Topic；群成员共享全部客户内容，不提供员工间的数据隔离。一个数据库只供同一个机器人使用。

## 官方 API 依据（2026-11-17）

- [普通机器人](https://core.telegram.org/bots#how-are-bots-different-from-users)：客户必须先打开机器人的 `https://t.me/<bot_username>` 链接并点「开始」或发消息，机器人不能主动建立陌生人会话。客户若屏蔽机器人，回复可能收到 403 并进入死信。
- [createForumTopic](https://core.telegram.org/bots/api#createforumtopic)：群启用 Topics，机器人是管理员并拥有 `can_manage_topics`。
- [sendMessage](https://core.telegram.org/bots/api#sendmessage) 及相应媒体方法：私聊方向使用 `chat_id`，向话题发送时额外使用 `message_thread_id`；不再传 `business_connection_id`，不再检查 Business 的 24 小时回复窗口。
- [setWebhook](https://core.telegram.org/bots/api#setwebhook)：公网 HTTPS 与 secret header 校验，只订阅 `message`。历史 Business 更新即使再次送达也会忽略。

## 1. 准备机器人和群

1. BotFather 创建普通机器人并保存 token；不需要启用 Secretary/Business Mode，也不要让其他 webhook 服务或 getUpdates 同时接管它。
2. 新建群 → 群设置 → 群组类型选「私有」→ 开启「Topics / 话题」。只邀请可信客服。
3. 添加机器人为群管理员，授予管理话题及正常发送文字/媒体的能力。
4. 获取 B 等客服的数字用户 ID 和群的负数 chat ID；分别填写 `AGENT_IDS` 和 `SUPPORT_GROUP_ID`。它们不是 username、手机号或邀请链接。
5. 部署并注册 webhook 后，把机器人链接发给客户。客户先点「开始」，然后直接发送问题；`/start` 本身也会进入客户 Topic。

### 接入客户群

把同一个 Bot 加入客户群，设为管理员并允许发送文本和媒体，即可接收普通用户的群消息并自动创建客服 Topic。无需客户群白名单，也不用把客户群 ID 填入配置；`SUPPORT_GROUP_ID` 始终只填内部客服群 ID。Bot 能读到的所有客户群普通消息都会同步，请只把它加入需要接入客服的群。

[官方群消息权限](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get)：群管理员 Bot 可以接收普通用户消息。若不设为管理员，则需通过 BotFather 的 `/setprivacy` 关闭隐私模式，并将 Bot 移出后重新加入群；同时确保它有发送文本/媒体权限。权限开启前的历史不会补拉。

普通群/超级群每群一个客服 Topic，标题为「客户群<负数ID>」。客户群启用话题时，每个原话题单独映射，客服回复发回原话题；多个群成员共用对应 Topic。文本及媒体保持原内容；每条消息下方显示发送者身份卡，不保持原始回复引用。匿名管理员、频道身份及其他机器人消息不处理，群内用户使用个人身份发言。

群升级为超级群会改变 chat ID；升级后的新消息创建新映射和客服 Topic，旧 Topic 不会自动迁移。机器人被移出客户群或失去发送权限后，回复进入死信。客服工作群被明确排除，避免循环转发。

## 2. Docker Compose 部署

需要 Docker Engine + Compose v2、域名和 HTTPS 反向代理。80/443 指向部署服务器。

```sh
test -e .env || cp .env.example .env
openssl rand -hex 32
# 分别运行三次，为数据库密码、WEBHOOK_SECRET、ADMIN_TOKEN 填入不同随机值。
# 编辑 .env，把所有占位值替换成真实配置。
chmod 600 .env
docker compose up -d --build db app
docker compose exec app helpdesk -action check
docker compose exec app helpdesk -action set-webhook
curl --fail http://127.0.0.1:8080/readyz
```

`check` 调用 Telegram 验证普通机器人身份、群私有性、Topics 和管理权限，不要求机器人具备 Business 能力。`set-webhook` 须在 HTTPS 已可访问时执行；保留 Telegram 待处理更新，`max_connections=1` 减少乱序。普通启动不自动覆盖 webhook。

应用启动先执行迁移，数据库健康后运行。PostgreSQL 不暴露宿主端口；app 仅绑定 `127.0.0.1:8080`。`Caddyfile.example` 用于**宿主机安装的 Caddy**，替换域名后加载，可自动签发 HTTPS。只向公网代理 `/webhook`，不向公网开放 `/admin`。

若反向代理也运行在容器内，改为访问同一 Docker 网络的 `app:8080`，不要使用容器内的 `127.0.0.1`。数据库内网使用 `sslmode=disable`；外部数据库须使用证书验证 TLS。

查看日志、升级：

```sh
docker compose logs --tail=100 app
docker compose up -d --build app
```

升级前备份数据库；不要执行 `docker compose down -v`，该命令会删除数据卷。Go 模块版本由 `go.mod` / `go.sum` 锁定；生产环境可进一步将基础镜像固定为经批准的 digest。

## 3. 本地运行和迁移

Go 1.27.4+、PostgreSQL 17+。已固定使用修复安全问题的 Go 1.27.4。程序只读取环境变量，不自动加载 `.env`。在本机数据库上先创建 `helpdesk` 用户和数据库，填写本地 DSN。**只 source 自己可信且符合 shell 语法的配置文件**。

```sh
set -a
. ./.env
set +a
go mod download
go run ./cmd/server -action migrate
go run ./cmd/server -action check
go run ./cmd/server -action serve
```

`migrations/001_init.sql`、`002_observability.sql`、`003_direct_bot.sql`、`004_customer_groups.sql` 和迁移版本嵌入二进制；启动时升级到 v4。v4 为原会话增加默认值为 0 的 `customer_thread_id`，保留已有私聊 Topic 和任务。历史 Business `routes` 表保留，新会话使用独立的 `bot_routes` 表。`schema_migrations` 记录已执行版本；事务与 PostgreSQL advisory lock 防止并发迁移。没有破坏性自动回滚。未来加迁移时增加 SQL 文件与版本执行逻辑，不能修改已上线版本冒充新迁移。

## 4. 功能与处理语义

- 文本及实体格式、图片、视频、语音、文件双向同步；媒体字幕及实体保留，图片选最大尺寸。
- 客户看到机器人发送的消息；B 在群内操作。媒体以新消息发送，不带转发来源。Telegram 大群成功排程但返回 `message_id=0` 时视作已提交，不因暂缺最终 ID 重发。不会保持 Telegram 原始回复引用。
- 客户首次直接联系机器人时创建 Topic，标题为「客户数字ID」，之后复用 `bot_routes` 映射。客户不需要加入客服群。
- 仅群内白名单客服可以代机器人回复。普通私聊不使用 Business 权限或 24 小时窗口；API 若因客户屏蔽、机器人被踢出群等原因拒绝发送，任务进入死信。
- 客户群自动接入；客服工作群仅处理白名单客服在已关联 Topic 内的回复。忽略机器人、匿名管理员/频道身份、旧 Business 消息和机器人回声。B 必须用个人身份发言。
- **客户 Topic 的所有白名单客服支持类型消息都会发送给客户**，不要在其中写内部备注。不支持的客服消息进入死信；不支持的客户类型在 Topic 发一条提示，请客户改发文本或受支持的文件。
- 相册的每个项目分别同步，不重组成相册。编辑、删除、贴纸、视频圆消息、音频音乐、富消息、历史回填和服务消息同步不在此版范围；已发送消息的后续编辑/删除不会同步。未知更新安全忽略。
- 只处理普通 `message`；不会拉取历史消息或把旧 A 联系人自动导入。

### 区分客户与群内发言人

转给客服的每条消息下方附带身份按钮：

```text
👤 张三 Lee · @zhangsan
用户 ID：123456789
群：客户交流群 · -1001234567890
原群话题 ID：7
```

私聊只显示姓名/用户名和用户 ID；群来源、原话题行只在适用时显示。点击用户名行可打开公开的 Telegram 用户名链接；没有用户名时仍显示姓名，点击该行复制用户 ID。点击 ID 或群来源行可复制对应数字 ID。姓名和群名是用户自填资料，同名或改名时以数字 ID 区分；不代表经过身份认证。

身份卡适用于文本、图片、视频、语音、文件及不支持类型的提示，不修改正文、字幕或格式实体，也不额外发送一条消息。只向内部客服群添加身份卡，客服回复客户时不附带该卡。超长姓名和群名在按钮上截短，数字 ID 保持完整。

不需要新增配置或数据库迁移；升级后新处理的消息自动带身份卡，已有 Topic 继续使用原映射。历史已发送消息不会回填身份。Topic 标题保留稳定的数字标识。

## 5. 去重、重试和可用性边界

Webhook 只有在 PostgreSQL 持久化成功后返回 200；数据库失败返回 503，让 Telegram 重投。请求须通过 secret 验证，限制 1 MiB，JSON 必须带合法 `update_id`。`update_id` 主键及来源消息唯一键双重去重。不要删除历史任务后又重放旧 webhook，否则会失去去重依据。

PostgreSQL 持久队列使用单个活动 worker；专用数据库会话持有 advisory lock，多个 app 实例不会同时处理同一队列。副本可接收 webhook，持锁进程断开后自动接管。多副本也必须保持相同的白名单和群配置。只适合同一 bot 的实例共享此库；不要让不同 bot 共用库。worker 单次处理检查点独立提交，不长时间持有数据库事务。

网络错误、HTTP/API 429、5xx 指数退避，尊重 `retry_after`；最多 8 次。400/401/403 等永久错误直接进入 `dead`；失败在群 General Topic 尝试提示任务 ID，持久死信记录为准。告警发送自身不重试，需监控死信队列。已成功任务不允许人工重放。

**不承诺外部发送 exactly-once**：Telegram 没有业务发送幂等键。如果发送/创建 Topic 成功，但返回丢失或进程在保存结果前崩溃，后续重试可能重复发消息或留下孤立 Topic。正常 webhook 重投与已落库消息能去重，这个跨系统崩溃窗口不能仅靠 PostgreSQL 消除。重要人工重放前先在机器人私聊/群中检查是否已送达。

任务按可执行的 update ID 选取；延迟重试不阻塞后来任务，**错误恢复时不保证严格消息顺序**。单 worker 空队列每 250ms 查询一次；所有群出站发送（包括客服群转发、客户群回复和失败告警，共用限速）最小间隔 3.2 秒，所有客户私聊出站共用独立的 1 秒间隔。无关事件与普通队列状态处理不再等待 3.2 秒。群方向理论上限约 18 次/分钟，私聊方向约 60 次/分钟，单 worker 的网络开销与重试会进一步降低；按库级串行、非高吞吐架构。大量客户或持续大文件/相册可能积压；可监控 pending 最老创建时间。不要随意加并发绕开锁，否则会破坏路由创建的并发假设。

关闭/删除 Topic 会导致发送失败，应由群管理员恢复原 Topic；不要未经核对直接改数据库映射。变更客服群需要明确迁移已有路由，否则系统拒绝继续转发入站消息，防止意外串群。

### 从旧 Business 版本升级

先备份数据库，**停止所有旧 app 实例后**再启动新版本，避免旧 worker 继续处理旧会话。v3 不删除旧 `routes` 或历史正文/发送记录；为防止模式切换时误投，迁移当时所有 `pending` 任务会转为 `dead` 并标注 `Business mode retired`，须人工审阅，不能批量盲目重放。

旧 Topic 不复用为普通 bot 会话，也不能继续在其中回复旧客户。客户须先直接联系机器人，新消息会创建新 Topic。删除 `.env` 中的 `BUSINESS_OWNER_IDS`（旧值即使保留也不再使用），更新代码后重新执行 `set-webhook`，将订阅改成仅 `message`。迁移完成后不要直接回退运行旧程序，应按备份恢复计划处理。

## 6. 运维和权限

`GET /healthz` 检查进程；`GET /readyz` 检查数据库中的活跃 worker 心跳（超过 90 秒失效，未启动 worker 也返回 503）。多副本共享同一心跳，因此备用实例不会因未持锁而误报。心跳不能证明 Telegram 通路健康；须同时监控队列积压和 webhook 状态。

`GET /admin/status` 使用管理员 token，返回 worker 健康状态、完整 pending/dead 数量、可执行任务数与最老积压时长，不包含客户正文。`python3 scripts/admin.py status` 可直接查看。`helpdesk -action webhook-info` 查询 Telegram 端的待投递数量及最近错误。

Dockerfile 自带 `healthcheck` 命令与健康检查；Docker 标记 unhealthy 不会自动触发 restart 策略，需接外部告警。应用使用 JSON 日志记录成功、重试、失败任务的 update ID、方向、尝试次数和耗时。忽略事件为 debug；数据库错误记录安全的错误分类/SQLSTATE，panic 保留调用栈，避免打印请求头和消息正文。

死信接口需要单独 `ADMIN_TOKEN`，通过本地地址或 SSH 隧道操作。脚本不会打印 token：

```sh
# 先在本机设置 ADMIN_TOKEN，或可信环境中加载 .env
python3 scripts/admin.py status
python3 scripts/admin.py list
python3 scripts/admin.py list --status pending
python3 scripts/admin.py retry 123456789
```

对应接口：`GET /admin/jobs?status=dead`（最近 100 条，返回任务元数据，不含消息正文），`POST /admin/jobs/:update_id/retry`（只可重放 dead）。重试会重新检查当前客服白名单和 Topic 路由，不能绕过 Telegram 的屏蔽或权限限制。管理员 token 拥有重放所有客户任务的能力，不交给普通客服。脚本使用本地端口；远程访问先建立 `ssh -L 8080:127.0.0.1:8080 user@server`。

库中 payload 存有客户文字、ID 和 file_id；保护数据库、备份、群访问权限。默认没有自动清理；保留策略需同时考虑个人信息和去重要求。可对终态任务将 payload 置为 `{}` 以减少正文保留，但保留 update_id/source_key/status，并且这些任务不能再重试。清理前备份并审核。

备份/恢复（从项目目录运行）：

```sh
docker compose exec -T db pg_dump -U helpdesk -Fc helpdesk > backup.dump
# 恢复到已备份、允许覆盖的目标数据库前，先停止 app；这是恢复操作，会覆盖对象。
docker compose stop app
cat backup.dump | docker compose exec -T db pg_restore -U helpdesk -d helpdesk --clean --if-exists
docker compose up -d app
```

`.env` 与数据库凭据不提交到版本库。应用日志不打印 token URL、客户正文和原始 webhook；限制和轮转日志。数据库/管理接口保留在内网。使用直连 PostgreSQL 或 PgBouncer session pooling；不要使用 transaction pooling，因为 worker 的排他锁绑定数据库会话。部署前执行 `check`，确保群仍是私有的；应用不会持续监控群是否后来被改为公开。

## 7. 测试和验收

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/helpdesk ./cmd/server
# 仅用于全新的一次性测试库：集成测试会 TRUNCATE jobs/routes/bot_routes，数据库名必须以 _test 结尾！
TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/helpdesk_test?sslmode=disable' go test -race -count=1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/helpdesk-linux-amd64 ./cmd/server
```

单元测试覆盖媒体选择、转发参数、Webhook 安全/体积/JSON、白名单配置、回声过滤、错误分类和退避。真实 PostgreSQL 集成测试配合本地 HTTP 模拟 Telegram，覆盖双向路由、Topic 复用、并发去重、非白名单/匿名客服拦截、普通私聊超过 24 小时仍可回复、旧 Business 更新及旧 Topic 隔离、v2→v3 升级保留历史、429 持久化重试、worker 重建后续跑、死信重放与排他锁。

真实 Telegram 验收清单：

1. 客户向机器人发送文本及四种媒体，确认专属 Topic 内容和字幕。
2. B 在 Topic 逐项回复，确认客户看到机器人发出；第二个客户验证不同 Topic 和回复对象。
3. 非白名单/匿名身份发言不传给客户；机器人回声不会循环。
4. 客户屏蔽机器人或临时撤销群权限，确认失败进入死信；恢复后核对是否已送达再重试。
5. 重启 app，确认映射保留；重复投递同一 webhook，确认不重复处理。
6. 检查无公开 admin 路由，测试备份恢复，观察日志和队列。

GitHub Actions 自动执行真实 PostgreSQL 集成测试、竞态检测、依赖漏洞扫描、Docker 构建和 Compose 健康启动测试；使用临时假凭据，不会向真实 Telegram 发送消息。

具体已执行的验证与未验证项目见 `VERIFICATION.md`。本交付不包含真实账号凭据，不会自动连接你的 Telegram 或向真实客户发消息。

### 从私聊版本升级到群聊版本

备份数据库，停止所有旧 app 实例，再运行 `docker compose up -d --build --wait`。v4 自动保留已有私聊映射和队列；Webhook 仍只订阅 `message`，无需重新注册。把 Bot 加入客户群并设为管理员，再发新消息测试。无需新增环境变量。
