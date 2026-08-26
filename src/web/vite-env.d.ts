/// <reference types="vite/client" />

// WebCodecs / WebTransport / BarcodeDetector shims for older lib.dom typings.
// All major evergreen browsers ship these; we only need compile-time types.

interface VideoFrame {
  readonly timestamp: number
  readonly duration: number | null
  close(): void
}

interface EncodedVideoChunkInit {
  type: 'key' | 'delta'
  timestamp: number
  duration?: number
  data: BufferSource
}

declare class EncodedVideoChunk {
  constructor(init: EncodedVideoChunkInit)
  readonly type: 'key' | 'delta'
  readonly timestamp: number
  readonly duration: number | null
  copyTo(destination: BufferSource): void
}

interface VideoDecoderConfig {
  codec: string
  width?: number
  height?: number
  description?: BufferSource
  optimizeForLatency?: boolean
}

interface VideoDecoderInit {
  output: (frame: VideoFrame) => void
  error: (error: DOMException) => void
}

declare class VideoDecoder {
  constructor(init: VideoDecoderInit)
  readonly state: 'unconfigured' | 'configured' | 'closed'
  configure(config: VideoDecoderConfig): void
  decode(chunk: EncodedVideoChunk): void
  flush(): Promise<void>
  reset(): void
  close(): void
}

interface VideoDecoderHeapSizeLimit {
  readonly maxEncodedBytes: number
}

// BarcodeDetector (Chrome/Edge) for QR scanning of connect payloads.
interface DetectedBarcode {
  readonly rawValue: string
  readonly format: string
}

declare class BarcodeDetector {
  constructor(options?: { formats?: string[] })
  detect(source: CanvasImageSource | Blob): Promise<DetectedBarcode[]>
}

interface Window {
  BarcodeDetector?: typeof BarcodeDetector
}
