# Recodex Bridge

Go bridge service for Remote Codex Companion.

## Run

```bash
go run ./cmd/rcc-bridge -config bridge.yaml
```

The service listens on `127.0.0.1:8765` by default.

Useful endpoints:

- `GET /healthz`
- `GET /version`
- `GET /pairing`
- `WS /ws`

On startup the bridge prints a pairing token. Enter that token in the Flutter app on the Device page, or fetch `/pairing` and scan the returned `recodex://pair` URI as a QR code.

## Config

Edit `bridge.yaml` to change the host, port, Codex binary, state directory, and workspace allowlist.

To connect from another device on the LAN, set:

```yaml
server:
  host: "0.0.0.0"
  port: 8765
```

Then use the computer's LAN address in the Flutter app, for example `http://192.168.1.20:8765`.

## Implemented

- HTTP health, version, and pairing endpoints.
- WebSocket JSON envelope protocol.
- Pairing token and stored device keys.
- Device list and revoke messages.
- Workspace allowlist enforcement.
- `codex exec --json` session streaming.
- Session index persistence.
- Git status, diff, log, commit, and push wrappers.
- Confirmation requirement for Git write operations.

## Relay

```bash
go run ./cmd/rcc-relay -addr 127.0.0.1:8787
```

Relay endpoints:

- `GET /healthz`
- `WS /relay/<room>`

The relay forwards opaque text/binary WebSocket messages to other peers in the same room. It intentionally does not inspect or persist business payloads.
