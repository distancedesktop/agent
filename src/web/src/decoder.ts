// H264 (Annex B) decoder using the platform WebCodecs VideoDecoder, drawing
// straight onto a <canvas>. The agent sends raw ffmpeg H264 bytes (Annex B with
// 00 00 00 01 / 00 00 01 start codes) on the unidirectional video stream, so we
// split the byte stream into NAL units and feed each to the decoder.

function mapCodec(codec: string, width?: number, height?: number): any {
  // H264-only: agent encodes exclusively as H.264 Annex B
  const c = 'avc1.42E01E'
  const cfg: any = { codec: c }
  if (width && height) {
    cfg.width = width
    cfg.height = height
  }
  return cfg
}

export class Decoder {
  private videoDecoder: VideoDecoder | null = null
  private canvas: HTMLCanvasElement
  private ctx: CanvasRenderingContext2D
  private buf = new Uint8Array(0)
  private dts = 0
  private sps: Uint8Array | null = null
  private pps: Uint8Array | null = null

  private frameCount = 0
  private fpsWindowStart = performance.now()
  private fps = 0

  public onFps: ((fps: number) => void) | null = null
  public onFirstFrame: (() => void) | null = null
  private firstFrameDrawn = false

  constructor(canvas: HTMLCanvasElement) {
    this.canvas = canvas
    const ctx = canvas.getContext('2d', { alpha: false })
    if (!ctx) throw new Error('2d canvas context unavailable')
    this.ctx = ctx
    if (typeof VideoDecoder === 'undefined') {
      throw new Error('WebCodecs VideoDecoder is not available in this browser')
    }
  }

  configure(codec: string, width?: number, height?: number): void {
    if (this.videoDecoder && this.videoDecoder.state !== 'closed') {
      try {
        this.videoDecoder.reset()
      } catch {
        /* ignore */
      }
    }
    this.videoDecoder = new VideoDecoder({
      output: (frame) => this.onFrame(frame),
      error: (e) => console.error('[decoder] error', e)
    })
    this.videoDecoder.configure(mapCodec(codec, width, height))
  }

  private onFrame(frame: VideoFrame): void {
    const w = frame.displayWidth
    const h = frame.displayHeight
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width = w
      this.canvas.height = h
    }
    this.ctx.drawImage(frame as unknown as CanvasImageSource, 0, 0, w, h)
    frame.close()

    this.frameCount++
    if (!this.firstFrameDrawn) {
      this.firstFrameDrawn = true
      this.onFirstFrame?.()
    }
  }

  /** Feed a raw chunk of video bytes (may contain many NAL units). */
  feed(chunk: Uint8Array): void {
    if (!this.videoDecoder || this.videoDecoder.state !== 'configured') return
    this.buf = concat(this.buf, chunk)
    let start = 0
    let i = 0
    const buf = this.buf
    while (i + 2 < buf.length) {
      if (buf[i] === 0 && buf[i + 1] === 0) {
        if (i + 3 < buf.length && buf[i + 2] === 0 && buf[i + 3] === 1) {
          // 4-byte start code: 00 00 00 01
          if (start < i) {
            const nal = buf.subarray(start, i)
            this.decodeNal(nal)
          }
          start = i + 4
          i = start
          continue
        } else if (buf[i + 2] === 1) {
          // 3-byte start code: 00 00 01
          if (start < i) {
            const nal = buf.subarray(start, i)
            this.decodeNal(nal)
          }
          start = i + 3
          i = start
          continue
        }
      }
      i++
    }
    this.buf = buf.subarray(start)
    this.tickFps()
  }

  private decodeNal(nal: Uint8Array): void {
    if (nal.length === 0) return
    const nalType = nal[0] & 0x1f
    if (nalType === 7) {
      this.sps = nal.slice()
      return
    }
    if (nalType === 8) {
      this.pps = nal.slice()
      return
    }
    // Build access unit: prepend cached SPS/PPS to IDR so decoder has params
    let data: Uint8Array
    let type: 'key' | 'delta'
    if (nalType === 5) {
      type = 'key'
      if (this.sps && this.pps) {
        data = annexBConcat([this.sps, this.pps, nal])
      } else {
        data = nal.slice()
      }
    } else if (nalType === 6) {
      // SEI — skip
      return
    } else {
      type = 'delta'
      data = nal.slice()
    }
    this.dts += 33333 // ~30fps access unit tick
    try {
      this.videoDecoder!.decode(
        new EncodedVideoChunk({ type, timestamp: this.dts, data: data as unknown as BufferSource })
      )
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'InvalidStateError')) {
        console.warn('[decoder] decode threw', e)
      }
    }
  }

  private tickFps(): void {
    const now = performance.now()
    const dt = (now - this.fpsWindowStart) / 1000
    if (dt >= 1) {
      this.fps = Math.round(this.frameCount / dt)
      this.frameCount = 0
      this.fpsWindowStart = now
      this.onFps?.(this.fps)
    }
  }

  get currentFps(): number {
    return this.fps
  }

  reset(): void {
    this.buf = new Uint8Array(0)
    this.dts = 0
    this.sps = null
    this.pps = null
    try {
      this.videoDecoder?.reset()
    } catch {
      /* ignore */
    }
  }

  close(): void {
    try {
      this.videoDecoder?.close()
    } catch {
      /* ignore */
    }
    this.videoDecoder = null
  }
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

function annexBConcat(nals: Uint8Array[]): Uint8Array {
  const sc = new Uint8Array([0, 0, 0, 1])
  let total = 0
  for (const n of nals) total += 4 + n.length
  const out = new Uint8Array(total)
  let off = 0
  for (const n of nals) {
    out.set(sc, off)
    off += 4
    out.set(n, off)
    off += n.length
  }
  return out
}
