package main

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/okdaichi/webtransport-go"
	"github.com/qumo-dev/gomoqt/transport"
)

type wtSessionAdapter struct {
	sess *webtransport.Session
}

func wrapWTSession(s *webtransport.Session) transport.WebTransportSession {
	return &wtSessionAdapter{sess: s}
}

func (a *wtSessionAdapter) AcceptStream(ctx context.Context) (transport.Stream, error) {
	s, err := a.sess.AcceptStream(ctx)
	return &wtStreamAdapter{stream: s}, err
}

func (a *wtSessionAdapter) AcceptUniStream(ctx context.Context) (transport.ReceiveStream, error) {
	s, err := a.sess.AcceptUniStream(ctx)
	return &wtReceiveStreamAdapter{stream: s}, err
}

func (a *wtSessionAdapter) CloseWithError(code transport.ConnErrorCode, msg string) error {
	return a.sess.CloseWithError(webtransport.SessionErrorCode(code), msg)
}

func (a *wtSessionAdapter) Context() context.Context {
	return a.sess.Context()
}

func (a *wtSessionAdapter) LocalAddr() net.Addr {
	return a.sess.LocalAddr()
}

func (a *wtSessionAdapter) OpenStream() (transport.Stream, error) {
	s, err := a.sess.OpenStream()
	return &wtStreamAdapter{stream: s}, err
}

func (a *wtSessionAdapter) OpenUniStream() (transport.SendStream, error) {
	s, err := a.sess.OpenUniStream()
	return &wtSendStreamAdapter{stream: s}, err
}

func (a *wtSessionAdapter) RemoteAddr() net.Addr {
	return a.sess.RemoteAddr()
}

func (a *wtSessionAdapter) TLS() *tls.ConnectionState {
	state := a.sess.SessionState()
	return &state.ConnectionState.TLS
}

func (a *wtSessionAdapter) Subprotocol() string {
	return a.sess.SessionState().ApplicationProtocol
}

type wtStreamAdapter struct {
	stream *webtransport.Stream
}

func (a *wtStreamAdapter) Read(b []byte) (int, error)      { return a.stream.Read(b) }
func (a *wtStreamAdapter) Write(b []byte) (int, error)     { return a.stream.Write(b) }
func (a *wtStreamAdapter) Close() error                     { return a.stream.Close() }
func (a *wtStreamAdapter) Context() context.Context         { return a.stream.Context() }
func (a *wtStreamAdapter) SetDeadline(t time.Time) error    { return a.stream.SetDeadline(t) }
func (a *wtStreamAdapter) SetReadDeadline(t time.Time) error { return a.stream.SetReadDeadline(t) }
func (a *wtStreamAdapter) SetWriteDeadline(t time.Time) error { return a.stream.SetWriteDeadline(t) }
func (a *wtStreamAdapter) CancelRead(code transport.StreamErrorCode) {
	a.stream.CancelRead(webtransport.StreamErrorCode(code))
}
func (a *wtStreamAdapter) CancelWrite(code transport.StreamErrorCode) {
	a.stream.CancelWrite(webtransport.StreamErrorCode(code))
}

type wtSendStreamAdapter struct {
	stream *webtransport.SendStream
}

func (a *wtSendStreamAdapter) Write(b []byte) (int, error) { return a.stream.Write(b) }
func (a *wtSendStreamAdapter) Close() error                 { return a.stream.Close() }
func (a *wtSendStreamAdapter) Context() context.Context     { return a.stream.Context() }
func (a *wtSendStreamAdapter) SetWriteDeadline(t time.Time) error { return a.stream.SetWriteDeadline(t) }
func (a *wtSendStreamAdapter) CancelWrite(code transport.StreamErrorCode) {
	a.stream.CancelWrite(webtransport.StreamErrorCode(code))
}

type wtReceiveStreamAdapter struct {
	stream *webtransport.ReceiveStream
}

func (a *wtReceiveStreamAdapter) Read(b []byte) (int, error) { return a.stream.Read(b) }
func (a *wtReceiveStreamAdapter) SetReadDeadline(t time.Time) error { return a.stream.SetReadDeadline(t) }
func (a *wtReceiveStreamAdapter) CancelRead(code transport.StreamErrorCode) {
	a.stream.CancelRead(webtransport.StreamErrorCode(code))
}
