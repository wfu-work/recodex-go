# Security Notes

- The bridge listens on `127.0.0.1` by default.
- Workspace access is restricted to manual YAML entries plus existing Codex projects marked `trust_level = "trusted"`.
- Pairing uses a short-lived token from `/api/pairing`.
- Pairing tokens are single-use and `/api/pairing` is restricted to the local machine.
- `/api/pairing` also exposes a `recodex://pair` URI for QR-based setup.
- Paired devices receive a long-lived device key stored in `.recodex/devices.json`.
- Authorized devices can be listed and revoked over authenticated WebSocket messages.
- Git write operations require an explicit `confirm: true` payload.
- Commands use `exec.CommandContext` with argument arrays rather than shell strings.
- Codex prompts are sent through stdin so prompt text cannot become a CLI option.
- HTTP control APIs are loopback-only; remote devices use authenticated WebSocket messages.
- Browser origins are checked and wildcard CORS/WebSocket origin acceptance is not enabled.
- WebSocket messages, request bodies, Git output, session pages, and event pages have size limits.
- Remote Relay should only forward opaque WebSocket payloads. Recodex authentication and authorization remain owned by Bridge/App.
- Prefer `wss://` for remote Relay URLs outside local development.

For LAN usage, change `server.host` to `0.0.0.0`, restart the bridge, and use the token or QR shown by the local Web console. LAN clients connect to `/api/ws`; they cannot call the local HTTP control API directly.
