# Recodex Bridge

Recodex Bridge 是 Remote Codex Companion 的 Go 端桥接服务。它运行在开发机上，为移动端或其他客户端提供一个受控入口，用来连接 Codex CLI、访问允许的工作区、查看会话流式输出，并执行常见 Git 操作。

Bridge 可以作为客户端接入远程 Relay 服务，用于 Bridge 和 App 无法直接互连时的外网通信。Relay 只转发不透明 WebSocket 消息，Recodex 的认证和业务协议仍由 Bridge/App 处理。

## 功能

- 本地 HTTP 服务：健康检查、版本信息、配对信息。
- WebSocket JSON 信封协议。
- 短期配对 Token 和长期设备 Key。
- 已配对设备列表与撤销。
- 自动读取 Codex 已记录的项目作为工作区。
- 通过 `codex exec --json` 启动 Codex 会话并流式返回事件。
- 会话索引持久化。
- Git 状态、Diff、日志、提交和推送封装。
- Git 写操作默认要求客户端显式确认。
- 可配置远程 Relay 客户端。

## 项目结构

```text
cmd/
  rcc-bridge/   Bridge 服务入口
internal/
  api/          HTTP 和 WebSocket API
  auth/         设备配对、设备 Key 存储与校验
  codex/        Codex CLI 适配器
  config/       YAML 配置加载与默认值
  gitops/       Git 命令封装
  relayclient/  远程 Relay 签名和连接
  session/      Codex 会话管理与持久化
  workspace/    工作区解析
docs/
  protocol.md   通信协议说明
  security.md   安全设计说明
```

## 常用运行方式

如果常用场景是手机通过网络控制公司电脑里的 Codex，推荐直接在项目根目录启动 Bridge：

```bash
go run . -config config.yaml
```

默认 Bridge 地址为 `http://127.0.0.1:8765`。如果配置了 `relay.enabled: true`，Bridge 还会主动连接远程 Relay 房间。启动后，日志会打印 Bridge 的配对 Token。客户端可以手动输入该 Token，也可以请求 `/pairing` 获取 `recodex://pair` URI 后生成二维码。

## 单独运行 Bridge

先确认本机已经安装并可直接执行 `codex` 命令，然后启动服务：

```bash
go run ./cmd/rcc-bridge -config config.yaml
```

默认监听地址为：

```text
http://127.0.0.1:8765
```

常用接口：

- `GET /healthz`
- `GET /version`
- `GET /pairing`
- `GET /relay`
- `GET /context`
- `GET /workspaces`
- `GET /devices`
- `GET /sessions`
- `GET /sessions/{id}/events`
- `GET /git/status?workspace=<path-or-name>`
- `GET /git/diff?workspace=<path-or-name>`
- `POST /git/commit`
- `POST /git/push`
- `POST /git/undo`
- `WS /ws`

## 配置

配置文件为 `config.yaml`，可以调整监听地址、Codex 二进制路径、状态目录、安全选项和远程 Relay。

工作区默认会从 `~/.codex/config.toml` 的 `[projects."路径"]` 自动读取，只加入本机真实存在的目录。通常不需要在 `config.yaml` 里维护项目路径。

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

security:
  pairing_enabled: true
  pairing_ttl_seconds: 300
  require_confirm_for_git_write: true
```

远程 Relay 示例：

```yaml
relay:
  enabled: true
  url: "wss://relay.example.com/relay"
  public_url: "wss://relay.example.com/relay"
  room_id: "recodex-your-room"
  room_token: "<optional bridge roomToken>"
  client_id: "<bridge clientId>"
  client_secret: "<bridge clientSecret>"
  client_type: "bridge"
  reconnect_seconds: 5
```

本地调试时可以把 `url` 指向本机 `relay-go`：

```yaml
relay:
  enabled: true
  url: "ws://127.0.0.1:8788/relay"
  public_url: "ws://127.0.0.1:8788/relay"
  room_id: "recodex-local"
  client_type: "bridge"
```

`url` 是 Bridge 自己拨号使用的地址，`public_url` 是 `/pairing` 和 `/relay` 返回给客户端看的地址。远程第三方 Relay 通常两者一样；如果 Bridge 走内网地址、App 走公网反代地址，就把 `public_url` 设置成公网 `wss://` 地址。

如果需要补充 Codex 未记录的目录，也可以手动添加 `workspaces`：

```yaml
workspaces:
  - name: "my-project"
    path: "/path/to/my-project"
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

## 远程 Relay

如果使用 `relay-go` 或兼容的第三方 Relay 服务，Bridge 可以在 `config.yaml` 中配置为 Relay 客户端：

```yaml
relay:
  enabled: true
  url: "wss://relay.example.com/relay"
  public_url: "wss://relay.example.com/relay"
  room_id: "recodex-room"
  room_token: "<optional bridge roomToken>"
  client_id: "<bridge clientId>"
  client_secret: "<保存一次的 clientSecret>"
  client_type: "bridge"
```

启动 Bridge 后，它会用 `clientId + "\n" + clientType + "\n" + roomId + "\n" + timestamp + "\n" + nonce` 生成 HMAC-SHA256 签名并加入该房间。App 端需要使用同账号下 `clientType=app` 的客户端凭证连接同一个 `room_id`，然后发送原有的 `auth.hello`、`workspace.list`、`session.start` 等消息。很多 Relay 实现同一房间只允许一个 `bridge` 在线，所以不要用 Bridge 凭证模拟 App 连接。

## 安全约束

- Bridge 默认只监听 `127.0.0.1`。
- 工作区默认来自 Codex 已记录项目，也可以通过 `config.yaml` 追加。
- 配对 Token 短期有效。
- 已配对设备的长期 Key 保存在状态目录中。
- 已认证客户端可以列出和撤销设备。
- Git 写操作默认需要 `confirm: true`。
- 命令执行使用 `exec.CommandContext` 和参数数组，不拼接 Shell 字符串。
- 远程 Relay 只应转发不透明载荷，业务认证仍由 Bridge/App 完成。

更多说明见 [docs/security.md](docs/security.md)。

## 测试

```bash
go test ./...
```
