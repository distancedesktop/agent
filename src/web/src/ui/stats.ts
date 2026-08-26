import { formatBitrate } from '../util'

export interface StatsView {
  fps: number
  rtt: number // ms
  bitrate: number // bits/sec
  width: number
  height: number
  online: boolean
}

/** Top-right HUD overlay showing live stream quality metrics. */
export class StatsOverlay {
  private root: HTMLElement
  private visible = false
  private last: StatsView = { fps: 0, rtt: 0, bitrate: 0, width: 0, height: 0, online: false }

  constructor(root: HTMLElement) {
    this.root = root
  }

  toggle(): void {
    this.visible = !this.visible
    this.root.classList.toggle('hidden', !this.visible)
    if (this.visible) this.render()
  }

  show(): void {
    this.visible = true
    this.root.classList.remove('hidden')
    this.render()
  }

  get isVisible(): boolean {
    return this.visible
  }

  update(v: Partial<StatsView>): void {
    this.last = { ...this.last, ...v }
    if (this.visible) this.render()
  }

  private row(k: string, v: string, cls = ''): string {
    return `<div class="stat"><span class="k">${k}</span><span class="v ${cls}">${v}</span></div>`
  }

  private render(): void {
    const v = this.last
    const rttCls = v.rtt > 150 ? 'crit' : v.rtt > 60 ? 'bad' : ''
    const fpsCls = v.fps === 0 ? 'bad' : ''
    const bitrateCls = v.bitrate === 0 ? 'bad' : ''
    this.root.innerHTML =
      this.row('fps', v.fps ? `${v.fps}` : '—', fpsCls) +
      this.row('rtt', v.rtt ? `${v.rtt} ms` : '—', rttCls) +
      this.row('bitrate', v.bitrate ? formatBitrate(v.bitrate) : '—', bitrateCls) +
      this.row('res', v.width ? `${v.width}×${v.height}` : '—') +
      this.row('link', v.online ? 'up' : 'down', v.online ? '' : 'crit')
  }
}
