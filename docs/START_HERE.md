# 从这里开始

客户直接私聊机器人，或把机器人拉入客户群，客服 B 在私有客服群的对应 Topic 回复。客户收到的回复显示为机器人身份。

## 1. 准备 Telegram

1. 在 BotFather 创建普通机器人，保存 `BOT_TOKEN`。不需要 A 账号、Business/Secretary Mode 或额外的账号授权。
2. 新建群 → 点群名 → 编辑/管理群组 → 群组类型选「私有」→ 开启「Topics / 话题」。
3. 把机器人加入群并设为管理员，开启「管理话题 / Manage Topics」以及正常发文字、图片、视频、语音和文件的能力。
4. 获取 B 等客服的数字用户 ID，以及群的负数 chat ID。分别填写 `AGENT_IDS` 和 `SUPPORT_GROUP_ID`；不是 username、手机号或群链接。
5. 服务上线后，把 `https://t.me/<你的机器人用户名>` 发给客户。客户点「开始」或主动发消息，即可建立客服会话。客户不用加入客服群。

[官方说明](https://core.telegram.org/bots#how-are-bots-different-from-users)：机器人不能主动建立陌生人私聊。本版本不再使用 Business 的 24 小时来信窗口；客户屏蔽机器人后仍可能发送失败。

### 客户群入口

把同一个 Bot 拉进客户群 → 设为管理员 → 允许发送文字和媒体 → 群内用个人身份发一条新消息。客服工作群会自动出现「客户群<负数ID>」Topic，B 在里面回复，Bot 会发回客户群。

不用填写客户群 ID，不设客户群白名单。所有 Bot 能读取的客户群普通消息都会进入客服群。`SUPPORT_GROUP_ID` 只填内部客服群的 ID，客户不要加入客服工作群。

客户群开启 Topics 时，每个原话题对应一个独立客服 Topic，回复回到原话题。多人消息共用该群/话题的客服 Topic；匿名管理员和其他机器人消息不处理。每条转给客服的消息下方显示发送者姓名、用户名、数字 ID 和来源群名；原回复引用不保留。

读取权限依据见 [Telegram 官方说明](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get)。

## 2. 准备服务器与配置

需要 Docker Engine / Docker Desktop + Compose v2、一个可访问 Telegram API 的服务器、域名和 HTTPS 反向代理。本地电脑可以先测试启动；真实 webhook 需要公网 HTTPS。

```sh
# 首次在服务器下载；已有源码则进入项目目录即可。
git clone https://github.com/kurtloong/tg-business-helpdesk.git
cd tg-business-helpdesk
# 保留已有配置。
test -e .env || cp .env.example .env
chmod 600 .env
openssl rand -hex 32
openssl rand -hex 32
openssl rand -hex 32
```

编辑 `.env`，填入以下值。上面的三个随机值分别用作数据库密码、Webhook secret 和管理员 token，必须不同。

| 配置 | 含义 |
|---|---|
| `POSTGRES_PASSWORD` | 数据库随机密码，使用生成的十六进制字符串 |
| `BOT_TOKEN` | BotFather 提供的机器人 token |
| `WEBHOOK_SECRET` | 验证 Telegram webhook 的随机值 |
| `ADMIN_TOKEN` | 本机查看状态和重试失败任务的随机值 |
| `SUPPORT_GROUP_ID` | 私有客服超级群的数字 ID |
| `AGENT_IDS` | B 等可信客服的数字 ID，多个用逗号分隔 |
| `WEBHOOK_URL` | 如 `https://support.example.com/webhook` |

Compose 会自动设置数据库连接，暂时不用修改样例中的 `DATABASE_URL`；它只供直接运行 Go 程序时使用。不要把 `.env` 提交到 Git，也不要把真实 token 发到公开 issue。

## 3. 启动、检查、接入

```sh
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 120
docker compose exec app helpdesk -action check
curl --fail http://127.0.0.1:8080/readyz
```

`check` 验证普通机器人身份、群私有性、Topics 和管理权限。若失败，先修正 Telegram 的配置，再继续。

参考根目录 `Caddyfile.example` 在**宿主机**配置 HTTPS，替换域名。只对公网开放 `/webhook`，保持 `/admin` 在内网。若代理也在容器内，使用同网络的 `app:8080`，不能用代理容器自己的 `127.0.0.1`。

HTTPS 配好后执行：

```sh
docker compose exec app helpdesk -action set-webhook
docker compose exec app helpdesk -action webhook-info
```

`webhook-info` 应显示正确 URL，观察 `pending_update_count` 和 `last_error_message`；旧错误的时间戳不等于现在仍失败，要结合新消息判断。

## 4. 用真实账号验收

1. 用另一个客户账号打开机器人链接，点「开始」后给 bot 发消息，确认自动创建客户 Topic。
2. B 在该 Topic 回复，确认客户收到机器人发出的内容。
3. 依次测试图片、视频、语音、文件及字幕。
4. 再换一个客户账号，确认分配不同 Topic，回复不会串客户。
5. 非白名单客服在工作群发言不得送给客户；重启 app 后 Topic 映射仍应保留。
6. 把 Bot 加入两个测试客户群并设为管理员，分别发消息并回复，确认不会串群；开启话题的客户群还需测试两个原话题的回复位置。

身份卡在文本和媒体消息下方显示：姓名/用户名、可复制的用户 ID，以及适用的来源群名/话题 ID。没有用户名也能靠姓名和 ID 区分；同名客户以 ID 为准。仅新处理的消息带身份卡，历史消息不回填，客服回复不会附带此卡。

**客户 Topic 内所有白名单客服的受支持消息都会发送给客户，不要在其中写内部备注。**

## 5. 日常查看状态

```sh
docker compose ps
docker compose logs --tail 100 app
docker compose exec app helpdesk -action webhook-info
```

在项目目录、宿主机操作管理员脚本：

```sh
# 仅加载你自己创建、可信的 .env。
set -a
. ./.env
set +a
python3 scripts/admin.py status
python3 scripts/admin.py list
# 核对机器人私聊/群中是否已送达，再重试 dead 任务：
python3 scripts/admin.py retry 123456789
```

`status` 包含 worker 心跳、完整 pending/dead 数量、已到执行时间的任务数、最老积压时长。`/readyz` 检查活跃 worker 的共享心跳；它不保证 Telegram 每次发送都成功。

生产至少接入这些告警：

- worker 不健康，或 `/readyz` 持续非 200。
- `oldest_ready_seconds` 持续超过 60 秒；低流量试运行时即可从此阈值开始调整。
- `queue.dead` 增长：旧 dead 不会自动删除，应看增量并处理。
- Telegram `pending_update_count` 增长或 webhook 出现新错误。
- 数据库磁盘、备份失败。

状态接口需 `Authorization: Bearer <ADMIN_TOKEN>`。Docker 的 `unhealthy` 标记本身不会自动重启容器；`restart: unless-stopped` 只处理进程退出，需要外部监控通知值班人员。

## 上线边界

先用少量真实客户试运行，再逐步放量。当前仍是单活 worker；群方向实际发送间隔至少 3.2 秒（约 18 次/分钟），所有客户私聊出站共用独立的 1 秒间隔（约 60 次/分钟）。网络开销和重试会降低这些上限；无关事件不再占用发送间隔。

不保证错误恢复时的严格消息顺序，也不能保证跨 Telegram/数据库的绝对不重复。备份恢复、崩溃时的重复发送窗口和支持范围详见 README。多客服共享群，也就共享全部客户内容；需要员工之间的数据隔离时不能直接使用这个模型。

## 已部署旧版本的升级

先备份数据库，停止所有旧 app 实例，再更新并启动新版本。v3 会保留旧 Business 历史，但旧 pending 任务转为需人工审阅的 dead，不会把原 A 联系人或旧 Topic 自动变成 bot 会话。客户必须先联系机器人，新会话使用新的 Topic。删去旧 `BUSINESS_OWNER_IDS`，重新执行 `set-webhook`；其他配置继续使用。
