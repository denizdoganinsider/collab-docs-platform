package ws

import (
	"fmt"
	"testing"

	"collab-docs-platform/doc-service/internal/middleware"

	"github.com/gorilla/websocket"
)

// The 101 is written by the upgrader on the hijacked connection, not through
// the ResponseWriter, so the instance header has to be handed to Upgrade
// explicitly or the one response the balancer verification reads would be
// the one without it.
func TestUpgradeResponseCarriesTheInstanceHeader(t *testing.T) {
	h := newHarness(t)

	conn, resp, err := websocket.DefaultDialer.Dial(h.url(fmt.Sprint(docID)), headers(ownerID, origin))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if got := resp.Header.Get(middleware.InstanceHeaderName); got != "doc-test" {
		t.Errorf("101 %s = %q, want doc-test", middleware.InstanceHeaderName, got)
	}
}
