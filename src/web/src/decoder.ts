/**
 * H264 (Annex B) decoder on the platform WebCodecs `VideoDecoder`, drawing onto
 * a <canvas>.
 *
 * The agent streams raw ffmpeg H.264 Annex B bytes over a WebTransport
 * unidirectional stream — one contiguous byte stream with no framing. So:
 *   1. buffer incoming bytes and split them on Annex B start codes
 *      (00 00 00 01 / 00 00 01);
 *   2. group NALs into access units (one per frame), starting a new unit at each
 *      VCL NAL whose slice header reports first_mb_in_slice == 0, so the
 *      SPS/PPS/SEI preceding a frame stay attached to it;
 *   3. build the AVCDecoderConfigurationRecord (avcC `description`) from the
 *      SPS/PPS and derive the RFC 6381 `avc1.PPCCLL` codec string from the SPS —
 *      the profile and level must come from the bitstream, not a constant, or
 *      `VideoDecoder` silently decodes nothing;
 *   4. convert each access unit from Annex B to AVCC (4-byte length prefixes)
 *      and feed it as one `EncodedVideoChunk`.
 *
 * Streams are always joined mid-GOP, since the agent's encoder is already
 * running when a viewer connects, so access units before the first keyframe are
 * discarded (see `emitAU`).
 */

const NAL_IDR = 5
const NAL_SPS = 7
const NAL_PPS = 8

function nalType(nal: Uint8Array): number {
  return nal[0] & 0x1f
}

function isVCL(t: number): boolean {
  return t >= 1 && t <= 5
}

/**
 * True when a VCL NAL starts a new picture, i.e. its slice header's
 * `first_mb_in_slice` is 0.
 *
 * That field is the first ue(v) Exp-Golomb value after the 1-byte NAL header,
 * and ue(v) == 0 is encoded as a single set bit, so a high bit in the first
 * payload byte means "this slice covers macroblock 0" — a new picture. With
 * multiple slices per picture only the first has first_mb_in_slice == 0, so this
 * still identifies exactly one boundary per frame.
 *
 * This is the reliable boundary test. Keying off "a VCL NAL following a non-VCL
 * NAL" only works when the encoder emits SEI/parameter sets between frames:
 * ffmpeg's Main-profile output does, but its High-profile output does not, and
 * there consecutive slices would otherwise collapse into a single access unit.
 */
function startsNewPicture(nal: Uint8Array): boolean {
  return nal.length > 1 && (nal[1] & 0x80) !== 0
}

/**
 * Build the AVCDecoderConfigurationRecord (avcC) from SPS/PPS NALs.
 * Both are passed without start codes but with their 1-byte NAL header.
 */
function buildAvcC(sps: Uint8Array, pps: Uint8Array): Uint8Array {
  const out = new Uint8Array(5 + 1 + 2 + sps.length + 1 + 2 + pps.length)
  let o = 0
  out[o++] = 1 // configurationVersion
  out[o++] = sps[1] // AVCProfileIndication = profile_idc
  out[o++] = sps[2] // profile_compatibility = constraint flags
  out[o++] = sps[3] // AVCLevelIndication = level_idc
  out[o++] = 0xff // 6 bits reserved + lengthSizeMinusOne = 3 (4-byte lengths)
  out[o++] = 0xe1 // 3 bits reserved + numOfSequenceParameterSets = 1
  out[o++] = (sps.length >> 8) & 0xff
  out[o++] = sps.length & 0xff
  out.set(sps, o)
  o += sps.length
  out[o++] = 1 // numOfPictureParameterSets
  out[o++] = (pps.length >> 8) & 0xff
  out[o++] = pps.length & 0xff
  out.set(pps, o)
  o += pps.length
  return out.subarray(0, o)
}

