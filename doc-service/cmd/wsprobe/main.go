// wsprobe is the verification client for the WebSocket protocol: it dials a
// /ws URL, sends the frames given on the command line (one JSON text frame
// per argument) and prints every frame it receives, one per line, until the
// socket closes or --wait elapses. The close code is printed as the last line
// (close 4409 too_far_behind). It replaces websocat, which is not installed
// everywhere, and is what scripts/e2e.sh drives.
//
//	go run ./cmd/wsprobe --wait 1s 'ws://localhost:9000/ws?doc=1&ticket=…' '{"type":"op","v":0,"op":["hi"],"seq":1}'
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	wait := flag.Duration("wait", time.Second, "how long to keep reading after the last frame was sent")
	origin := flag.String("origin", "", "Origin header to send (none by default)")
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

	for _, frame := range flag.Args()[1:] {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
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
