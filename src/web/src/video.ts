import type { VideoFrameRecord } from './types'

const HEADER_SIZE = 13
const MAX_PAYLOAD = 64 * 1024 * 1024

/** One parser per video uni stream; read boundaries are not record boundaries. */
export class VideoRecordParser {
  private header = new Uint8Array(HEADER_SIZE)
  private headerBytes = 0
  private payload: Uint8Array | null = null
  private payloadBytes = 0
  private keyframe = false
  private timestampMs = 0

  push(chunk: Uint8Array): VideoFrameRecord[] {
    const records: VideoFrameRecord[] = []
    let offset = 0
    while (offset < chunk.length) {
      if (!this.payload) {
        const count = Math.min(HEADER_SIZE - this.headerBytes, chunk.length - offset)
        this.header.set(chunk.subarray(offset, offset + count), this.headerBytes)
        this.headerBytes += count
        offset += count
        if (this.headerBytes < HEADER_SIZE) break
        const view = new DataView(this.header.buffer)
        const size = view.getUint32(9, false)
        if (size === 0 || size > MAX_PAYLOAD) throw new Error(`invalid video payload length: ${size}`)
        const timestamp = view.getBigUint64(1, false)
        if (timestamp > BigInt(Math.floor(Number.MAX_SAFE_INTEGER / 1000))) {
          throw new Error('video timestamp exceeds microsecond precision')
        }
        this.keyframe = (this.header[0] & 1) !== 0
        this.timestampMs = Number(timestamp)
        this.payload = new Uint8Array(size)
      }
      const count = Math.min(this.payload.length - this.payloadBytes, chunk.length - offset)
      this.payload.set(chunk.subarray(offset, offset + count), this.payloadBytes)
      this.payloadBytes += count
      offset += count
      if (this.payloadBytes === this.payload.length) {
        records.push({ keyframe: this.keyframe, timestampMs: this.timestampMs, data: this.payload })
        this.headerBytes = 0
        this.payload = null
        this.payloadBytes = 0
      }
    }
    return records
  }

  finish(): void {
    if (this.headerBytes !== 0 || this.payload !== null) throw new Error('truncated video record')
  }
}
