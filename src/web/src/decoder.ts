// H264 (Annex B) decoder using the platform WebCodecs VideoDecoder, drawing
// straight onto a <canvas>. The agent sends raw ffmpeg H264 bytes (Annex B with
// 00 00 00 01 / 00 00 01 start codes) on the unidirectional video stream, so we
// split the byte stream into NAL units and feed each to the decoder.

function mapCodec(codec: string, width?: number, height?: number): any {
  let c = 'avc1.42E01E' // Constrained Baseline 3.0 — safe default for H264
  if (codec === 'hevc' || codec === 'h265') c = 'hvc1.1.6.L93.B0'
  else if (codec === 'av1') c = 'av01.0.05M.08'
  else if (codec === 'vp9') c = 'vp09.00.10.08'
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
      if (buf[i] === 0 && buf[i + 1] === 0 && buf[i + 2] === 1) {
        if (start < i) {
          const nal = buf.subarray(start, i)
          this.decodeNal(nal)
        }
        start = i + 3
        i = start
        continue
      }
      i++
    }
    this.buf = buf.subarray(start)
    this.tickFps()
  }

  private decodeNal(nal: Uint8Array): void {
    if (nal.length === 0) return
    const nalType = nal[0] & 0x1f
    const type: 'key' | 'delta' = nalType === 5 ? 'key' : 'delta'
    this.dts += 1000 // µs, monotonic in decode order
    try {
      this.videoDecoder!.decode(
        new EncodedVideoChunk({ type, timestamp: this.dts, data: nal as unknown as BufferSource })
      )
    } catch (e) {
      // Decoding a NAL before SPS/PPS is configured can throw; ignore until
      // the stream establishes a valid state.
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
