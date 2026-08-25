# TAPD → 钉钉缺陷通知服务

这是一个 Go 服务：按配置周期查询 TAPD 缺陷，用 PostgreSQL 持久化通知状态并去重，然后通过钉钉群自定义机器人 Webhook 发送 Markdown 消息。一个配置文件可以包含多个 `monitors`，每个 monitor 通过 `tapd_connection_id` 和 `dingtalk_connection_id` 引用数据库中的加密连接。

## 快速启动

需要 Go 1.25+ 和 PostgreSQL。项目已提供本地开发用的 `config.yaml` 和 `.env`；`.env` 会在加载配置时自动读取，已有的系统环境变量优先级更高。

生产环境不要提交 `.env`，建议通过 Docker、Kubernetes 或系统环境变量注入密钥。

如果需要重新生成本地配置，先复制示例文件，并在 `.env` 中填写 PostgreSQL 和应用加密密钥：

```powershell
Copy-Item config.example.yaml config.yaml
Copy-Item .env.example .env
# 将下面命令的输出写入 .env 的 APP_ENCRYPTION_KEY
go run ./cmd/tapd-dingding -command generate-key
go mod tidy
go run ./cmd/tapd-dingding -config config.yaml
```

## Docker Compose 部署

项目提供 `Dockerfile` 和 `compose.yaml`，会启动两个服务：

- `postgres`：PostgreSQL 18.6，数据持久化到 Docker volume `tapd-dingding-postgres`；
- `app`：TAPD → 钉钉通知服务，应用端口默认只绑定到服务器本机的 `127.0.0.1:8080`。

首次部署时复制配置模板并生成应用密钥：

```bash
cp .env.example .env
cp config.example.yaml config.yaml
docker build --tag tapd-dingding:local .
docker run --rm tapd-dingding:local -command generate-key
```

将生成的密钥填入 `.env` 的 `APP_ENCRYPTION_KEY`，同时设置 `POSTGRES_PASSWORD`，并让 `DATABASE_URL` 中的密码与其一致。然后启动：

```bash
docker compose up -d --build
docker compose ps
curl http://127.0.0.1:8080/healthz
```

Compose 默认使用 `host.docker.internal:8000` 访问宿主机上的 TAPD MCP 服务；如果 MCP 在其他容器中运行，请把 `.env` 中的 `TAPD_MCP_URL` 改成对应的 Compose 服务地址。PostgreSQL 不直接发布到公网，只在 Compose 内部网络中提供服务。

## CI/CD

`.github/workflows/ci-cd.yml` 会在 Pull Request 和推送到 `master` 时运行 `go test ./...`、`go vet ./...`，并验证 Compose 配置和应用镜像构建。推送到 `master` 且检查通过后，工作流会通过 SSH 连接部署服务器，在 `/home/ubuntu/tapd-dingding` 更新代码并执行 `docker compose up -d --build --remove-orphans`。

GitHub 仓库需要配置以下 Actions Secrets：

```text
DEPLOY_HOST   服务器 IP 或域名
DEPLOY_USER   SSH 用户名
DEPLOY_SSH_KEY 对应服务器 authorized_keys 的私钥
```

服务器上的 `.env` 和 `config.yaml` 不纳入 Git，由部署服务器自行保留。

本机 PostgreSQL 已验证为 18.4，服务使用独立的 `tapd_app` 用户连接 `tapd_dingding` 数据库，地址为 `127.0.0.1:5432`。服务启动时会自动创建状态表和连接表，连接配置通过下面的两个 HTTP 接口加密写入数据库。

```text
GET http://127.0.0.1:8080/healthz
GET http://127.0.0.1:8080/readyz
GET http://127.0.0.1:8080/metrics
```

连接管理接口：

```text
POST http://127.0.0.1:8080/api/connections/tapd
POST http://127.0.0.1:8080/api/connections/dingtalk
POST http://127.0.0.1:8080/api/recipients/tapd
GET http://127.0.0.1:8080/api/recipients/tapd?tapd_connection_id=6
```

这两个接口会接收敏感凭据，只应通过内网或已有的管理网关访问，不能直接暴露到公网。

写入 TAPD 连接：

```json
{
  "name": "研发项目",
  "access_token": "your-personal-token",
  "statuses": ["new", "open", "in_progress"],
  "fields": "id,title,description,priority,priority_label,severity,module,status,reporter,current_owner,de,fixer,te,confirmer,cc,participator,created,modified,workspace_id"
}
```

写入钉钉连接：

```json
{
  "name": "研发群机器人",
  "url": "https://oapi.dingtalk.com/robot/send?access_token=...",
  "secret": "..."
}
```

写入 TAPD 账号与钉钉 @ 映射：

```json
{
  "tapd_connection_id": 6,
  "tapd_account": "lim1",
  "name": "李明",
  "dingtalk_user_id": "024401420615845952"
}
```

映射按 `tapd_connection_id + tapd_account` 唯一保存。服务扫描时会读取对应 TAPD Token 下的映射；当缺陷的 `current_owner`、`reporter` 等字段命中 `lim1` 时，自动把 `dingtalk_user_id` 放入钉钉消息的 `atUserIds`。数据库中存在该 Token 的映射时，会优先使用数据库映射而不是 monitor 里的静态 `recipients`。

