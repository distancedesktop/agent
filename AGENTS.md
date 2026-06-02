# AGENTS.md

## captured (../captured/)

Dumb screen capture daemon. Unix sockets in `/tmp/`. No deps beyond Go stdlib.

- **Control**: `/tmp/captured.socket` — JSON over Unix socket. Commands: `list-displays`, `start-stream`, `stop-stream`, `info`.
- **Media**: `/tmp/captured-media.socket` — raw BGRA frames: `[4B width][4B height][w*h*4 BGRA]`. Only active during a stream.
- **Run**: `go run .` (optionally `--listen :9090` for TCP control).
- **Pipeline interface**: `pipelines/pipeline.go`. Per-platform impls in `pipelines/{macos,linux,windows}/`. Only macOS done (uses `sckit-go` for ScreenCaptureKit).

## agent (this dir)

Reads BGRA from captured, GPU-encodes via ffmpeg, publishes over WebTransport/QUIC.

### Ports

| Port | Transport | Purpose |
|------|-----------|---------|
| 52020 | UDP | WebTransport — `/wt` for JSON control, `/moq` for MoQ media (same QUIC conn) |
| 52022 | TCP | Web UI (plain HTTP, fingerprint display) |

### Protocol

**Endpoint**: `https://<server>:52020/wt` (WebTransport, QUIC over UDP)

Each client gets one `WebTransport` session with:
- **1 bidirectional stream** for JSON control messages (client-initiated)
- **1 unidirectional stream** for binary video (server-initiated after `start`)

JSON control messages (bidirectional stream):
```json
{"type":"list-displays"} → {"type":"displays","displays":[...]}
{"type":"start","display_id":0,"fps":60} → {"type":"started","width":1920,"height":1080,"codec":"h264"}
{"type":"stop"} → {"type":"stopped"}
{"type":"stream-ended"}  // sent to all subscribers on stop
{"type":"fingerprint-refresh","algorithm":"sha-256","fingerprint":"<hex>"}  // sent on connect + cert rotation
```

Video: 64KB chunks of H.264/H.265/AV1 Annex B byte stream over the unidirectional stream.

**Media over QUIC (MoQ)**: `https://<server>:52020/moq` — separate WebTransport session using `@moq/lite`.
- gomoqt `WebTransportHandler` with `UpgradeFunc` wrapping via `okdaichi/webtransport-go`
- `PublishFunc("/video", ...)` registers each subscriber's `TrackWriter`
- Each ffmpeg chunk → one MoQ group → one frame
- Old uni-stream model kept for backwards compat; MoQ runs alongside it.
- gomoqt v0.15.0, falls back to IETF/moql mode (no ALPN h3/moq).

### Cert system

- Self-signed ECDSA P-256 certificate, 13-day validity
- SHA-256 fingerprint of DER-encoded cert
- Auto-rotation: checks every hour, generates new cert if < 7 days left
- On rotation, sends `fingerprint-refresh` to all connected clients
- Storage: `~/.config/distancedesktop/ec-cert.pem`, `ec-key.pem`
- No CA chain, no `security add-trusted-cert` needed

### Client connection (browser JavaScript)
```js
const transport = new WebTransport(`https://${ip}:52020/wt`, {
  serverCertificateHashes: [{
    algorithm: "sha-256",
    value: new Uint8Array(fingerprintBytes)
  }]
});
await transport.ready;
const stream = await transport.createBidirectionalStream();
```

MoQ video connection:
```js
const moqTransport = new WebTransport(`https://${ip}:52020/moq`, {
  serverCertificateHashes: [{
    algorithm: "sha-256",
    value: new Uint8Array(fingerprintBytes)
  }]
});
await moqTransport.ready;
// Use @moq/lite to subscribe to "/video"
```

### Web UI

Embedded HTML at `http://<server>:52022/` showing:
- SHA-256 fingerprint in hex
- Server LAN IPs
- QR code for mobile scanning

### Flags

| Flag | Purpose |
|------|---------|
| `--addr :52020` | WebTransport listen address (UDP) |
| `--web :52022` | Web UI listen address (TCP) |
| `--fingerprint` | Print SHA-256 fingerprint and exit |
| `--cert cert.pem` | Custom TLS certificate (ECDSA P-256 PEM) |
| `--key key.pem` | Custom TLS private key (ECDSA P-256 PEM) |

### Architecture

```
captured (Unix sockets)
  └─ raw BGRA frames → agent
                         ├─ ffmpeg (GPU encode via VideoToolbox/NVENC/AMF/QSV/VAAPI/libx264)
                         │    └─ H.264/H.265/AV1 Annex B byte stream → stdout
                         └─ publishStream goroutine
                              ├─ writes 64KB chunks to each subscriber's unidirectional stream
                              └─ writes each chunk as a MoQ frame/group to each MoQ TrackWriter
```

### Start sequence
1. Client opens WebTransport session → opens bidirectional stream → sends `{"type":"start"}`
2. Agent dials `/tmp/captured.socket` → sends `start-stream`
3. Captured returns media socket path → agent connects → reads first frame (header + data) for dimensions
4. Agent spawns `ffmpeg` with correct `-s WxH`, writes first frame, starts BGRA reader goroutine for subsequent frames
5. Agent opens unidirectional stream on the caller's session → adds caller as owner + subscriber
6. ffmpeg stdout read in 64KB chunks → each chunk written to all subscriber unidirectional streams
7. Agent responds to caller with `{"type":"started","width":...,"height":...,"codec":"h264"}`

### Stop sequence
1. Any client sends `{"type":"stop"}` → cancel publish context → close ffmpeg stdin (EOF) → `cmd.Wait()`
2. Close media socket → send `stop-stream` to captured → close control socket
3. Send `{"type":"stream-ended"}` to all subscribers → close all sessions
4. Set `state = nil`

### Owner disconnect
- If the client that started the stream disconnects, stream is torn down automatically
- All subscribers receive `stream-ended`

### Late joiners
- Clients connecting while a stream is active are auto-registered as subscribers (no video until they send `start`)
- On `start`, agent opens a unidirectional stream on their session for video

### Fingerprint refresh flow
1. Cert rotation goroutine detects < 7 days remaining
2. Generates new ECDSA P-256 cert, saves to disk
3. Sends `{"type":"fingerprint-refresh","algorithm":"sha-256","fingerprint":"<hex>"}` to all connected subs
4. Client caches additional fingerprint
5. On next connection, includes both old and new hashes in `serverCertificateHashes`

## MoQ integration

- `/moq` on same UDP port as `/wt` — separate WebTransport session using gomoqt.
- Each MoQ subscriber gets a `*moqt.TrackWriter` via `PublishFunc("/video", ...)`.
- `publishStream` writes each ffmpeg chunk to all `TrackWriter`s (one MoQ group + one frame per chunk).
- On teardown, `moqBroadcastCancel()` unregisters the publish handler.
- Server TLS `NextProtos` stays `["h3"]` — client MoQ WebTransport negotiates `h3`, `@moq/net` falls back to IETF/moql mode.

## Dependencies

- `github.com/okdaichi/webtransport-go` — WebTransport over QUIC/HTTP-3 (fork used by gomoqt)
- `github.com/quic-go/quic-go` — QUIC transport layer
- `github.com/qumo-dev/gomoqt` — Media over QUIC (MoQ) transport

## Build

```sh
go build -o agent .
```
