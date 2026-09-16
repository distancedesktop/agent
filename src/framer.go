package main

import "encoding/binary"

const maxNALBytes = 8 << 20

type framer struct {
	buf     []byte
	pending [][]byte
	ready   [][]byte
	dropped int
}

func (f *framer) Push(b []byte) [][]byte {
	f.buf = append(f.buf, b...)
	for {
		nal, ok := f.nextNAL()
		if !ok {
			if len(f.buf) > maxNALBytes {
				f.buf = nil
				f.pending = nil
				f.dropped++
			}
			break
		}
		f.ingestNAL(nal)
	}
	out := f.ready
	f.ready = nil
	return out
}

func (f *framer) Flush() [][]byte {
	if start := findStartCode(f.buf, 0); start >= 0 {
		dataStart := start + startCodeLen(f.buf, start)
		if dataStart < len(f.buf) {
			f.ingestNAL(append([]byte(nil), f.buf[start:]...))
		}
	}
	f.buf = nil
	if len(f.pending) > 0 {
		f.ready = append(f.ready, joinNALs(f.pending))
		f.pending = nil
	}
	out := f.ready
	f.ready = nil
	return out
}

func (f *framer) nextNAL() ([]byte, bool) {
	start := findStartCode(f.buf, 0)
	if start < 0 {
		if len(f.buf) > 3 {
			f.buf = append([]byte(nil), f.buf[len(f.buf)-3:]...)
		}
		return nil, false
	}
	dataStart := start + startCodeLen(f.buf, start)
	next := findStartCode(f.buf, dataStart)
	if next < 0 {
		f.buf = append([]byte(nil), f.buf[start:]...)
		return nil, false
	}
	nal := append([]byte(nil), f.buf[start:next]...)
	f.buf = f.buf[next:]
	return nal, true
}

func (f *framer) ingestNAL(nal []byte) {
	if len(nal) == 0 {
		return
	}
	t := nalType(nal)
	if isVCL(t) && startsNewPicture(nal) && len(f.pending) > 0 {
		split := len(f.pending)
		for split > 0 && !isVCL(nalType(f.pending[split-1])) {
			split--
		}
		if split > 0 {
			f.ready = append(f.ready, joinNALs(f.pending[:split]))
			f.pending = append([][]byte(nil), f.pending[split:]...)
		}
	}
	f.pending = append(f.pending, nal)
}

func joinNALs(nals [][]byte) []byte {
	size := 0
	for _, nal := range nals {
		size += len(nal)
	}
	out := make([]byte, 0, size)
	for _, nal := range nals {
		out = append(out, nal...)
	}
	return out
}

func findStartCode(buf []byte, from int) int {
	for i := from; i+3 <= len(buf); i++ {
		if buf[i] != 0 || buf[i+1] != 0 {
			continue
		}
		if buf[i+2] == 1 {
			return i
		}
		if i+3 < len(buf) && buf[i+2] == 0 && buf[i+3] == 1 {
			return i
		}
	}
	return -1
}

func startCodeLen(buf []byte, i int) int {
	if i+3 < len(buf) && buf[i+2] == 0 && buf[i+3] == 1 {
		return 4
	}
	return 3
}

func nalType(nal []byte) byte {
	i := startCodeLen(nal, 0)
	if i >= len(nal) {
		return 0
	}
	return nal[i] & 0x1f
}

func isVCL(t byte) bool {
	return t >= 1 && t <= 5
}

func startsNewPicture(nal []byte) bool {
	i := startCodeLen(nal, 0)
	return i+1 < len(nal) && nal[i+1]&0x80 != 0
}

func keyframe(au []byte) bool {
	for i := 0; ; {
		start := findStartCode(au, i)
		if start < 0 {
			return false
		}
		t := nalType(au[start:])
		if t == 5 {
			return true
		}
		i = start + startCodeLen(au, start)
	}
}

func encodeFrame(flags byte, tsMs uint64, au []byte) []byte {
	out := make([]byte, 13+len(au))
	out[0] = flags
	binary.BigEndian.PutUint64(out[1:9], tsMs)
	binary.BigEndian.PutUint32(out[9:13], uint32(len(au)))
	copy(out[13:], au)
	return out
}
