package ws

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"collab-docs-platform/doc-service/internal/session"

	"github.com/gorilla/websocket"
)

// Heartbeat and write bounds, the chat project's constants: two missed pings
// are tolerated before the read deadline closes the socket (3x rule).
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	// Frames queued for a socket that has not drained them. A client this
	// far behind is a slow consumer; the session closes it with 4409 and it
	// reloads, which is right for an editor: catching up op by op would
	// only be slower.
	sendBuffer = 64
)

// Client is one WebSocket connection joined to one document session. It is
// what the session sees behind session.Client: a user id, a non-blocking
// Send and a Close it may call from its own goroutine without waiting.
type Client struct {
	conn    *websocket.Conn
	userID  int64
	session *session.Session

	send    chan []byte
	closing chan struct{}
	once    sync.Once
	code    int
	reason  string
}

func newClient(conn *websocket.Conn, userID int64) *Client {
	return &Client{
		conn:    conn,
		userID:  userID,
		send:    make(chan []byte, sendBuffer),
		closing: make(chan struct{}),
	}
}

func (c *Client) UserID() int64 { return c.userID }

// Send queues a frame for the write pump. False means the buffer is full.
func (c *Client) Send(frame []byte) bool {
	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

// Close asks the write pump to send a close frame with code and reason and
// end the connection. Safe to call more than once and from any goroutine;
// only the first call's code is sent.
func (c *Client) Close(code int, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		close(c.closing)
	})
}

// readPump decodes frames and hands them to the session until the socket
// ends. Deliver blocks while the session is not reading (backpressure); the
// socket is simply not read meanwhile.
func (c *Client) readPump() {
	defer func() {
		c.session.Leave(c)
		c.Close(websocket.CloseNormalClosure, "")
	}()

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		kind, payload, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure) {
				slog.Warn("websocket read", "service", "doc-service", "user_id", c.userID, "error", err)
			}
			return
		}
		if kind != websocket.TextMessage {
			c.Close(websocket.CloseUnsupportedData, "text frames only")
			return
		}

		var in session.Inbound
		if err := json.Unmarshal(payload, &in); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				c.Close(websocket.CloseUnsupportedData, "malformed JSON")
			} else {
				// Valid JSON with the wrong shape: a float in an op, a
				// string where an integer goes. Protocol violation.
				c.Close(session.CloseProtocol, err.Error())
			}
			return
		}
		if !c.session.Deliver(c, in) {
			return
		}
	}
}

// writePump is the only goroutine that writes to the socket: queued frames,
// protocol pings, and finally the close frame the session (or the read pump)
// asked for.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case frame := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closing:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(c.code, c.reason))
			return
		}
	}
}
