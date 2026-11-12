package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/session"
)

type fakeCache struct {
	lists       map[int64][]domain.DocumentSummary
	getErr      error
	sets        int
	setTokens   []string
	invalidated []int64
}

func newFakeCache() *fakeCache { return &fakeCache{lists: map[int64][]domain.DocumentSummary{}} }

func (c *fakeCache) Get(_ context.Context, userID int64) ([]domain.DocumentSummary, bool, string, error) {
	if c.getErr != nil {
		return nil, false, "", c.getErr
	}
	docs, ok := c.lists[userID]
	return docs, ok, "gen", nil
}

func (c *fakeCache) Set(_ context.Context, userID int64, docs []domain.DocumentSummary, token string) error {
	c.sets++
	c.setTokens = append(c.setTokens, token)
	c.lists[userID] = docs
	return nil
}

func (c *fakeCache) Invalidate(_ context.Context, userIDs ...int64) error {
	c.invalidated = append(c.invalidated, userIDs...)
	for _, id := range userIDs {
		delete(c.lists, id)
	}
	return nil
}

type fakeDocs struct {
	listCalls int
	list      []domain.DocumentSummary
	deleted   bool
}

func (d *fakeDocs) Create(doc *domain.Document) error { doc.ID = 7; return nil }
func (d *fakeDocs) GetByID(id int64) (*domain.Document, error) {
	if d.deleted {
		return nil, sql.ErrNoRows
	}
	return &domain.Document{ID: id, OwnerID: 1, Title: "t"}, nil
}
func (d *fakeDocs) ListForUser(int64, int, int) ([]domain.DocumentSummary, error) {
	d.listCalls++
	return d.list, nil
}
func (d *fakeDocs) ListAll(int, int) ([]domain.DocumentSummary, error) { return nil, nil }
func (d *fakeDocs) CountAll() (int64, error)                           { return 0, nil }
func (d *fakeDocs) UpdateTitle(int64, string) error                    { return nil }
func (d *fakeDocs) Delete(int64) error                                 { d.deleted = true; return nil }

// fakeMembers holds one document: user 1 owner, 2 editor, 3 viewer.
type fakeMembers struct{ rows map[int64]string }

func newFakeMembers() *fakeMembers {
	return &fakeMembers{rows: map[int64]string{1: domain.RoleOwner, 2: domain.RoleEditor, 3: domain.RoleViewer}}
}

func (m *fakeMembers) GetRole(_, userID int64) (string, error) {
	role, ok := m.rows[userID]
	if !ok {
		return "", sql.ErrNoRows
	}
	return role, nil
}
func (m *fakeMembers) List(int64) ([]domain.Member, error) {
	var out []domain.Member
	for id, role := range m.rows {
		out = append(out, domain.Member{UserID: id, Role: role})
	}
	return out, nil
}
func (m *fakeMembers) Upsert(_, userID int64, role string) error { m.rows[userID] = role; return nil }
func (m *fakeMembers) Delete(_, userID int64) (bool, error) {
	_, ok := m.rows[userID]
	delete(m.rows, userID)
	return ok, nil
}

type noLive struct{}

func (noLive) View(int64) (session.View, bool) { return session.View{}, false }

func newCachedService() (*DocumentService, *fakeDocs, *fakeMembers, *fakeCache) {
	docs, members, cache := &fakeDocs{}, newFakeMembers(), newFakeCache()
	s := NewDocumentService(docs, members, nil, noLive{})
	s.UseListCache(cache)
	return s, docs, members, cache
}

func sorted(ids []int64) []int64 { out := slices.Clone(ids); slices.Sort(out); return out }

func TestListReadsTheDatabaseOncePerTTL(t *testing.T) {
	s, docs, _, _ := newCachedService()
	docs.list = []domain.DocumentSummary{{ID: 7, Title: "a"}}

	for range 5 {
		got, err := s.List(1, 1, DefaultPerPage)
		if err != nil || len(got) != 1 || got[0].ID != 7 {
			t.Fatalf("List = %+v, %v", got, err)
		}
	}
	if docs.listCalls != 1 || s.ListDBReads() != 1 {
		t.Fatalf("database reads = %d (counter %d), want 1", docs.listCalls, s.ListDBReads())
	}
}

// The fill must carry the generation the miss returned, or the cache cannot
// refuse a fill that an invalidation overtook.
func TestListFillCarriesTheTokenOfItsMiss(t *testing.T) {
	s, _, _, cache := newCachedService()
	_, _ = s.List(1, 1, DefaultPerPage)
	if len(cache.setTokens) != 1 || cache.setTokens[0] != "gen" {
		t.Fatalf("Set tokens = %v, want [gen]", cache.setTokens)
	}
}

