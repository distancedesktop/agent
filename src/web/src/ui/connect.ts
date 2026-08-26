import type { Display, ConnectPayload } from '../types'
import { el, toast } from '../util'

const RECENT_KEY = 'distance.recentHosts'

export interface RecentHost {
  host: string
  port: number
  fingerprint: string
  label?: string
  last: number
}

export interface ConnectDeps {
  /** Connect + fetch the display list. Resolves with displays or rejects. */
  connectAndList: (payload: ConnectPayload) => Promise<Display[]>
  /** Begin streaming a chosen display. */
  startStream: (displayId: number) => void
  /** Called once a connection succeeds, to persist the host. */
  onConnected?: (payload: ConnectPayload) => void
}

/**
 * The landing screen: paste/scan the agent fingerprint, pick a recent host,
 * then choose a display to stream. Pure DOM (no framework).
 */
export class ConnectScreen {
  private root: HTMLElement
  private deps: ConnectDeps
  private fpInput!: HTMLTextAreaElement
  private hostInput!: HTMLInputElement
  private portInput!: HTMLInputElement
  private labelInput!: HTMLInputElement
  private connectBtn!: HTMLButtonElement
  private qrStream: MediaStream | null = null

  constructor(root: HTMLElement, deps: ConnectDeps) {
    this.root = root
    this.deps = deps
  }

  render(): void {
    this.root.innerHTML = ''
    const card = el('div', { class: 'connect-card' })

    card.append(
      el('div', { class: 'brand' }, [
        el('span', { class: 'dot' }),
        el('h1', { text: 'Distance Desktop' })
      ]),
      el('div', { class: 'subtitle', text: 'Connect to a self-hosted agent over WebTransport.' })
    )

    // Auto-fill from the serving agent when possible.
    const suggested = this.suggestFromPage()

    const fpField = el('div', { class: 'field' }, [el('label', { text: 'Agent fingerprint (SHA-256)' })])
    this.fpInput = el('textarea', {
      placeholder: 'paste the fingerprint shown by the agent (64 hex chars)',
      spellcheck: 'false'
    }) as HTMLTextAreaElement
    fpField.append(this.fpInput)

    const row = el('div', { class: 'row' })
    const hostField = el('div', { class: 'field' }, [el('label', { text: 'Host' })])
    this.hostInput = el('input', {
      type: 'text',
      placeholder: '10.10.1.5',
      value: suggested.host
    }) as HTMLInputElement
    hostField.append(this.hostInput)

    const portField = el('div', { class: 'field' }, [el('label', { text: 'WT port' })])
    this.portInput = el('input', { type: 'number', value: String(suggested.port) }) as HTMLInputElement
    portField.append(this.portInput)
    row.append(hostField, portField)

    const labelField = el('div', { class: 'field' }, [el('label', { text: 'Label (optional)' })])
    this.labelInput = el('input', { type: 'text', placeholder: 'living room pc' }) as HTMLInputElement
    labelField.append(this.labelInput)

    const actions = el('div', { class: 'actions' })
    this.connectBtn = el('button', { class: 'btn primary', text: 'Connect' }) as HTMLButtonElement
    this.connectBtn.addEventListener('click', () => this.doConnect())
    const qrBtn = el('button', { class: 'btn ghost', text: 'Scan QR' }) as HTMLButtonElement
    qrBtn.addEventListener('click', () => this.openQr())
    actions.append(this.connectBtn, qrBtn)

    const pasteJson = el('div', { class: 'field' }, [
      el('label', { text: '…or paste a connection JSON' })
    ])
    const pasteArea = el('textarea', {
      placeholder: '{"host":"10.10.1.5","port":52020,"fingerprint":"abcd…","label":"pc"}',
      spellcheck: 'false'
    }) as HTMLTextAreaElement
    pasteArea.addEventListener('change', () => this.applyJson(pasteArea.value))
    pasteJson.append(pasteArea)

    card.append(fpField, row, labelField, actions, pasteJson)

    // Recent hosts
    const recent = this.loadRecent()
    if (recent.length) {
      const rec = el('div', { class: 'recent' }, [el('h2', { text: 'Recent hosts' })])
      const list = el('div', { class: 'recent-list' })
      for (const h of recent) {
        const item = el('div', { class: 'recent-item' })
        const meta = el('div', { class: 'meta' }, [
          el('div', { class: 'host', text: h.label ? `${h.label} · ${h.host}:${h.port}` : `${h.host}:${h.port}` }),
          el('div', { class: 'fp', text: h.fingerprint })
        ])
        item.append(meta)
        const del = el('button', { class: 'del', title: 'forget', text: '×' }) as HTMLButtonElement
        del.addEventListener('click', (e) => {
          e.stopPropagation()
          this.forget(h.fingerprint + h.host)
          this.render()
        })
        item.append(del)
        item.addEventListener('click', () => {
          this.fpInput.value = h.fingerprint
          this.hostInput.value = h.host
          this.portInput.value = String(h.port)
          if (h.label) this.labelInput.value = h.label
        })
        list.append(item)
      }
      rec.append(list)
      card.append(rec)
    }

    this.root.append(card)
    this.fpInput.value = suggested.fingerprint ?? ''
  }

