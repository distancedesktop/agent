/// <reference types="vite/client" />

// BarcodeDetector (Chrome/Edge) for scanning QR connection payloads.
// WebCodecs (VideoDecoder / EncodedVideoChunk / VideoFrame) and WebTransport
// types are provided by lib.dom in modern TypeScript, so we only shim what is
// missing there.

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
