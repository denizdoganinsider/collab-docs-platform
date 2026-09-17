package controller

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/repository"
	"collab-docs-platform/doc-service/internal/service"

	_ "github.com/go-sql-driver/mysql"
)

// The permission matrix runs against a real MySQL: the checks under test are
// SQL. Set DOCS_TEST_DSN (for the compose database:
// root:root@tcp(localhost:3308)/docs_service_db?parseTime=true), otherwise
// this file skips so `go test ./...` runs anywhere.
const testKey = "test-gateway-key"

type client struct {
	t   *testing.T
	url string
}

func (c *client) do(method, path string, userID int64, role string, body any) (int, map[string]any, []map[string]any) {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.url+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if userID > 0 {
		req.Header.Set(middleware.GatewayKeyHeader, testKey)
		req.Header.Set(middleware.UserIDHeader, fmt.Sprint(userID))
		req.Header.Set(middleware.UserRoleHeader, role)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var obj map[string]any
	var list []map[string]any
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &obj)
	} else if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &list)
	}
	return resp.StatusCode, obj, list
}

func newTestServer(t *testing.T) *client {
	t.Helper()
	dsn := os.Getenv("DOCS_TEST_DSN")
	if dsn == "" {
		t.Skip("DOCS_TEST_DSN not set; skipping database-backed permission matrix")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })

	docRepo := repository.NewDocumentRepository(db)
	memberRepo := repository.NewMemberRepository(db)
	e := NewRouter(Dependencies{
		GatewayKey: testKey,
		InstanceID: "doc-test",
		Documents:  NewDocumentController(service.NewDocumentService(docRepo, memberRepo)),
		Members:    NewMemberController(service.NewMemberService(memberRepo)),
	})

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &client{t: t, url: srv.URL}
}

// Users are bare ids: there is no users table in this schema and no FK, so
// any positive id is a valid member. High ids keep clear of real rows.
const (
	owner     = int64(910001)
	editor    = int64(910002)
	viewer    = int64(910003)
	stranger  = int64(910004)
	adminUser = int64(910009)
)

