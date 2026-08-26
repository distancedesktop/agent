// Control protocol shared with distancedesktop/agent (src/session.go).
// All control messages are newline-delimited JSON on the bidirectional stream.

export interface Display {
  id: number
  width: number
  height: number
  x: number
  y: number
  refresh_rate: number
}

// Input message types (not yet implemented on server; defined for future use)
export type InputMessage =
  | { type: 'input'; kind: 'mouse'; dx: number; dy: number; buttons: number }
  | { type: 'input'; kind: 'mousedown'; button: number }
  | { type: 'input'; kind: 'mouseup'; button: number }
  | { type: 'input'; kind: 'wheel'; dx: number; dy: number }
  | { type: 'input'; kind: 'key'; code: string; down: boolean }
  | { type: 'input'; kind: 'touch'; id: number; x: number; y: number; phase: 'start' | 'move' | 'end' }

// Client -> Server
export type ClientMessage =
  | { type: 'list-displays' }
  | {
      type: 'start'
      display_id: number
      fps?: number
      codec?: string
      bitrate?: number
    }
  | { type: 'stop' }
  | InputMessage

// Server -> Client
export type ServerMessage =
  | { type: 'fingerprint-refresh'; algorithm: string; fingerprint: string }
  | { type: 'displays'; displays: Display[] }
  | { type: 'started'; width: number; height: number; codec: string }
  | { type: 'stopped' }
  | { type: 'stream-ended' }
  | { type: 'error'; message: string }
  | { type: 'pong'; t: number }

export type ControlMessage = ServerMessage

// Connect payload encoded in a QR / paste blob.
export interface ConnectPayload {
  host: string
  port?: number
  fingerprint: string
  label?: string
}
