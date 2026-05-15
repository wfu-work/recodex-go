# Remote Codex Companion Protocol

All WebSocket messages are JSON envelopes:

```json
{
  "type": "workspace.list",
  "id": "msg_001",
  "payload": {}
}
```

## HTTP

- `GET /healthz`
- `GET /version`
- `GET /pairing`
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

These HTTP endpoints are available without WebSocket authentication for the bundled local web console. Git write endpoints still honor `security.require_confirm_for_git_write` and return `confirm.required`-style payloads when confirmation is required.

`GET /pairing` returns:

```json
{
  "baseUrl": "http://127.0.0.1:8765",
  "token": "pairing-token",
  "pairingUri": "recodex://pair?baseUrl=http%3A%2F%2F127.0.0.1%3A8765&token=pairing-token",
  "lanHost": "192.168.1.20"
}
```

## WebSocket

Connect to `ws://<host>:8765/ws`.

The first client message must be `auth.hello`.

### Pairing

```json
{
  "type": "auth.hello",
  "id": "auth_001",
  "payload": {
    "deviceId": "phone_1",
    "deviceName": "iPhone",
    "token": "<token from /pairing>"
  }
}
```

The bridge returns `auth.ok` with `deviceKey`. The app stores that key and uses it on later connects:

```json
{
  "type": "auth.hello",
  "id": "auth_002",
  "payload": {
    "deviceId": "phone_1",
    "deviceKey": "<saved key>"
  }
}
```

### Supported Client Messages

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

### Relay

Relay connections use:

```text
ws://<relay-host>:8787/relay/<room>
```

The relay forwards opaque messages between peers in the same room. Encryption and higher-level authentication are owned by Bridge/App messages, not by the relay.
