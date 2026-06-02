package main

import (
	"context"
	"io"
	"net"
	"os/exec"
	"sync"

	"github.com/qumo-dev/gomoqt/moqt"
	"github.com/okdaichi/webtransport-go"
)

type subscriber struct {
	sess   *webtransport.Session
	ctrl   *webtransport.Stream
	ctrlMu sync.Mutex
	video  *webtransport.SendStream
}

type streamState struct {
	displayID int
	width     int
	height    int
	fps       int

	capturedCtrl  net.Conn
	capturedMedia net.Conn

	ffmpeg    *exec.Cmd
	ffmpegIn  io.WriteCloser
	ffmpegOut io.ReadCloser

	subscribers map[*subscriber]struct{}
	subMu       sync.Mutex
	moqTracks   map[*moqt.TrackWriter]struct{}
	moqTrackMu  sync.Mutex
	stopPub     context.CancelFunc

	owner *subscriber
}

var (
	state              *streamState
	stateMu            sync.Mutex
	moqBroadcastCancel context.CancelFunc
)
