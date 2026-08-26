import type { InputMessage } from './types'

type Send = (msg: InputMessage) => void

/**
 * Captures local input and serializes it to the control bidi stream.
 *
 * - Pointer Lock gives relative mouse motion (movementX/Y) with no clipping.
 * - Keyboard Lock captures key events even when the browser would otherwise
 *   consume them (Tab, F-keys, etc.).
 * - Wheel and multi-touch (normalized 0..1) are forwarded for remote injection.
 */
export class InputController {
  private send: Send
  private target: HTMLElement | null = null
  private locked = false
  private bound: Array<[EventTarget, string, EventListenerOrEventListenerObject, AddEventListenerOptions | boolean]> = []

  constructor(send: Send) {
    this.send = send
  }

  attach(target: HTMLElement): void {
    this.target = target

    this.on(target, 'click', () => {
      if (!this.locked && target.requestPointerLock) {
        target.requestPointerLock()
      }
    })

    this.on(document, 'pointerlockchange', () => {
      this.locked = document.pointerLockElement === target
      target.classList.toggle('locked', this.locked)
      if (this.locked) this.tryKeyboardLock()
    })

    this.on(target, 'mousemove', (e) => {
      if (!this.locked) return
      const ev = e as MouseEvent
      this.send({ type: 'input', kind: 'mouse', dx: ev.movementX, dy: ev.movementY, buttons: ev.buttons })
    })

    this.on(target, 'mousedown', (e) => {
      const ev = e as MouseEvent
      this.send({ type: 'input', kind: 'mousedown', button: ev.button })
    })

    this.on(target, 'mouseup', (e) => {
      const ev = e as MouseEvent
      this.send({ type: 'input', kind: 'mouseup', button: ev.button })
    })

    this.on(
      target,
      'wheel',
      (e) => {
        if (!this.locked) return
        e.preventDefault()
        const ev = e as WheelEvent
        this.send({ type: 'input', kind: 'wheel', dx: ev.deltaX, dy: ev.deltaY })
      },
      { passive: false }
    )

    // Suppress the context menu so right-click can be sent to the remote.
    this.on(target, 'contextmenu', (e) => e.preventDefault())

    this.on(window, 'keydown', (e) => this.onKey(e as KeyboardEvent, true))
    this.on(window, 'keyup', (e) => this.onKey(e as KeyboardEvent, false))

    this.on(target, 'touchstart', (e) => this.onTouch(e as TouchEvent, 'start'), { passive: false })
    this.on(target, 'touchmove', (e) => this.onTouch(e as TouchEvent, 'move'), { passive: false })
    this.on(target, 'touchend', (e) => this.onTouch(e as TouchEvent, 'end'), { passive: false })
  }

  get isLocked(): boolean {
    return this.locked
  }

  release(): void {
    if (this.locked && document.exitPointerLock) document.exitPointerLock()
  }

  detach(): void {
    this.release()
    for (const [t, type, fn, opts] of this.bound) t.removeEventListener(type, fn, opts)
    this.bound = []
    this.target = null
  }

  private on(
    t: EventTarget,
    type: string,
    fn: (e: Event) => void,
    opts?: AddEventListenerOptions | boolean
  ): void {
    t.addEventListener(type, fn, opts)
    this.bound.push([t, type, fn, opts ?? false])
  }

  private tryKeyboardLock(): void {
    const kb = (navigator as Navigator & { keyboard?: { lock?: (codes?: string[]) => Promise<void> } }).keyboard
    if (!kb?.lock) return
    // Best-effort: capture a broad set of keys. Failures are non-fatal.
    kb.lock([
      'Tab',
      'Space',
      'ArrowUp',
      'ArrowDown',
      'ArrowLeft',
      'ArrowRight',
      'F1',
      'F2',
      'F3',
      'F4',
      'F5',
      'F6',
      'F7',
      'F8',
      'F9',
      'F10',
      'F11',
      'F12'
    ]).catch(() => {
      /* keyboard lock may require a transient activation; ignore */
    })
  }

  private onKey(e: KeyboardEvent, down: boolean): void {
    if (!this.locked) return
    // Stop the page from acting on keys we forward (scroll, find, etc.)
    const swallow = [
      'Tab',
      'Space',
      'ArrowUp',
      'ArrowDown',
      'ArrowLeft',
      'ArrowRight',
      "'",
      '/',
      'F1',
      'F2',
      'F3',
      'F4',
      'F5',
      'F6',
      'F7',
      'F8',
      'F9',
      'F10',
      'F11',
      'F12'
    ]
    if (swallow.includes(e.key)) e.preventDefault()
    this.send({ type: 'input', kind: 'key', code: e.code, down })
  }

  private onTouch(e: TouchEvent, phase: 'start' | 'move' | 'end'): void {
    if (!this.target) return
    e.preventDefault()
    const rect = this.target.getBoundingClientRect()
    const touches = e.changedTouches
    for (let i = 0; i < touches.length; i++) {
      const t = touches[i]
      const x = rect.width ? (t.clientX - rect.left) / rect.width : 0
      const y = rect.height ? (t.clientY - rect.top) / rect.height : 0
      this.send({ type: 'input', kind: 'touch', id: t.identifier, x, y, phase })
    }
  }
}
