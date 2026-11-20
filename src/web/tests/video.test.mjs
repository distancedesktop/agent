import assert from 'node:assert/strict'
import { test } from 'node:test'
import { VideoRecordParser } from '../src/video.ts'
import { Decoder } from '../src/decoder.ts'

function wire(keyframe, timestampMs, data) {
  const bytes = new Uint8Array(13 + data.length)
  const view = new DataView(bytes.buffer)
  bytes[0] = keyframe ? 1 : 0
  view.setBigUint64(1, BigInt(timestampMs))
  view.setUint32(9, data.length)
  bytes.set(data, 13)
  return bytes
}

const records = [
  { keyframe: true, timestampMs: 0, data: Uint8Array.of(0, 0, 1, 0x65, 9) },
  { keyframe: false, timestampMs: 4294967297, data: Uint8Array.of(0, 0, 0, 1, 0x41, 8, 7) }
]
const bytes = new Uint8Array(records.flatMap(r => [...wire(r.keyframe, r.timestampMs, r.data)]))

test('records preserve payloads and metadata across every split, coalesced and one-byte reads', () => {
  for (let split = 0; split <= bytes.length; split++) {
    const parser = new VideoRecordParser()
    assert.deepEqual([...parser.push(bytes.subarray(0, split)), ...parser.push(bytes.subarray(split))], records)
    parser.finish()
  }
  const parser = new VideoRecordParser()
  assert.deepEqual([...bytes].flatMap(b => parser.push(Uint8Array.of(b))), records)
  parser.finish()
})

test('empty input is harmless; incomplete, invalid and unsafe records are rejected', () => {
  const parser = new VideoRecordParser()
  assert.deepEqual(parser.push(new Uint8Array()), [])
  parser.finish()
  const record = wire(true, 1, records[0].data)
  for (let end = 1; end < record.length; end++) {
    const partial = new VideoRecordParser()
    assert.deepEqual(partial.push(record.subarray(0, end)), [])
    assert.throws(() => partial.finish(), /truncated/)
  }
  for (const length of [0, 64 * 1024 * 1024 + 1, 0xffffffff]) {
    const header = new Uint8Array(13)
    new DataView(header.buffer).setUint32(9, length)
    assert.throws(() => new VideoRecordParser().push(header), /payload length/)
  }
  assert.throws(() => parser.push(wire(true, 0xffffffffffffffffn, Uint8Array.of(1))), /timestamp/)
})

class MockVideoDecoder {
  static chunks = []
  static configs = []
  static failNextConfigure = false
  state = 'unconfigured'
  configure(config) {
    if (MockVideoDecoder.failNextConfigure) {
      MockVideoDecoder.failNextConfigure = false
      throw new Error('bad configuration')
    }
    MockVideoDecoder.configs.push(config)
    this.state = 'configured'
  }
  decode(chunk) { MockVideoDecoder.chunks.push(chunk) }
  close() { this.state = 'closed' }
}
globalThis.VideoDecoder = MockVideoDecoder
globalThis.EncodedVideoChunk = class { constructor(init) { Object.assign(this, init) } }
function decoder() {
  MockVideoDecoder.chunks = []
  MockVideoDecoder.configs = []
  return new Decoder({ getContext: () => ({ drawImage() {} }) })
}
function annexB(...nals) { return new Uint8Array(nals.flatMap(n => [0, 0, 0, 1, ...n])) }
const sps = [0x67, 0x64, 0, 0x34, 0xac, 0xd9]
const pps = [0x68, 0xee, 0x3c, 0x80]
const idr = [0x65, 0x80, 0x33]

test('SPS and PPS in the same AU configure immediately; flags and timestamps drive each chunk', () => {
  const d = decoder()
  const parser = new VideoRecordParser()
  for (const [keyframe, ms, data] of [[true, 1234, annexB(sps, pps, idr)], [false, 1251, annexB(idr)]]) {
    for (const b of wire(keyframe, ms, data)) {
      for (const frame of parser.push(Uint8Array.of(b))) d.feedFrame(frame)
    }
  }
  assert.equal(MockVideoDecoder.configs[0].codec, 'avc1.640034')
  assert.deepEqual(MockVideoDecoder.chunks.map(c => [c.type, c.timestamp]), [['key', 1234000], ['delta', 1251000]])
  assert.deepEqual([...MockVideoDecoder.chunks[1].data], [0, 0, 0, 3, ...idr])
  d.close()
})

test('empty AUs, pre-keyframe AUs and malformed SPS are dropped without stopping later frames', () => {
  const d = decoder()
  d.feedFrame({ keyframe: true, timestampMs: 0, data: new Uint8Array() })
  d.feedFrame({ keyframe: false, timestampMs: 1, data: annexB(sps, pps, idr) })
  assert.equal(MockVideoDecoder.chunks.length, 0)
  for (const bad of [[0x67], [0x67, 0], [0x67, 0, 0, 2]]) {
    assert.doesNotThrow(() => d.feedFrame({ keyframe: true, timestampMs: 2, data: annexB(bad, pps, idr) }))
  }
  d.feedFrame({ keyframe: true, timestampMs: 3, data: annexB(sps, pps, idr) })
  assert.equal(MockVideoDecoder.chunks.length, 1)
  assert.equal(MockVideoDecoder.chunks[0].timestamp, 3000)
  d.close()
})

test('configuration failure is contained and a later valid keyframe recovers', () => {
  const d = decoder()
  MockVideoDecoder.failNextConfigure = true
  const original = console.error
  console.error = () => {}
  try {
    assert.doesNotThrow(() => d.feedFrame({ keyframe: true, timestampMs: 1, data: annexB(sps, pps, idr) }))
    d.feedFrame({ keyframe: true, timestampMs: 2, data: annexB(sps, pps, idr) })
    assert.ok(MockVideoDecoder.chunks.some(c => c.timestamp === 2000))
  } finally {
    console.error = original
    d.close()
  }
})
