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

- `GET /api/healthz`
- `GET /api/version`
- `GET /api/pairing`
- `GET /api/context`
- `GET /api/workspaces`
- `GET /api/devices`
- `GET /api/sessions?limit=100&offset=0`
- `GET /api/sessions/{id}/events?limit=1000`
- `GET /api/git/status?workspace=<path-or-name>`
- `GET /api/git/diff?workspace=<path-or-name>`
- `POST /api/git/commit`
- `POST /api/git/push`
- `POST /api/git/undo`
- `GET /api/relay`

These HTTP endpoints support the bundled web console and, except for health and version, only accept loopback requests. Remote devices use authenticated WebSocket messages. Git write endpoints still honor `security.require_confirm_for_git_write`.

Session lists default to 100 records and return `nextOffset` when another page exists. Session events default to the most recent 1000 records. The list limit is capped at 200 and the event limit at 5000.

`GET /api/pairing` returns:

```json
{
  "baseUrl": "http://127.0.0.1:8765/api",
  "token": "pairing-token",
  "pairingUri": "recodex://pair?baseUrl=http%3A%2F%2F127.0.0.1%3A8765%2Fapi&token=pairing-token",
  "lanHost": "192.168.1.20"
}
```

## WebSocket

Connect to `ws://<host>:8765/api/ws`.

The first client message must be `auth.hello`.

### Pairing

```json
{
  "type": "auth.hello",
  "id": "auth_001",
  "payload": {
    "deviceId": "phone_1",
    "deviceName": "iPhone",
    "token": "<token from the local /api/pairing endpoint>"
  }
}
```

The bridge returns `auth.ok` with `deviceKey`. The pairing token is single-use, so a successful pairing immediately invalidates it and creates a fresh local token for the next device. The app stores the device key and uses it on later connects:

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
ws://<relay-host>/relay/<roomId>?clientId=...&clientType=bridge|app&timestamp=...&nonce=...&signature=...
```

For `relay-go`, the signature payload is:

```text
clientId + "\n" + clientType + "\n" + roomId + "\n" + timestamp + "\n" + nonce
```

The signature is `hex(hmac_sha256(clientSecret, signPayload))`.

The Recodex Bridge joins the configured room as `clientType=bridge` and continues to handle the same Recodex JSON envelopes after the relay connection is established. App clients must join the same `roomId` with an `app` client credential, then send `auth.hello` and the normal bridge messages through the relay. The relay forwards opaque messages between peers in the same room and does not wrap business messages.
