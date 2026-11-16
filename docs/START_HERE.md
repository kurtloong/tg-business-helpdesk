# 从这里开始

本项目让客户只与 A 私聊，B 在客服群 Topic 回复。除此之外还需要一个 BotFather 机器人：服务拿的是机器人 token，不是 A/B 的账号密码。

## 1. 准备 Telegram

1. 在 BotFather 创建机器人，启用 Secretary Mode（旧客户端可能称 Business Mode）。
2. 创建**私有超级群**并开启 Topics，只邀请可信客服。将机器人设为管理员，授予管理 Topic 和正常发消息/媒体的能力。
3. A 在 Telegram 中连接这个机器人，授权它访问需要接待的客户会话，并允许回复。
4. 取得 A、B 的数字用户 ID，以及群的负数 chat ID。`BUSINESS_OWNER_IDS` 填 A；`AGENT_IDS` 填 B 或多个客服。用户名、手机号、群邀请链接均不可替代数字 ID。

官方入口与权限：[Business Bots](https://core.telegram.org/bots/features#business-bots)。回复受 [rights.can_reply 与 24 小时来信窗口](https://core.telegram.org/bots/api#businessbotrights) 限制。

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
| `BUSINESS_OWNER_IDS` | A 的数字 ID，多个用逗号分隔 |
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

`check` 验证机器人模式、群私有性、Topics 和管理权限。若失败，先修正 Telegram 的配置，再继续。

参考根目录 `Caddyfile.example` 在**宿主机**配置 HTTPS，替换域名。只对公网开放 `/webhook`，保持 `/admin` 在内网。若代理也在容器内，使用同网络的 `app:8080`，不能用代理容器自己的 `127.0.0.1`。

HTTPS 配好后执行：

```sh
docker compose exec app helpdesk -action set-webhook
docker compose exec app helpdesk -action webhook-info
```

`webhook-info` 应显示正确 URL，观察 `pending_update_count` 和 `last_error_message`；旧错误的时间戳不等于现在仍失败，要结合新消息判断。

## 4. 用真实账号验收

1. 用另一个客户账号给 A 发消息，确认自动创建客户 Topic。
2. B 在该 Topic 回复，确认客户收到 A 发出的内容。
3. 依次测试图片、视频、语音、文件及字幕。
4. 再换一个客户账号，确认分配不同 Topic，回复不会串客户。
5. 非白名单账号发言不得送给客户；重启 app 后 Topic 映射仍应保留。

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
# 核对 A/群中是否已送达，再重试 dead 任务：
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
