package session

import (
	"encoding/json"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/ot"
)

// Close codes of the wire protocol (README, "Wire protocol").
const (
	CloseGoingAway     = 1001
	CloseServerError   = 1011
	CloseRevoked       = 4003
	CloseDeleted       = 4004
	CloseProtocol      = 4400
	CloseTooFarBehind  = 4409
	reasonTooFarBehind = "too_far_behind"
)

// Frame types.
const (
	TypeOp       = "op"
	TypeCursor   = "cursor"
	TypePing     = "ping"
	TypeSnapshot = "snapshot"
	TypeAck      = "ack"
	TypePresence = "presence"
	TypeTitle    = "title"
	TypeError    = "error"
)

// Inbound is a client frame after JSON decoding. The read pump decodes (a
// frame that is not JSON never reaches the session) and the session decides
// what the type means.
type Inbound struct {
	Type string       `json:"type"`
	V    int64        `json:"v"`
	Op   ot.Operation `json:"op"`
	Seq  int64        `json:"seq"`
	Pos  int          `json:"pos"`
	Sel  int          `json:"sel"`
}

type presenceUser struct {
	UserID int64 `json:"user_id"`
}

type snapshotFrame struct {
	Type     string          `json:"type"`
	DocID    int64           `json:"doc_id"`
	Title    string          `json:"title"`
	V        int64           `json:"v"`
	Content  string          `json:"content"`
	Role     string          `json:"role"`
	Members  []domain.Member `json:"members"`
	Presence []presenceUser  `json:"presence"`
}

type ackFrame struct {
	Type string `json:"type"`
	V    int64  `json:"v"`
	Seq  int64  `json:"seq"`
}

type opFrame struct {
	Type   string          `json:"type"`
	V      int64           `json:"v"`
	UserID int64           `json:"user_id"`
	Op     json.RawMessage `json:"op"`
	Seq    int64           `json:"seq"`
}

type cursorFrame struct {
	Type   string `json:"type"`
	UserID int64  `json:"user_id"`
	Pos    int    `json:"pos"`
	Sel    int    `json:"sel"`
	V      int64  `json:"v"`
}

type presenceFrame struct {
	Type  string         `json:"type"`
	Users []presenceUser `json:"users"`
}

type titleFrame struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

type errorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// encode cannot fail for the frame structs above: they hold only strings,
// integers and already-valid JSON.
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("session: frame does not encode: " + err.Error())
	}
	return b
}