  private suggestFromPage(): { host: string; port: number; fingerprint?: string } {
    const out = { host: location.hostname || '', port: 52020, fingerprint: undefined as string | undefined }
    // The page is served by the agent itself; ask it for its fingerprint + IPs.
    fetch('/api/info')
      .then((r) => r.json())
      .then((d) => {
        if (d?.fingerprint) {
          this.fpInput.value = d.fingerprint
        }
        if (Array.isArray(d?.ips) && d.ips.length && !this.hostInput.value) {
          this.hostInput.value = d.ips[0]
        }
      })
      .catch(() => {
        /* not served by an agent / offline */
      })
    return out
  }

  private async doConnect(): Promise<void> {
    const fingerprint = this.fpInput.value.trim().replace(/\s+/g, '')
    const host = this.hostInput.value.trim()
    const port = parseInt(this.portInput.value.trim(), 10) || 52020
    const label = this.labelInput.value.trim() || undefined

    if (!/^[0-9a-fA-F]{64}$/.test(fingerprint)) {
      toast('Fingerprint must be 64 hex characters', 'err')
      return
    }
    if (!host) {
      toast('Enter the agent host', 'err')
      return
    }

    this.connectBtn.disabled = true
    this.connectBtn.textContent = 'Connecting…'
    try {
      const displays = await this.deps.connectAndList({ host, port, fingerprint, label })
      this.deps.onConnected?.({ host, port, fingerprint, label })
      this.showDisplays(displays, { host, port, fingerprint, label })
    } catch (e) {
      toast(`Connection failed: ${(e as Error).message}`, 'err')
      this.connectBtn.disabled = false
      this.connectBtn.textContent = 'Connect'
    }
  }

  private showDisplays(displays: Display[], payload: ConnectPayload): void {
    this.root.innerHTML = ''
    const card = el('div', { class: 'connect-card' })
    card.append(
      el('div', { class: 'brand' }, [el('span', { class: 'dot' }), el('h1', { text: 'Choose a display' })]),
      el('div', { class: 'subtitle', text: `${payload.host}:${payload.port}` })
    )
    if (!displays.length) {
      card.append(el('div', { class: 'muted', text: 'No displays reported by the agent yet.' }))
    }
    const grid = el('div', { class: 'displays' })
    for (const d of displays) {
      const tile = el('button', { class: 'display-tile' }, [
        el('div', { class: 'name', text: `Display ${d.id}` }),
        el('div', { class: 'res', text: `${d.width}×${d.height} @ ${d.refresh_rate || '?'}Hz` })
      ]) as HTMLButtonElement
      tile.addEventListener('click', () => {
        this.deps.startStream(d.id)
        this.root.classList.add('hidden')
      })
      grid.append(tile)
    }
    card.append(grid)
    this.root.append(card)
  }

