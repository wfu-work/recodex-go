# Recodex Bridge

Recodex Bridge 是 Remote Codex Companion 的 Go 端桥接服务。它运行在开发机上，为移动端或其他客户端提供一个受控入口，用来连接 Codex CLI、访问允许的工作区、查看会话流式输出，并执行常见 Git 操作。

项目还包含一个轻量级 Relay 服务，用于在同一房间内转发 WebSocket 消息。Relay 不解析、不存储业务数据，适合在 Bridge 和 App 之间需要中继连接时使用。

## 功能

- 本地 HTTP 服务：健康检查、版本信息、配对信息。
- WebSocket JSON 信封协议。
- 短期配对 Token 和长期设备 Key。
- 已配对设备列表与撤销。
- 工作区白名单限制。
- 通过 `codex exec --json` 启动 Codex 会话并流式返回事件。
- 会话索引持久化。
- Git 状态、Diff、日志、提交和推送封装。
- Git 写操作默认要求客户端显式确认。
- Relay 房间消息转发。

## 项目结构

```text
cmd/
  rcc-bridge/   Bridge 服务入口
  rcc-relay/    Relay 服务入口
internal/
  api/          HTTP 和 WebSocket API
  auth/         设备配对、设备 Key 存储与校验
  codex/        Codex CLI 适配器
  config/       YAML 配置加载与默认值
  gitops/       Git 命令封装
  relay/        Relay 房间和连接管理
  session/      Codex 会话管理与持久化
  workspace/    工作区白名单解析
docs/
  protocol.md   通信协议说明
  security.md   安全设计说明
```

## 常用运行方式

如果常用场景是手机通过网络控制公司电脑里的 Codex，推荐直接在项目根目录启动组合服务：

```bash
go run . -config bridge.yaml
```

它会在同一个进程里同时启动：

- Bridge：默认 `http://127.0.0.1:8765`
- Relay：默认 `http://127.0.0.1:8787`

可以用 `-relay-addr` 修改 Relay 监听地址：

```bash
go run . -config bridge.yaml -relay-addr 0.0.0.0:8787
```

启动后，日志会打印 Bridge 的配对 Token。客户端可以手动输入该 Token，也可以请求 `/pairing` 获取 `recodex://pair` URI 后生成二维码。

## 单独运行 Bridge

先确认本机已经安装并可直接执行 `codex` 命令，然后启动服务：

```bash
go run ./cmd/rcc-bridge -config bridge.yaml
```

默认监听地址为：

```text
http://127.0.0.1:8765
```

常用接口：

- `GET /healthz`
- `GET /version`
- `GET /pairing`
- `WS /ws`

## 配置

配置文件为 `bridge.yaml`，可以调整监听地址、Codex 二进制路径、状态目录、工作区白名单和安全选项。

示例：

```yaml
server:
  host: "127.0.0.1"
  port: 8765

codex:
  mode: "cli"
  binary: "codex"

state:
  dir: ".recodex"

workspaces:
  - name: "recodex-go"
    path: "/path/to/recodex-go"

security:
  pairing_enabled: true
  pairing_ttl_seconds: 300
  require_confirm_for_git_write: true
```

如果要让局域网内其他设备连接，把 `server.host` 改为 `0.0.0.0`：

```yaml
server:
  host: "0.0.0.0"
  port: 8765
```

然后在客户端中使用开发机的局域网地址，例如：

```text
http://192.168.1.20:8765
```

## 配对流程

1. 启动 `rcc-bridge`。
2. 查看控制台输出的 pairing token，或访问 `GET /pairing`。
3. 客户端通过 `auth.hello` 发送设备信息和配对 Token。
4. Bridge 返回 `deviceKey`，客户端保存该 Key。
5. 后续连接使用 `deviceId` 和 `deviceKey` 认证，不再需要配对 Token。

配对 Token 默认有效期为 300 秒，可以通过 `security.pairing_ttl_seconds` 修改。

## WebSocket 消息

所有 WebSocket 消息都使用 JSON 信封：

```json
{
  "type": "workspace.list",
  "id": "msg_001",
  "payload": {}
}
```

客户端连接 `ws://<host>:8765/ws` 后，第一条消息必须是 `auth.hello`。

已支持的客户端消息：

- `workspace.list`
- `device.list`
- `device.revoke`
- `session.list`
- `session.start`
- `session.prompt`
- `session.interrupt`
- `git.status`
- `git.diff`
- `git.commit`
- `git.push`

更详细的协议示例见 [docs/protocol.md](docs/protocol.md)。

## 单独运行 Relay

```bash
go run ./cmd/rcc-relay -addr 127.0.0.1:8787
```

Relay 接口：

- `GET /healthz`
- `WS /relay/<room>`

同一 `<room>` 内的多个 WebSocket 连接会互相收到对方发送的文本或二进制消息。

## 安全约束

- Bridge 默认只监听 `127.0.0.1`。
- 工作区访问受 `bridge.yaml` 白名单限制。
- 配对 Token 短期有效。
- 已配对设备的长期 Key 保存在状态目录中。
- 已认证客户端可以列出和撤销设备。
- Git 写操作默认需要 `confirm: true`。
- 命令执行使用 `exec.CommandContext` 和参数数组，不拼接 Shell 字符串。
- Relay 只转发不透明载荷，不持久化业务数据。

更多说明见 [docs/security.md](docs/security.md)。

## 测试

```bash
go test ./...
```
