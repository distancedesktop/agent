package main

import (
	"fmt"
	"log"

	"distancedesktop/agent/src/input"
)

var inputEnabled = true
var newInjector = input.New

func injectInput(sub *subscriber, event input.Event) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	if state == nil || state.owner != sub {
		return nil
	}
	if !inputEnabled {
		return fmt.Errorf("input is disabled")
	}
	if !state.inputTried {
		state.inputTried = true
		i, err := newInjector()
		state.inputErr = err
		if err != nil {
			log.Printf("input: %v", err)
		} else {
			state.input = input.NewController(i)
		}
	}
	if state.inputErr != nil {
		return state.inputErr
	}
	return state.input.Handle(event, state.geometry)
}