接口返回连接 `id` 后，把对应 ID 填入 monitor 的 `tapd_connection_id` 和 `dingtalk_connection_id`。服务默认调用 `http://localhost:8000/mcp/` 上的 TAPD Streamable HTTP MCP 服务；TAPD MCP 会先发现当前 Token 可访问的项目，再查询这些项目的 Bug，不需要在连接中填写固定 `workspace_id`。

钉钉通知通过服务内 FIFO 队列发送，默认两条消息至少间隔 3 秒；遇到钉钉限流错误 `660026` 时会自动延迟重试。可在 `server` 中调整：

```yaml
server:
  dingtalk_min_interval: 3s
  dingtalk_queue_size: 100
  dingtalk_rate_limit_retry: 30s
```

## @ 对应的人

钉钉 Webhook 支持 `atUserIds` 和 `atMobiles`。TAPD 的 `current_owner`、`reporter` 等字段是 TAPD 账号，通常不能直接当作钉钉用户 ID，所以通过 `recipients` 显式映射：

```yaml
recipients:
  - name: "张三"
    tapd_accounts: ["zhangsan"]
    user_id: "钉钉内部userid"
    mobile: "13800000000"
```

服务读取 `mention_fields` 指定的缺陷字段，匹配 `tapd_accounts` 后，把对应的 `user_id` / `mobile` 放进消息的 `at` 字段，并在正文加入 `@钉钉userid(张三)` 或 `@手机号`。被 @ 的人必须已经在对应钉钉群中。只有手机号时填 `mobile`；能从钉钉通讯录/API 获取稳定 `userid` 时优先填 `user_id`。

如果要始终 @ 某些人，把他们的 `name` 放进 `default_recipients`；如果只想按负责人通知，把 `mention_fields` 改成 `["current_owner"]`。

## 去重、首次扫描和重试

通知唯一键为 `monitor_name + bug_id + fingerprint`。fingerprint 默认包含缺陷 ID、标题、状态和 modified 时间：

- 同一个缺陷同一版本只发一次，服务重启不会重复发送。
- `notify_on_changes: true` 时，TAPD 缺陷修改后会按新 fingerprint 再通知。
- 钉钉发送失败会写入失败状态，下一次扫描会重新尝试。
- `notify_existing: true` 会在首次运行时通知当前已有的匹配缺陷；改成 `false` 会建立基线，不会首轮刷屏。

## 新增 Bug 和每日汇总

服务会在 `tapd_bug_observations` 中记录每个 monitor 第一次和最近一次观察到某个 Bug 的时间。日志和 `/metrics` 会标记首次观察到的 Bug；服务重启不会重复判定为新增。这里的“新增”是“首次被本服务观察到”，不是 TAPD 创建时间本身。

每个 monitor 默认每天 `09:30` 和 `18:00` 发送当前监控范围内的全部 Bug 汇总，时间使用 `server.timezone`，默认是 `Asia/Shanghai`。可通过 `daily_report_times` 调整，例如：

```yaml
server:
  timezone: "Asia/Shanghai"

monitors:
  - name: "研发项目"
    daily_report_times: ["09:30", "18:00"]
```

Bug 查询范围由 `bug_scope` 控制：`all` 查询当前 Token 可见项目中的缺陷，`mine` 使用 TAPD 当前账号的待办缺陷。若只希望通知自己的缺陷，设置 `bug_scope: "mine"`；日报也会使用相同范围。

日报发送状态保存在 `tapd_daily_reports`，服务重启或多实例运行不会在同一时段重复发送；发送失败会在该时段的短暂重试窗口内重试。

## 设计说明

TAPD 使用 MCP 的项目发现和 Bug 查询工具。服务端直接作为 MCP Client 调用，不需要接入 AI 或额外的大模型服务。查询失败不会阻塞其他 monitor；日志使用 JSON 结构化输出，包含 monitor、workspace、bug_id、耗时和错误信息，token 和 Webhook secret 不会写入日志。

当前实现使用钉钉群自定义机器人 Webhook，适合已有机器人配置的场景。如果后续要创建机器人、查通讯录或发单聊消息，应改用钉钉应用机器人 OpenAPI。

## 项目结构与实现约定

```text
cmd/tapd-dingding/       程序入口与进程生命周期
internal/config/         YAML、环境变量、默认值与配置校验
internal/crypto/         应用密钥与 AES-256-GCM 封装
internal/database/       PostgreSQL 连接、迁移与持久化仓储
internal/tapd/           TAPD MCP 客户端、工具发现与结果解析
internal/dingtalk/       钉钉 Webhook 客户端与消息模型
internal/service/        HTTP 管理接口、调度、扫描、消息渲染、指标和发送队列
```

实现遵循以下约定：配置加载阶段启用未知字段检查并完成默认值校验；敏感连接只在数据库中保存密文；外部请求使用调用方 `context.Context`，错误按层补充上下文并保留原始错误；数据库认领操作必须是幂等的原子 SQL；消息发送统一经过 FIFO 限流队列；改动后使用 `gofmt`、`go vet`、`staticcheck` 和 `go test ./...` 验证。

## TAPD 认证和加密

服务通过 TAPD MCP 的个人 Token 认证，不再读取 TAPD 用户名、密码、client ID、client secret、MCP URL 或 workspace ID。

monitor 只需要引用数据库中的连接：

```yaml
tapd_connection_id: 1
dingtalk_connection_id: 1
```

TAPD 和钉钉连接的敏感字段都使用 AES-256-GCM 加密写入数据库，密钥由 `APP_ENCRYPTION_KEY` 提供。数据库泄露时无法直接还原凭据，但必须妥善保护应用加密密钥。
