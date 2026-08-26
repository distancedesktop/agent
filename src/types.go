package main

import (
	"context"
	"sync"

	"github.com/okdaichi/webtransport-go"

	"distancedesktop/agent/src/backend"
)

// subscriber wraps one WebTransport session's control + video streams.
type subscriber struct {
	sess   *webtransport.Session
	ctrl   *webtransport.Stream
	ctrlMu sync.Mutex
	video  *webtransport.SendStream
}

// streamState tracks the one active video stream.
type streamState struct {
	stream backend.Stream

	subscribers map[*subscriber]struct{}
	subMu       sync.Mutex
	stopPub     context.CancelFunc

	owner *subscriber
}

var (
	state   *streamState
	stateMu sync.Mutex

	// activeBackend is resolved at startup from --backend/--<backend> flags.
	activeBackend backend.Backend
)

// listDisplays queries the active backend (kept as a helper for session.go).
func listDisplays() ([]backend.Display, error) {
	if activeBackend == nil {
		b, err := backend.Get("captured")
		if err != nil {
			return nil, err
		}
		activeBackend = b
	}
	return activeBackend.ListDisplays(context.Background())
}
