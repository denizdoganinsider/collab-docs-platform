package service

import (
	"context"
	"errors"
	"testing"

	"collab-docs-platform/doc-service/internal/domain"
)

type erroringCache struct {
	*fakeCache
	invalidateErr   error
	setErr          error
	invalidateCalls int
	setCalls        int
}

func (c *erroringCache) Invalidate(ctx context.Context, userIDs ...int64) error {
	c.invalidateCalls++
	if c.invalidateErr != nil {
		return c.invalidateErr
	}
	return c.fakeCache.Invalidate(ctx, userIDs...)
}

func (c *erroringCache) Set(ctx context.Context, userID int64, docs []domain.DocumentSummary, token string) error {
	c.setCalls++
	if c.setErr != nil {
		return c.setErr
	}
	return c.fakeCache.Set(ctx, userID, docs, token)
}

type listFailingMembers struct {
	*fakeMembers
	listErr error
}

func (m *listFailingMembers) List(docID int64) ([]domain.Member, error) {
	return nil, m.listErr
}

type failingDocs struct {
	*fakeDocs
	listErr   error
	deleteErr error
}

func (d *failingDocs) ListForUser(userID int64, limit, offset int) ([]domain.DocumentSummary, error) {
	if d.listErr != nil {
		d.listCalls++
		return nil, d.listErr
	}
	return d.fakeDocs.ListForUser(userID, limit, offset)
}

func (d *failingDocs) Delete(id int64) error {
	if d.deleteErr != nil {
		return d.deleteErr
	}
	return d.fakeDocs.Delete(id)
}

func TestCreateSucceedsWhenInvalidationFails(t *testing.T) {
	cache := &erroringCache{fakeCache: newFakeCache(), invalidateErr: errors.New("redis down")}
	s := NewDocumentService(&fakeDocs{}, newFakeMembers(), nil, noLive{})
	s.UseListCache(cache)

	got, err := s.Create(1, "x")
	if err != nil {
		t.Fatalf("Create with a failing invalidation = %v, want nil", err)
	}
	if got == nil || got.ID != 7 || got.Title != "x" || got.Role != domain.RoleOwner {
		t.Fatalf("Create summary = %+v", got)
	}
	if cache.invalidateCalls != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", cache.invalidateCalls)
	}
}

func TestRenameSucceedsWhenMemberListFailsAndInvalidatesNothing(t *testing.T) {
	cache := newFakeCache()
	members := &listFailingMembers{fakeMembers: newFakeMembers(), listErr: errors.New("db down")}
	s := NewDocumentService(&fakeDocs{}, members, nil, noLive{})
	s.UseListCache(cache)

	got, err := s.Rename(7, 2, "renamed")
	if err != nil || got == nil || got.ID != 7 {
		t.Fatalf("Rename = %+v, %v; want success", got, err)
	}
	if len(cache.invalidated) != 0 {
		t.Fatalf("invalidated %v, want nothing", cache.invalidated)
	}
}

func TestDeleteReturnsMemberListErrorBeforeDeleting(t *testing.T) {
	listErr := errors.New("db down")
	docs := &fakeDocs{}
	members := &listFailingMembers{fakeMembers: newFakeMembers(), listErr: listErr}
	s := NewDocumentService(docs, members, nil, noLive{})
	s.UseListCache(newFakeCache())

	if err := s.Delete(7, 1); !errors.Is(err, listErr) {
		t.Fatalf("Delete = %v, want %v", err, listErr)
	}
	if docs.deleted {
		t.Fatal("document deleted although its members could not be read")
	}
}

func TestDeleteFailureInvalidatesNothing(t *testing.T) {
	deleteErr := errors.New("db down")
	cache := newFakeCache()
	s := NewDocumentService(&failingDocs{fakeDocs: &fakeDocs{}, deleteErr: deleteErr}, newFakeMembers(), nil, noLive{})
	s.UseListCache(cache)

	if err := s.Delete(7, 1); !errors.Is(err, deleteErr) {
		t.Fatalf("Delete = %v, want %v", err, deleteErr)
	}
	if len(cache.invalidated) != 0 {
		t.Fatalf("invalidated %v after a failed delete", cache.invalidated)
	}
}

func TestListDatabaseErrorOnMissIsNotCached(t *testing.T) {
	listErr := errors.New("db down")
	docs := &failingDocs{fakeDocs: &fakeDocs{}, listErr: listErr}
	cache := newFakeCache()
	s := NewDocumentService(docs, newFakeMembers(), nil, noLive{})
	s.UseListCache(cache)

	got, err := s.List(1, 1, DefaultPerPage)
	if !errors.Is(err, listErr) || got != nil {
		t.Fatalf("List = %+v, %v; want nil, %v", got, err, listErr)
	}
	if cache.sets != 0 {
		t.Fatalf("cache sets = %d, want 0", cache.sets)
	}
	if _, ok := cache.lists[1]; ok {
		t.Fatal("database failure stored in the cache")
	}
}

func TestListReturnsRowsWhenCacheSetFails(t *testing.T) {
	docs := &fakeDocs{list: []domain.DocumentSummary{{ID: 7, Title: "a"}}}
	cache := &erroringCache{fakeCache: newFakeCache(), setErr: errors.New("redis down")}
	s := NewDocumentService(docs, newFakeMembers(), nil, noLive{})
	s.UseListCache(cache)

	got, err := s.List(1, 1, DefaultPerPage)
	if err != nil || len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("List with a failing cache write = %+v, %v; want the database rows", got, err)
	}
	if cache.setCalls != 1 {
		t.Fatalf("Set calls = %d, want 1", cache.setCalls)
	}
}
