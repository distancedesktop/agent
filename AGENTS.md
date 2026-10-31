# AGENTS.md

## captured (../captured/)

Capture and encode in one process. The agent spawns it per stream (and briefly for display listing). See `../captured/README.md` for the full protocol.

- **Control**: JSON-RPC 2.0 lines on captured's stdin/stdout. Methods `list`, `get`, `set`, `start`, `stop`, plus a `stopped` notification. Properties are generic keys like `video.bitrate`, `video.codec`, `video.encoder`, `stats.encode_avg_ms`.
- **Media**: encoded records on fd 3 (an inherited handle passed with `--media fd:<handle>` on Windows): `[u32 BE len][u8 type][u8 flags][u16 0][u64 BE pts µs][payload]`. Type 0 is one access unit (flag bit 0 keyframe), type 2 is a format JSON.
- **Build**: cgo with libavcodec/libavutil/libswscale dev packages. `kms_nvidia` needs `setcap cap_sys_admin+ep` on the binary.

## agent (this dir)

Spawns captured, forwards its encoded access units over WebTransport/QUIC.

### Ports

| Port | Transport | Purpose |
|------|-----------|---------|
| 52020 | UDP | WebTransport — `/wt` for JSON control + uni-stream media (QUIC over UDP) |
| 52022 | TCP | Web UI (HTTPS, fingerprint display) |

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

Video uni-stream records use one record per access unit:

| Bytes | Description |
|-------|-------------|
| 1 | Flags; bit 0 is set when the access unit contains an IDR NAL |
| 8 | Timestamp in milliseconds since the stream started, big-endian uint64 |
| 4 | Access-unit payload length, big-endian uint32 |
| N | Annex B access unit payload, including start codes |

The client may receive records split across reads and must parse each complete
record before decoding it.

`start` also accepts optional `codec` and `bitrate`.

On connect the agent pushes `fingerprint-refresh` (only when it manages its own
cert) followed by an **unsolicited `displays`**, before the client asks for
anything. Clients must tolerate `displays` arriving unprompted.

Unrecognized message types are answered with
`{"type":"error","message":"unknown type: <t>"}`. There is currently no `input`
or `ping` handler.

### Origin checks

WT upgrades accept an empty `Origin`, an `Origin` matching the request `Host`,
and the agent's own `https://<host>:<webPort>`. A viewer served from any other
origin (reverse proxy, separate web deployment) is rejected unless that origin is
passed with `--allow-origin`.

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

**Note**: MoQ integration has been removed. The agent now publishes video exclusively over WebTransport unidirectional streams using the framed H.264 format above.

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
| `--backend auto\|captured\|sunshine\|vnc\|rdp` | Video backend (auto probes in order captured → sunshine → vnc → rdp) |
| `--captured "bin=...,pipeline=...,video.encoder=..."` | captured binary (default `$CAPTURED_BIN`, then `captured` on PATH) and any captured properties to set on start |
| `--sunshine "addr=host:47989"` | Sunshine/Moonlight host address |
| `--vnc "addr=host:5901"` | VNC server address |
| `--rdp "addr=host:3389"` | RDP server address |
| `--allow-origin <origin>` | Additional allowed browser `Origin` for WT upgrades (repeatable; `*` allows any). Needed when the viewer is hosted somewhere other than the agent's own `:52022`, e.g. behind a reverse proxy. |
| `--dry-run` | List displays via the selected backend and exit |

### Architecture

```text
backend.Backend { ListDisplays(ctx) ([]Display,error); StartStream(ctx, StartRequest) (Stream,error); Stream.Chunks() <-chan H264Chunk }   (src/backend/backend.go)
  ├─ captured  — spawned captured child, capture + encode in one process (src/backend/captured.go)
  ├─ sunshine  — Moonlight RTSP passthrough H264 (STUB, src/backend/sunshine.go)
  ├─ vnc       — RFB frame polling (STUB, src/backend/vnc.go)
  └─ rdp       — MS-RDPBCGR (STUB, src/backend/rdp.go)

activeBackend (chosen via --backend at startup):
  └─ StartStream → Stream.Chunks() channel
       └─ publishStream goroutine frames complete access units and writes records to every subscriber's WT uni stream
```

### Start sequence
1. Client opens WebTransport session → opens bidirectional stream → sends `{"type":"start"}`
2. Agent spawns captured with a pipe on fd 3 → sends JSON-RPC `start` with display, fps, codec and bitrate (plus `--captured` properties)
3. captured replies with every property, including `video.size` and `video.active_encoder`
4. Agent opens unidirectional stream on the caller's session → adds caller as owner + subscriber
5. Each video record on fd 3 is one access unit; it goes straight into a framed record for every subscriber's unidirectional stream (no framer pass)
6. Agent responds to caller with `{"type":"started","width":...,"height":...,"codec":"h264"}`

### Stop sequence
1. Any client sends `{"type":"stop"}` → cancel publish context → close captured's stdin, which makes it stop and exit (killed after 2 s)
2. Send `{"type":"stream-ended"}` to all subscribers → close all sessions
3. Set `state = nil`

A `stopped` notification from captured (capture or encoder failure) tears the stream down the same way.

### Owner disconnect
- If the client that started the stream disconnects, stream is torn down automatically
- All subscribers receive `stream-ended`

### Late joiners
- Clients connecting while a stream is active are auto-registered as subscribers (no video until they send `start`)
- On `start`, agent opens a unidirectional stream on their session for video and asks captured for a keyframe (`video.keyframe`), since captured only sends IDRs on request by default

### Fingerprint refresh flow
1. Cert rotation goroutine detects < 7 days remaining
2. Generates new ECDSA P-256 cert, saves to disk
3. Sends `{"type":"fingerprint-refresh","algorithm":"sha-256","fingerprint":"<hex>"}` to all connected subs
4. Client caches additional fingerprint
5. On next connection, includes both old and new hashes in `serverCertificateHashes`

**Caveat:** the rotation broadcast only reaches sessions subscribed to a *live*
stream — `broadcastControlMsg` returns early when no stream is active, and
subscribers are only registered when a stream exists at connect time. A
connected-but-idle client is not notified. The connect-time push in
`handleSession` is unconditional.

**With `--cert`/`--key`** there is no cert manager at all: no fingerprint push,
no rotation loop, and no web UI on `:52022`. That is the reverse-proxy /
publicly-trusted-cert mode, where clients connect without
`serverCertificateHashes`.

## Dependencies

- `github.com/okdaichi/webtransport-go` — WebTransport over QUIC/HTTP-3
- `github.com/quic-go/quic-go` — QUIC transport layer

## Build

```sh
go build -o agent .
```