  private applyJson(text: string): boolean {
    let p: ConnectPayload
    try {
      p = JSON.parse(text.trim())
    } catch {
      toast('Invalid connection JSON', 'err')
      return false
    }
    if (p.fingerprint) this.fpInput.value = p.fingerprint
    if (p.host) this.hostInput.value = p.host
    if (p.port) this.portInput.value = String(p.port)
    if (p.label) this.labelInput.value = p.label
    toast('Filled from JSON', 'ok')
    return true
  }

  // ---------- QR scanning ----------
  private openQr(): void {
    if (!window.BarcodeDetector) {
      toast('QR scanning requires Chrome/Edge with BarcodeDetector', 'err')
      return
    }
    const modal = el('div', { class: 'qr-modal' })
    const box = el('div', { class: 'box' }, [el('h3', { text: 'Scan connection QR' })])
    const video = el('video', { class: 'qr-video', playsinline: 'true' }) as HTMLVideoElement
    box.append(video)
    box.append(el('div', { class: 'muted', text: 'Point your camera at the agent’s QR code.' }))
    const close = el('button', { class: 'btn ghost', text: 'Cancel' }) as HTMLButtonElement
    close.addEventListener('click', () => this.stopQr(modal))
    box.append(el('div', { class: 'actions' }, [close]))
    modal.append(box)
    document.body.append(modal)

    navigator.mediaDevices
      .getUserMedia({ video: { facingMode: 'environment' } })
      .then(async (stream) => {
        this.qrStream = stream
        video.srcObject = stream
        await video.play()
        this.scanLoop(video, modal)
      })
      .catch(() => {
        toast('Camera unavailable', 'err')
        this.stopQr(modal)
      })
  }

  private async scanLoop(video: HTMLVideoElement, modal: HTMLElement): Promise<void> {
    if (!this.qrStream || !window.BarcodeDetector) return
    const detector = new BarcodeDetector({ formats: ['qr'] })
    const tick = async () => {
      if (!this.qrStream) return
      try {
        const codes = await detector.detect(video)
        for (const c of codes) {
          if (this.applyJson(c.rawValue)) {
            this.stopQr(modal)
            return
          }
        }
      } catch {
        /* ignore frame errors */
      }
      requestAnimationFrame(tick)
    }
    tick()
  }

  private stopQr(modal: HTMLElement): void {
    this.qrStream?.getTracks().forEach((t) => t.stop())
    this.qrStream = null
    modal.remove()
  }

  // ---------- recent hosts ----------
  private loadRecent(): RecentHost[] {
    try {
      const raw = localStorage.getItem(RECENT_KEY)
      if (!raw) return []
      const arr = JSON.parse(raw) as RecentHost[]
      return arr.sort((a, b) => b.last - a.last).slice(0, 8)
    } catch {
      return []
    }
  }

  private remember(payload: ConnectPayload): void {
    try {
      const arr = this.loadRecent().filter((h) => !(h.host === payload.host && h.port === (payload.port ?? 52020)))
      arr.unshift({
        host: payload.host,
        port: payload.port ?? 52020,
        fingerprint: payload.fingerprint,
        label: payload.label,
        last: Date.now()
      })
      localStorage.setItem(RECENT_KEY, JSON.stringify(arr.slice(0, 8)))
    } catch {
      /* storage may be unavailable */
    }
  }

  private forget(key: string): void {
    try {
      const arr = this.loadRecent().filter((h) => h.fingerprint + h.host !== key)
      localStorage.setItem(RECENT_KEY, JSON.stringify(arr))
    } catch {
      /* ignore */
    }
  }

  /** Persist a host (called by main on successful connect). */
  saveRecent(payload: ConnectPayload): void {
    this.remember(payload)
  }
}