/** Derive the RFC 6381 codec string (e.g. `avc1.4d4020`) from an SPS NAL. */
function codecStringFromSps(sps: Uint8Array): string {
  const h = (n: number) => n.toString(16).padStart(2, '0')
  return `avc1.${h(sps[1])}${h(sps[2])}${h(sps[3])}`
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

/** Index of the first Annex B start code at or after `from`, else -1. */
function findStartCode(buf: Uint8Array, from: number): number {
  for (let i = from; i + 3 <= buf.length; i++) {
    if (buf[i] === 0x00 && buf[i + 1] === 0x00) {
      if (buf[i + 2] === 0x01) return i
      if (buf[i + 2] === 0x00 && i + 3 < buf.length && buf[i + 3] === 0x01) return i
    }
  }
  return -1
}

/** Length of the start code at index `i` (3 or 4). */
function startCodeLen(buf: Uint8Array, i: number): number {
  return i + 3 < buf.length && buf[i + 3] === 0x01 ? 4 : 3
}

export class Decoder {
  private videoDecoder: VideoDecoder | null = null
  private canvas: HTMLCanvasElement
  private ctx: CanvasRenderingContext2D

  private buf: Uint8Array = new Uint8Array(0)
  private pendingAU: Uint8Array[] = []
  private pendingBeforeConfig: Uint8Array[][] = []

  private sps: Uint8Array | null = null
  private pps: Uint8Array | null = null
  private configured = false
  private codec = ''
  private fps = 60
  private frameIndex = 0
  private seenKeyframe = false
  private droppedBeforeKeyframe = 0

  private frameCount = 0
  private fpsWindowStart = performance.now()
  private measuredFps = 0

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

  /**
   * Prepare for a new stream. The agent encodes H.264 only, and the real codec
   * string plus dimensions are taken from the SPS once it arrives, so the
   * `started` message is used just for the frame-rate hint.
   */
  configure(_codec: string, _width?: number, _height?: number, fps?: number): void {
    this.reset()
    if (fps && fps > 0) this.fps = fps
  }

  /** Feed a raw chunk of video bytes; may contain any number of NAL units. */
  feed(chunk: Uint8Array): void {
    this.buf = concat(this.buf, chunk)
    while (true) {
      const nal = this.nextNal()
      if (!nal) break
      this.ingestNal(nal)
    }
  }

  /**
   * Extract the next complete NAL. A NAL's end is only known once the following
   * start code appears, so an incomplete trailing NAL stays buffered.
   */
  private nextNal(): Uint8Array | null {
    const start = findStartCode(this.buf, 0)
    if (start === -1) {
      // Keep up to 3 trailing bytes that could be the head of a start code.
      const keep = Math.max(0, this.buf.length - 3)
      this.buf = this.buf.subarray(keep)
      return null
    }
    const dataStart = start + startCodeLen(this.buf, start)
    const next = findStartCode(this.buf, dataStart)
    if (next === -1) {
      this.buf = this.buf.subarray(start)
      return null
    }
    const nal = this.buf.subarray(dataStart, next)
    this.buf = this.buf.subarray(next)
    return nal
  }

  private ingestNal(nal: Uint8Array): void {
    if (nal.length === 0) return
    const t = nalType(nal)
    if (t === NAL_SPS) this.sps = nal.slice()
    else if (t === NAL_PPS) this.pps = nal.slice()

    // A VCL NAL that starts a new picture closes the previous access unit. Any
    // trailing non-VCL NALs already buffered (SPS/PPS/SEI) are parameter sets
    // for the *new* picture, so they stay with it rather than being emitted.
    if (isVCL(t) && startsNewPicture(nal) && this.pendingAU.length > 0) {
      let split = this.pendingAU.length
      while (split > 0 && !isVCL(nalType(this.pendingAU[split - 1]))) split--
      if (split > 0) {
        this.emitAU(this.pendingAU.slice(0, split))
        this.pendingAU = this.pendingAU.slice(split)
      }
    }
    this.pendingAU.push(nal)
  }

  private emitAU(au: Uint8Array[]): void {
    const carriesParams = au.some((n) => {
      const t = nalType(n)
      return t === NAL_SPS || t === NAL_PPS
    })
    if (carriesParams && this.sps && this.pps) {
      this.configureDecoder(this.sps, this.pps)
    }

    const isKey = au.some((n) => nalType(n) === NAL_IDR)

    // A stream is joined mid-GOP: the agent's ffmpeg is already running, so the
    // first access units received are the tail of the previous GOP and reference
    // frames (and a PPS) that were never sent. Feeding those to VideoDecoder
    // raises a fatal decode error, which moves it to `closed` and kills the
    // IDR that follows. So everything before the first keyframe is dropped.
    if (!this.seenKeyframe) {
      if (!isKey) {
        this.droppedBeforeKeyframe++
        return
      }
      this.seenKeyframe = true
      if (this.droppedBeforeKeyframe > 0) {
        console.info(
          `[decoder] dropped ${this.droppedBeforeKeyframe} access unit(s) before the first keyframe`
        )
      }
    }

    if (!this.configured) {
      // Keyframe arrived but SPS/PPS have not: hold it so the GOP is not lost.
      if (this.pendingBeforeConfig.length < 8) this.pendingBeforeConfig.push(au)
      return
    }
    if (!this.videoDecoder || this.videoDecoder.state !== 'configured') return

    // Annex B -> AVCC: replace start codes with 4-byte lengths.
    let size = 0
    for (const n of au) size += 4 + n.length
    const avcc = new Uint8Array(size)
    let o = 0
    for (const n of au) {
      avcc[o++] = (n.length >> 24) & 0xff
      avcc[o++] = (n.length >> 16) & 0xff
      avcc[o++] = (n.length >> 8) & 0xff
      avcc[o++] = n.length & 0xff
      avcc.set(n, o)
      o += n.length
    }

    const timestamp = Math.round((this.frameIndex * 1_000_000) / this.fps)
    const duration = Math.round(1_000_000 / this.fps)
    try {
      this.videoDecoder.decode(
        new EncodedVideoChunk({
          type: isKey ? 'key' : 'delta',
          timestamp,
          duration,
          data: avcc as unknown as BufferSource
        })
      )
      this.frameIndex++
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'InvalidStateError')) {
        console.warn('[decoder] decode threw', e)
      }
    }
  }

  /**
   * A fatal VideoDecoder error moves it to `closed`, after which every decode
   * throws. Recover by tearing the decoder down and waiting for the next
   * SPS/PPS + keyframe, rather than leaving a dead decoder in place.
   */
  private onDecoderError(e: DOMException): void {
    console.error('[decoder] error', e)
    this.configured = false
    this.seenKeyframe = false
    this.sps = null
    this.pps = null
    this.pendingBeforeConfig = []
    this.videoDecoder = null
  }

  private configureDecoder(sps: Uint8Array, pps: Uint8Array): void {
    const codec = codecStringFromSps(sps)
    if (this.configured && this.codec === codec) return

    if (this.videoDecoder && this.videoDecoder.state !== 'closed') {
      try { this.videoDecoder.reset() } catch { /* ignore */ }
    }

    this.videoDecoder = new VideoDecoder({
      output: (frame) => this.onFrame(frame),
      error: (e) => this.onDecoderError(e)
    })

    try {
      this.videoDecoder.configure({
        codec,
        // codedWidth/codedHeight come from the SPS inside `description`.
        description: buildAvcC(sps, pps) as unknown as BufferSource,
        optimizeForLatency: true
      })
    } catch (e) {
      console.error('[decoder] configure failed', codec, e)
      return
    }

    this.codec = codec
    this.configured = true
    console.info('[decoder] configured', codec)

    if (this.pendingBeforeConfig.length) {
      const queued = this.pendingBeforeConfig
      this.pendingBeforeConfig = []
      for (const au of queued) this.emitAU(au)
    }
  }

  private onFrame(frame: VideoFrame): void {
    const w = frame.displayWidth || frame.codedWidth
    const h = frame.displayHeight || frame.codedHeight
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width = w
      this.canvas.height = h
    }
    this.ctx.drawImage(frame as unknown as CanvasImageSource, 0, 0, w, h)
    frame.close()

    this.frameCount++
    this.tickFps()
    if (!this.firstFrameDrawn) {
      this.firstFrameDrawn = true
      this.onFirstFrame?.()
    }
  }

  /** Rolling fps over decoded frames (not received bytes). */
  private tickFps(): void {
    const now = performance.now()
    const dt = (now - this.fpsWindowStart) / 1000
    if (dt >= 1) {
      this.measuredFps = Math.round(this.frameCount / dt)
      this.frameCount = 0
      this.fpsWindowStart = now
      this.onFps?.(this.measuredFps)
    }
  }

  get currentFps(): number {
    return this.measuredFps
  }

  reset(): void {
    this.buf = new Uint8Array(0)
    this.pendingAU = []
    this.pendingBeforeConfig = []
    this.sps = null
    this.pps = null
    this.configured = false
    this.codec = ''
    this.frameIndex = 0
    this.frameCount = 0
    this.measuredFps = 0
    this.firstFrameDrawn = false
    this.seenKeyframe = false
    this.droppedBeforeKeyframe = 0
    try { this.videoDecoder?.reset() } catch { /* ignore */ }
    this.videoDecoder = null
  }

  close(): void {
    try { this.videoDecoder?.close() } catch { /* ignore */ }
    this.videoDecoder = null
    this.configured = false
  }
}