func TestListCachesTheEmptyList(t *testing.T) {
	s, docs, _, cache := newCachedService()
	docs.list = []domain.DocumentSummary{}

	_, _ = s.List(9, 1, DefaultPerPage)
	_, _ = s.List(9, 1, DefaultPerPage)
	if docs.listCalls != 1 {
		t.Fatalf("database reads = %d, want 1 (empty list cached)", docs.listCalls)
	}
	if _, ok := cache.lists[9]; !ok {
		t.Fatal("empty list not stored")
	}
}

func TestListOtherPagesBypassTheCache(t *testing.T) {
	s, docs, _, cache := newCachedService()
	_, _ = s.List(1, 2, DefaultPerPage)
	_, _ = s.List(1, 1, 50)
	if docs.listCalls != 2 || cache.sets != 0 {
		t.Fatalf("db reads %d, cache sets %d; want 2 and 0", docs.listCalls, cache.sets)
	}
}

func TestListCacheErrorReadsDatabaseAndDoesNotWriteBack(t *testing.T) {
	s, docs, _, cache := newCachedService()
	cache.getErr = errors.New("redis down")
	docs.list = []domain.DocumentSummary{{ID: 7}}

	got, err := s.List(1, 1, DefaultPerPage)
	if err != nil || len(got) != 1 {
		t.Fatalf("List with cache down = %+v, %v; want the database answer", got, err)
	}
	if cache.sets != 0 {
		t.Fatal("wrote back to a cache that just failed")
	}
}

func TestCreateInvalidatesTheOwner(t *testing.T) {
	s, _, _, cache := newCachedService()
	if _, err := s.Create(1, "new"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cache.invalidated, []int64{1}) {
		t.Fatalf("invalidated %v, want [1]", cache.invalidated)
	}
}

func TestRenameInvalidatesEveryMember(t *testing.T) {
	s, _, _, cache := newCachedService()
	if _, err := s.Rename(7, 2, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := sorted(cache.invalidated); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("invalidated %v, want [1 2 3]", got)
	}
}

func TestDeleteInvalidatesMembersReadBeforeTheDelete(t *testing.T) {
	s, _, _, cache := newCachedService()
	if err := s.Delete(7, 1); err != nil {
		t.Fatal(err)
	}
	if got := sorted(cache.invalidated); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("invalidated %v, want [1 2 3]", got)
	}
}

func TestMemberChangesInvalidateTheTarget(t *testing.T) {
	members, cache := newFakeMembers(), newFakeCache()
	s := NewMemberService(members)
	s.UseListCache(cache)

	if _, err := s.Set(7, 1, 4, domain.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(7, 1, 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cache.invalidated, []int64{4, 2}) {
		t.Fatalf("invalidated %v, want [4 2]", cache.invalidated)
	}
}

func TestRefusedWritesInvalidateNothing(t *testing.T) {
	s, _, _, cache := newCachedService()
	_, _ = s.Rename(7, 3, "viewer cannot") // viewer
	_ = s.Delete(7, 2)                     // editor cannot
	ms := NewMemberService(newFakeMembers())
	ms.UseListCache(cache)
	_, _ = ms.Set(7, 2, 4, domain.RoleEditor) // not the owner
	if len(cache.invalidated) != 0 {
		t.Fatalf("refused writes invalidated %v", cache.invalidated)
	}
}

type fakeStore struct {
	session.Store
	snapErr error
}

func (f fakeStore) Snapshot(int64, string, int64) error { return f.snapErr }

func TestSnapshotInvalidatesEveryMember(t *testing.T) {
	cache := newFakeCache()
	store := InvalidatingStore{Store: fakeStore{}, Members: newFakeMembers(), Cache: cache}
	if err := store.Snapshot(7, "x", 5); err != nil {
		t.Fatal(err)
	}
	if got := sorted(cache.invalidated); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("invalidated %v, want [1 2 3]", got)
	}
}

func TestFailedSnapshotInvalidatesNothing(t *testing.T) {
	cache := newFakeCache()
	store := InvalidatingStore{Store: fakeStore{snapErr: errors.New("db down")}, Members: newFakeMembers(), Cache: cache}
	if err := store.Snapshot(7, "x", 5); err == nil {
		t.Fatal("snapshot error swallowed")
	}
	if len(cache.invalidated) != 0 {
		t.Fatalf("invalidated %v after a failed snapshot", cache.invalidated)
	}
}
