import '../style.css'
import { Transport, wtUrl } from './transport'
import { Decoder } from './decoder'
import { InputController } from './input'
import { ConnectScreen, type ConnectDeps } from './ui/connect'
import { StatsOverlay } from './ui/stats'
import { toast } from './util'
import type { ConnectPayload, ControlMessage, Display, InputMessage } from './types'

const connectEl = document.getElementById('connect')!
const viewerEl = document.getElementById('viewer')!
const canvas = document.getElementById('stage') as HTMLCanvasElement
const statsEl = document.getElementById('stats')!
const hintEl = document.getElementById('viewer-hint')!

const transport = new Transport()
const decoder = new Decoder(canvas)
const stats = new StatsOverlay(statsEl)
const input = new InputController((m: InputMessage) => transport.send(m))

let pendingList: { resolve: (d: Display[]) => void; reject: (e: Error) => void } | null = null

transport.onMessage((msg: ControlMessage) => handleMessage(msg))
transport.setVideoHandler((chunk) => decoder.feed(chunk))
transport.onStats((s) => stats.update({ bitrate: s.bitrate, rtt: s.rtt, online: transport.connected }))

decoder.onFps = (fps) => stats.update({ fps })
decoder.onFirstFrame = () => stats.show()

function handleMessage(msg: ControlMessage): void {
  switch (msg.type) {
    case 'displays':
      if (pendingList) {
        pendingList.resolve(msg.displays)
        pendingList = null
      }
      break
    case 'started':
      onStarted(msg)
      break
    case 'error':
      if (pendingList) {
        pendingList.reject(new Error(msg.message))
        pendingList = null
      } else {
        toast(`Agent: ${msg.message}`, 'err')
      }
      break
    case 'stopped':
    case 'stream-ended':
      onStreamEnded(msg.type)
      break
    case 'fingerprint-refresh':
      // Server rotated its cert; a reconnect would be required with the new
      // fingerprint. Surfaced for awareness.
      toast('Agent certificate rotated — reconnect to refresh fingerprint', 'info')
      break
  }
}

function onStarted(msg: Extract<ControlMessage, { type: 'started' }>): void {
  decoder.configure(msg.codec, msg.width, msg.height)
  stats.update({ width: msg.width, height: msg.height })
  connectEl.classList.add('hidden')
  viewerEl.classList.remove('hidden')
  // width/height already published to overlay; now reveal it
  stats.show()
  input.attach(canvas)
  fadeHint()
  toast(`Streaming ${msg.width}×${msg.height}`, 'ok')
}

function onStreamEnded(kind: string): void {
  transport.close()
  toast(kind === 'stopped' ? 'Stream stopped' : 'Stream ended', 'info')
  input.detach()
  decoder.reset()
  viewerEl.classList.add('hidden')
  connectEl.classList.remove('hidden')
  connectScreen.render()
}

function fadeHint(): void {
  window.setTimeout(() => {
    if (hintEl) hintEl.style.opacity = '0'
  }, 6000)
}

// Press ~ to toggle the stats overlay.
window.addEventListener('keydown', (e) => {
  if (e.key === '~' || e.key === '`') {
    if (viewerEl.classList.contains('hidden')) return
    stats.toggle()
  }
})

const deps: ConnectDeps = {
  connectAndList: (payload: ConnectPayload) =>
    new Promise<Display[]>((resolve, reject) => {
      const url = wtUrl(payload.host, payload.port ?? 52020)
      transport
        .connect({ url, fingerprintHex: payload.fingerprint })
        .then(() => {
          pendingList = { resolve, reject }
          transport.listDisplays()
          // Safety timeout in case the agent never answers.
          window.setTimeout(() => {
            if (pendingList) {
              pendingList.reject(new Error('timed out waiting for displays'))
              pendingList = null
            }
          }, 8000)
        })
        .catch(reject)
    }),
  startStream: (displayId: number) => {
    transport.start(displayId)
  },
  onConnected: (payload) => connectScreen.saveRecent(payload)
}

const connectScreen = new ConnectScreen(connectEl, deps)
connectScreen.render()
