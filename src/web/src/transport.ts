import type { ClientMessage, ServerMessage, ControlMessage } from './types'
import { hexToBytes } from './util'

export interface ConnectOptions {
  // Full WebTransport endpoint, e.g. https://10.10.1.5:52020/wt
  url: string
  // SHA-256 fingerprint (hex) of the agent's TLS certificate.
  fingerprintHex: string
}

export interface TransportStats {
  bitrate: number // bits/sec
  rtt: number // ms (control round-trip estimate)
  rxBytes: number // total video bytes received
}

type MessageHandler = (msg: ControlMessage) => void
type VideoHandler = (chunk: Uint8Array) => void
type StatsHandler = (stats: TransportStats) => void

/**
 * WebTransport client for Distance Desktop.
 *
 * - One bidirectional stream carries newline-delimited JSON control messages.
 * - One unidirectional stream (server -> client) carries raw H264 Annex B video.
 * - The server certificate hash is pinned via `serverCertificateHashes` so a
 *   self-signed agent cert is accepted without OS trust store involvement.
 */
export class Transport {
  private wt: WebTransport | null = null
  private ctrlWriter: WritableStreamDefaultWriter<Uint8Array> | null = null
  private ctrlReader: ReadableStreamDefaultReader<Uint8Array> | null = null
  private msgHandler: MessageHandler | null = null
  private videoHandler: VideoHandler | null = null
  private statsHandler: StatsHandler | null = null

  private ctrlBuf = new Uint8Array(0)
  private enc = new TextEncoder()
  private dec = new TextDecoder()

  // video byte accounting
  private rxBytes = 0
  private windowBytes = 0
  private lastWindow = performance.now()
  private bitrate = 0

  // control RTT estimate (time between a request we send and its response)
  private pendingSince = 0
  private rtt = 0

  private statsTimer: number | undefined
  private closed = false

  onMessage(h: MessageHandler) {
    this.msgHandler = h
  }
  setVideoHandler(h: VideoHandler) {
    this.videoHandler = h
  }
  onStats(h: StatsHandler) {
    this.statsHandler = h
  }

  get connected(): boolean {
    return this.wt !== null
  }

  async connect(opts: ConnectOptions): Promise<void> {
    const hash = hexToBytes(opts.fingerprintHex)
    if (hash.length !== 32) {
      throw new Error('fingerprint must be a 32-byte SHA-256 hex string')
    }

    const wt = new WebTransport(opts.url, {
      // serverCertificateHashes pins the self-signed agent cert. Older lib.dom
      // typings omit it, so we cast loosely; the runtime supports it.
      serverCertificateHashes: [{ algorithm: 'sha-256', value: hash }]
    } as any)
    this.wt = wt

    wt.closed.then(() => {
      if (!this.closed) this.msgHandler?.({ type: 'stream-ended' } as ServerMessage)
    })

    await wt.ready

    // Open the control bidi stream (server AcceptStream picks this up).
    const bidi = await wt.createBidirectionalStream()
    this.ctrlWriter = bidi.writable.getWriter()
    this.ctrlReader = bidi.readable.getReader()
    this.readControlLoop()

    // Start the video uni-stream reader.
    this.readVideoLoop()

    // Stats ticker (bitrate + rtt) once per second.
    this.statsTimer = window.setInterval(() => this.tickStats(), 1000)
  }

  send(msg: ClientMessage): void {
    if (!this.ctrlWriter) throw new Error('not connected')
    if (msg.type === 'list-displays' || msg.type === 'start') {
      this.pendingSince = performance.now()
    }
    const bytes = this.enc.encode(JSON.stringify(msg) + '\n')
    this.ctrlWriter.write(bytes)
  }

  listDisplays(): void {
    this.send({ type: 'list-displays' })
  }

  start(displayId: number, opts: { fps?: number; codec?: string; bitrate?: number } = {}): void {
    this.send({ type: 'start', display_id: displayId, ...opts })
  }

  stop(): void {
    this.send({ type: 'stop' })
  }

  getStats(): TransportStats {
    return { bitrate: this.bitrate, rtt: this.rtt, rxBytes: this.rxBytes }
  }

  close(): void {
    this.closed = true
    if (this.statsTimer) window.clearInterval(this.statsTimer)
    try {
      this.ctrlWriter?.close()
    } catch {
      /* ignore */
    }
    try {
      this.wt?.close()
    } catch {
      /* ignore */
    }
  }

  private async readControlLoop(): Promise<void> {
    const reader = this.ctrlReader
    if (!reader) return
    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        if (!value) continue
        this.ctrlBuf = concat(this.ctrlBuf, value)
        // Split on newline-delimited JSON.
        let nl: number
        while ((nl = findNL(this.ctrlBuf)) !== -1) {
          const line = this.dec.decode(this.ctrlBuf.subarray(0, nl))
          this.ctrlBuf = this.ctrlBuf.subarray(nl + 1)
          const text = line.trim()
          if (!text) continue
          let msg: ControlMessage
          try {
            msg = JSON.parse(text)
          } catch {
            continue
          }
          if ((msg.type === 'displays' || msg.type === 'started') && this.pendingSince) {
            this.rtt = Math.round(performance.now() - this.pendingSince)
            this.pendingSince = 0
          }
          this.msgHandler?.(msg)
        }
      }
    } catch {
      /* stream closed */
    }
  }

  private async readVideoLoop(): Promise<void> {
    const wt = this.wt
    if (!wt) return
    try {
      // Server opens unidirectional stream(s) for video; we read them as they
      // arrive on incomingUnidirectionalStreams.
      const streamReader = wt.incomingUnidirectionalStreams.getReader()
      while (true) {
        const { value: recv, done: sdone } = await streamReader.read()
        if (sdone) break
        if (!recv) continue
        const reader = recv.readable.getReader()
        while (true) {
          const { value, done } = await reader.read()
          if (done) break
          if (!value) continue
          this.rxBytes += value.byteLength
          this.windowBytes += value.byteLength
          this.videoHandler?.(value)
        }
      }
    } catch {
      /* stream closed */
    }
  }

  private tickStats(): void {
    const now = performance.now()
    const dt = (now - this.lastWindow) / 1000
    if (dt > 0) {
      this.bitrate = Math.round((this.windowBytes * 8) / dt)
      this.windowBytes = 0
      this.lastWindow = now
    }
    this.statsHandler?.(this.getStats())
  }
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

function findNL(buf: Uint8Array): number {
  for (let i = 0; i < buf.length; i++) {
    if (buf[i] === 0x0a) return i
  }
  return -1
}

/**
 * Build the WebTransport URL from a host + port. WebTransport uses the `https`
 * scheme and H3, so we always normalize to https://host:port/wt.
 */
export function wtUrl(host: string, port: number): string {
  const h = host.replace(/^https?:\/\//, '').replace(/\/$/, '')
  return `https://${h}:${port}/wt`
}