func TestPermissionMatrix(t *testing.T) {
	c := newTestServer(t)

	status, doc, _ := c.do("POST", "/documents", owner, "user", map[string]string{"title": "Design notes"})
	if status != http.StatusCreated || doc["role"] != "owner" || doc["version"] != float64(0) {
		t.Fatalf("create: status %d body %v", status, doc)
	}
	docID := int64(doc["id"].(float64))
	path := fmt.Sprintf("/documents/%d", docID)
	t.Cleanup(func() { c.do("DELETE", path, owner, "user", nil) })

	// Only the owner may share.
	for _, u := range []int64{editor, viewer, stranger} {
		if s, _, _ := c.do("PUT", fmt.Sprintf("%s/members/%d", path, viewer), u, "user", map[string]string{"role": "viewer"}); s != http.StatusForbidden {
			t.Errorf("PUT members by %d: status %d, want 403", u, s)
		}
	}
	if s, _, _ := c.do("PUT", fmt.Sprintf("%s/members/%d", path, editor), owner, "user", map[string]string{"role": "editor"}); s != http.StatusOK {
		t.Fatalf("share editor: status %d", s)
	}
	if s, _, _ := c.do("PUT", fmt.Sprintf("%s/members/%d", path, viewer), owner, "user", map[string]string{"role": "viewer"}); s != http.StatusOK {
		t.Fatalf("share viewer: status %d", s)
	}

	type row struct {
		name   string
		method string
		path   string
		body   any
		want   map[int64]int // user -> status
	}
	rows := []row{
		{"read", "GET", path, nil, map[int64]int{owner: 200, editor: 200, viewer: 200, stranger: 403}},
		{"members", "GET", path + "/members", nil, map[int64]int{owner: 200, editor: 200, viewer: 200, stranger: 403}},
		{"rename", "PATCH", path, map[string]string{"title": "x"}, map[int64]int{owner: 200, editor: 200, viewer: 403, stranger: 403}},
		{"share", "PUT", fmt.Sprintf("%s/members/%d", path, 910005), map[string]string{"role": "viewer"}, map[int64]int{owner: 200, editor: 403, viewer: 403, stranger: 403}},
		{"unshare", "DELETE", fmt.Sprintf("%s/members/%d", path, 910005), nil, map[int64]int{editor: 403, viewer: 403, stranger: 403, owner: 204}},
		{"delete", "DELETE", path, nil, map[int64]int{editor: 403, viewer: 403, stranger: 403}},
	}
	for _, r := range rows {
		// Deterministic order so "share then unshare" sees the row it made.
		for _, u := range []int64{editor, viewer, stranger, owner} {
			want, ok := r.want[u]
			if !ok {
				continue
			}
			if got, _, _ := c.do(r.method, r.path, u, "user", r.body); got != want {
				t.Errorf("%s by %d: status %d, want %d", r.name, u, got, want)
			}
		}
	}

	// The role travels with the read.
	_, view, _ := c.do("GET", path, viewer, "user", nil)
	if view["role"] != "viewer" || len(view["members"].([]any)) != 3 {
		t.Errorf("viewer's view: role %v, members %v", view["role"], view["members"])
	}

	// Listing is scoped to membership.
	if _, _, list := c.do("GET", "/documents", editor, "user", nil); len(list) != 1 || list[0]["role"] != "editor" {
		t.Errorf("editor's list: %v", list)
	}
	if _, _, list := c.do("GET", "/documents", stranger, "user", nil); len(list) != 0 {
		t.Errorf("stranger's list: %v, want empty", list)
	}

	// Removing a member is immediate: the next read is refused.
	if s, _, _ := c.do("DELETE", fmt.Sprintf("%s/members/%d", path, viewer), owner, "user", nil); s != http.StatusNoContent {
		t.Errorf("unshare viewer: %d", s)
	}
	if s, _, _ := c.do("GET", path, viewer, "user", nil); s != http.StatusForbidden {
		t.Errorf("read after unshare: %d, want 403", s)
	}
	if s, _, _ := c.do("DELETE", fmt.Sprintf("%s/members/%d", path, viewer), owner, "user", nil); s != http.StatusNotFound {
		t.Errorf("unshare twice: %d, want 404", s)
	}

	// The owner's own row is not a members-API concern.
	if s, _, _ := c.do("PUT", fmt.Sprintf("%s/members/%d", path, owner), owner, "user", map[string]string{"role": "viewer"}); s != http.StatusBadRequest {
		t.Errorf("owner self-demote: %d, want 400", s)
	}
	if s, _, _ := c.do("DELETE", fmt.Sprintf("%s/members/%d", path, owner), owner, "user", nil); s != http.StatusBadRequest {
		t.Errorf("owner self-remove: %d, want 400", s)
	}
	if s, _, _ := c.do("PUT", fmt.Sprintf("%s/members/%d", path, editor), owner, "user", map[string]string{"role": "owner"}); s != http.StatusBadRequest {
		t.Errorf("assign owner role: %d, want 400", s)
	}

	// Owner deletes; everything about the document is gone.
	if s, _, _ := c.do("DELETE", path, owner, "user", nil); s != http.StatusNoContent {
		t.Errorf("owner delete: %d", s)
	}
	if s, _, _ := c.do("GET", path, owner, "user", nil); s != http.StatusForbidden {
		t.Errorf("read after delete: %d, want 403", s)
	}
}

func TestEdgeAuthAndAdmin(t *testing.T) {
	c := newTestServer(t)

	// No gateway key: refused, whatever identity the caller claims.
	if s, _, _ := c.do("GET", "/documents", 0, "", nil); s != http.StatusUnauthorized {
		t.Errorf("no key: %d, want 401", s)
	}

	if s, _, _ := c.do("GET", "/admin/documents", owner, "user", nil); s != http.StatusForbidden {
		t.Errorf("admin list as user: %d, want 403", s)
	}
	s, page, _ := c.do("GET", "/admin/documents?per_page=1", adminUser, "admin", nil)
	if s != http.StatusOK || page["per_page"] != float64(1) || page["documents"] == nil {
		t.Errorf("admin list: %d %v", s, page)
	}
	if s, _, _ := c.do("GET", "/admin/documents?page=x", adminUser, "admin", nil); s != http.StatusBadRequest {
		t.Errorf("bad page: %d, want 400", s)
	}

	// Validation.
	if s, _, _ := c.do("POST", "/documents", owner, "user", map[string]string{"title": "  "}); s != http.StatusBadRequest {
		t.Errorf("empty title: %d, want 400", s)
	}
	if s, _, _ := c.do("GET", "/documents/abc", owner, "user", nil); s != http.StatusBadRequest {
		t.Errorf("bad id: %d, want 400", s)
	}
}
