package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"distancedesktop/agent/src/input"
)

func TestControlRequest(t *testing.T) {
	for _, s := range []string{`{"type":"input","kind":"mouse","dx":2,"dy":-3,"buttons":1}`, `{"type":"input","kind":"mousedown","button":2}`, `{"type":"input","kind":"mouseup","button":2}`, `{"type":"input","kind":"wheel","dx":1,"dy":40}`, `{"type":"input","kind":"key","code":"KeyA","down":true}`, `{"type":"input","kind":"touch","id":7,"x":0.5,"y":0.2,"phase":"start"}`, `{"type":"start","display_id":3,"fps":30,"codec":"h264","bitrate":2000}`, `{"type":"stop"}`, `{}`} {
		var req controlRequest
		if err := json.Unmarshal([]byte(s), &req); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		json.Unmarshal(b, &got)
		json.Unmarshal([]byte(s), &want)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %v, want %v", s, k, got[k], v)
			}
		}
	}
	var req controlRequest
	if json.Unmarshal([]byte(`{"dx":"bad"}`), &req) == nil {
		t.Fatal("accepted invalid delta")
	}
}

func TestInputOwnership(t *testing.T) {
	oldState, oldNew, oldEnabled := state, newInjector, inputEnabled
	defer func() { state = oldState; newInjector = oldNew; inputEnabled = oldEnabled }()
	owner, viewer := &subscriber{}, &subscriber{}
	calls := 0
	newInjector = func() (input.Injector, error) { calls++; return nil, fmt.Errorf("no display") }
	inputEnabled = true
	state = &streamState{owner: owner}
	if err := injectInput(viewer, input.Event{}); err != nil || calls != 0 {
		t.Fatal("viewer initialized input")
	}
	for i := 0; i < 2; i++ {
		if injectInput(owner, input.Event{}) == nil {
			t.Fatal("missing unavailable error")
		}
	}
	if calls != 1 {
		t.Fatalf("initialized %d times", calls)
	}
	inputEnabled = false
	state = &streamState{owner: owner}
	if injectInput(owner, input.Event{}) == nil || calls != 1 {
		t.Fatal("disabled input initialized")
	}
}

func TestInputErrorChanges(t *testing.T) {
	sub := &subscriber{}
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"", false}, {"unavailable", true}, {"unavailable", false},
		{"disabled", true}, {"disabled", false}, {"", false},
		{"disabled", true},
	} {
		var err error
		if tc.message != "" {
			err = fmt.Errorf("%s", tc.message)
		}
		if got := sub.reportInputError(err); got != tc.want {
			t.Errorf("%q: report=%v want=%v", tc.message, got, tc.want)
		}
	}
}
