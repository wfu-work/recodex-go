# Security Notes

- The bridge listens on `127.0.0.1` by default.
- Workspace access is restricted to the YAML allowlist.
- Pairing uses a short-lived token from `/pairing`.
- `/pairing` also exposes a `recodex://pair` URI for QR-based setup.
- Paired devices receive a long-lived device key stored in `.recodex/devices.json`.
- Authorized devices can be listed and revoked over authenticated WebSocket messages.
- Git write operations require an explicit `confirm: true` payload.
- Commands use `exec.CommandContext` with argument arrays rather than shell strings.
- Remote Relay should only forward opaque WebSocket payloads. Recodex authentication and authorization remain owned by Bridge/App.
- Prefer `wss://` for remote Relay URLs outside local development.

For LAN usage, change `server.host` to `0.0.0.0`, restart the bridge, and pair again from the mobile app.
