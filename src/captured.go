package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
)

func capturedSocket() string {
	if v := os.Getenv("CAPTURED_SOCKET"); v != "" {
		return v
	}
	return "/tmp/captured.socket"
}

func listDisplays() ([]map[string]any, error) {
	conn, err := net.Dial("unix", capturedSocket())
	if err != nil {
		return nil, fmt.Errorf("captured: %w", err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	log.Printf("captured: sending list-displays")
	if err := enc.Encode(map[string]string{"type": "list-displays"}); err != nil {
		return nil, err
	}
	var resp struct {
		Displays []struct {
			ID          uint32  `json:"id"`
			Width       int     `json:"width"`
			Height      int     `json:"height"`
			X           int     `json:"x"`
			Y           int     `json:"y"`
			RefreshRate float64 `json:"refresh_rate"`
		} `json:"displays"`
		Error string `json:"error,omitempty"`
	}
	if err := dec.Decode(&resp); err != nil {
		log.Printf("captured: list-displays decode error: %v", err)
		return nil, err
	}
	if resp.Error != "" {
		log.Printf("captured: list-displays error: %s", resp.Error)
		return nil, errors.New(resp.Error)
	}
	log.Printf("captured: got %d display(s)", len(resp.Displays))
	out := make([]map[string]any, len(resp.Displays))
	for i, d := range resp.Displays {
		log.Printf("captured: display[%d] id=%d %dx%d @ (x=%d,y=%d) %.2fhz", i, d.ID, d.Width, d.Height, d.X, d.Y, d.RefreshRate)
		out[i] = map[string]any{
			"id":           d.ID,
			"width":        d.Width,
			"height":       d.Height,
			"x":            d.X,
			"y":            d.Y,
			"refresh_rate": d.RefreshRate,
		}
	}
	return out, nil
}
