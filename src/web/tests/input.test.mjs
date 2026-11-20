import assert from 'node:assert/strict'
import { test } from 'node:test'
import { InputController } from '../src/input.ts'

function fixture(t) {
  const globals = ['document', 'window', 'navigator'].map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)])
  t.after(() => {
    for (const [name, descriptor] of globals) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor)
      else delete globalThis[name]
    }
  })
  const document = new EventTarget()
  const window = new EventTarget()
  const target = new EventTarget()
  const classes = new Set()
  target.classList = {
    toggle(name, on) { if (on) classes.add(name); else classes.delete(name) },
    remove(name) { classes.delete(name) }
  }
  document.exitPointerLock = () => { document.pointerLockElement = null }
  for (const [name, value] of Object.entries({ document, window, navigator: {} })) {
    Object.defineProperty(globalThis, name, { configurable: true, value })
  }
  const messages = []
  let closed = false
  const input = new InputController(message => {
    if (closed) throw new Error('session closed')
    messages.push(message)
  })
  input.attach(target)
  const event = (on, type, fields = {}) => on.dispatchEvent(Object.assign(new Event(type), fields))
  const lock = () => { document.pointerLockElement = target; event(document, 'pointerlockchange') }
  const unlock = () => { document.pointerLockElement = null; event(document, 'pointerlockchange') }
  const key = (code, down) => event(window, down ? 'keydown' : 'keyup', { code, key: code })
  const button = (button, down) => event(target, down ? 'mousedown' : 'mouseup', { button })
  return { input, target, messages, classes, lock, unlock, key, button, close() { closed = true } }
}

test('pointer-lock loss releases held keys and buttons once, excluding already-released input', t => {
  const f = fixture(t)
  f.key('ShiftLeft', true)
  f.button(0, true)
  assert.deepEqual(f.messages, [])
  f.lock()
  f.key('ShiftLeft', true)
  f.key('ShiftLeft', true)
  f.key('KeyA', true)
  f.key('KeyA', false)
  f.button(0, true)
  f.button(2, true)
  f.button(2, false)
  f.messages.length = 0
  f.unlock()
  assert.deepEqual(f.messages, [
    { type: 'input', kind: 'mouseup', button: 0 },
    { type: 'input', kind: 'key', code: 'ShiftLeft', down: false }
  ])
  f.unlock()
  f.key('ShiftLeft', false)
  f.input.detach()
  assert.equal(f.messages.length, 2)
})

test('detach releases input without waiting for pointerlockchange and removes listeners', t => {
  const f = fixture(t)
  f.lock()
  f.key('MetaRight', true)
  f.button(1, true)
  f.messages.length = 0
  f.input.detach()
  assert.deepEqual(f.messages, [
    { type: 'input', kind: 'mouseup', button: 1 },
    { type: 'input', kind: 'key', code: 'MetaRight', down: false }
  ])
  assert.equal(f.input.isLocked, false)
  assert.equal(f.classes.has('locked'), false)
  f.lock()
  f.key('KeyC', true)
  f.input.detach()
  assert.equal(f.messages.length, 2)
})

test('release cleanup is harmless for empty state and an already-closed transport', t => {
  const f = fixture(t)
  f.input.detach()
  f.input.attach(f.target)
  f.lock()
  f.key('ShiftRight', true)
  f.button(0, true)
  f.close()
  assert.doesNotThrow(() => f.input.detach())
  assert.doesNotThrow(() => f.input.detach())
  assert.equal(f.input.isLocked, false)
})
