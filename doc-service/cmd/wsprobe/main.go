// wsprobe is the verification client for the WebSocket protocol: it dials a
// /ws URL, sends the frames given on the command line (one JSON text frame
// per argument) and prints every frame it receives, one per line, until the
// socket closes or --wait elapses. The close code is printed as the last line
// (close 4409 too_far_behind). When the 101 names the instance that
// accepted the socket (X-Doc-Instance, month 3) that is printed first as
// "instance doc-9001". It replaces websocat, which is not installed
// everywhere, and is what scripts/e2e.sh drives.
//
//	go run ./cmd/wsprobe --wait 1s 'ws://localhost:9000/ws?doc=1&ticket=…' '{"type":"op","v":0,"op":["hi"],"seq":1}'
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func main() {
	wait := flag.Duration("wait", time.Second, "how long to keep reading after the last frame was sent")
	origin := flag.String("origin", "", "Origin header to send (none by default)")
	flood := flag.Int("flood", 0, "after the given frames, send this many one-character ops as fast as the protocol allows (next op on ack or rate_limited)")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: wsprobe [--wait d] [--origin o] <url> [frame ...]")
		os.Exit(2)
	}

	header := http.Header{}
	if *origin != "" {
		header.Set("Origin", *origin)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(flag.Arg(0), header)
	if err != nil {
		if resp != nil {
			fmt.Printf("http %d\n", resp.StatusCode)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close()
	if instance := resp.Header.Get("X-Doc-Instance"); instance != "" {
		fmt.Printf("instance %s\n", instance)
	}

	for _, frame := range flag.Args()[1:] {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}

	if *flood > 0 {
		if !floodOps(conn, *flood) {
			return
		}
	}

	deadline := time.Now().Add(*wait)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				fmt.Printf("close %d %s\n", ce.Code, ce.Text)
			}
			return
		}
		fmt.Println(string(raw))
	}
}

// floodOps is a client that types faster than any person: it obeys the
// one-op-in-flight rule, so it is the server's rate limiter and not its
// protocol check that stops it. Each op inserts "x" at the start. Frames are
// printed as they arrive; false means the socket closed.
func floodOps(conn *websocket.Conn, n int) bool {
	var version, length int64
	sent, inFlight := 0, false
	send := func() bool {
		op := []any{"x"}
		if length > 0 {
			op = append(op, length)
		}
		frame, _ := json.Marshal(map[string]any{"type": "op", "v": version, "op": op, "seq": sent + 1})
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			return false
		}
		sent++
		inFlight = true
		return true
	}

	for sent < n || inFlight {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				fmt.Printf("close %d %s\n", ce.Code, ce.Text)
			}
			return false
		}
		fmt.Println(string(raw))

		var f struct {
			Type    string `json:"type"`
			V       int64  `json:"v"`
			Content string `json:"content"`
			Code    string `json:"code"`
		}
		_ = json.Unmarshal(raw, &f)
		switch {
		case f.Type == "snapshot":
			version, length = f.V, int64(utf8.RuneCountInString(f.Content))
			inFlight = false
		case f.Type == "ack":
			version, length = f.V, length+1
			inFlight = false
		case f.Type == "op":
			// Another client's op: this probe does not transform, so it
			// only floods documents nobody else is editing.
			version = f.V
		case f.Type == "error" && f.Code == "rate_limited":
			inFlight = false
		default:
			continue
		}
		if !inFlight && sent < n && !send() {
			return false
		}
	}
	return true
}
