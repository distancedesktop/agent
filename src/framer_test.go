package main

import (
	"bytes"
	"testing"
)

func TestFramerChunking(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 0x42, 0x00}
	pps := []byte{0, 0, 1, 0x68, 0xce, 0x06}
	idr := []byte{0, 0, 0, 1, 0x65, 0x88, 0x11}
	slice1 := []byte{0, 0, 1, 0x41, 0x9a, 0x22}
	slice2 := []byte{0, 0, 0, 1, 0x41, 0x9a, 0x33}
	stream := append(append(append(append(append([]byte{}, sps...), pps...), idr...), slice1...), slice2...)
	want := [][]byte{
		append(append(append([]byte{}, sps...), pps...), idr...),
		slice1,
		slice2,
	}

	tests := []struct {
		name   string
		chunks [][]byte
	}{
		{
			name: "all at once",
			chunks: [][]byte{
				stream,
			},
		},
		{
			name: "one byte at a time",
			chunks: func() [][]byte {
				out := make([][]byte, len(stream))
				for i := range stream {
					out[i] = stream[i : i+1]
				}
				return out
			}(),
		},
		{
			name: "split start code",
			chunks: [][]byte{
				stream[:len(sps)+len(pps)+len(idr)+1],
				stream[len(sps)+len(pps)+len(idr)+1 : len(sps)+len(pps)+len(idr)+2],
				stream[len(sps)+len(pps)+len(idr)+2:],
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f framer
			var got [][]byte
			for _, chunk := range tt.chunks {
				got = append(got, f.Push(chunk)...)
			}
			got = append(got, f.Flush()...)
			if len(got) != len(want) {
				t.Fatalf("got %d AUs, want %d", len(got), len(want))
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) {
					t.Errorf("AU %d = %x, want %x", i, got[i], want[i])
				}
			}
			if !keyframe(got[0]) {
				t.Error("AU 0 is not marked as a keyframe")
			}
			if keyframe(got[1]) || keyframe(got[2]) {
				t.Error("non-IDR AUs marked as keyframes")
			}
		})
	}
}

func TestEncodeFrame(t *testing.T) {
	au := []byte{0, 0, 1, 0x65, 0x88}
	got := encodeFrame(1, 0x0102030405060708, au)
	want := []byte{
		1,
		1, 2, 3, 4, 5, 6, 7, 8,
		0, 0, 0, 5,
		0, 0, 1, 0x65, 0x88,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = %x, want %x", got, want)
	}
}
