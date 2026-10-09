package repository_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/repository"
)

// The session's writes are SQL (a transaction, a duplicate-key mapping, a
// guarded update), so they run against a real MySQL. Set DOCS_TEST_DSN as for
// the permission matrix; without it this file skips.
func newStore(t *testing.T) (*repository.SessionStore, *repository.OpRepository, int64) {
	t.Helper()
	dsn := os.Getenv("DOCS_TEST_DSN")
	if dsn == "" {
		t.Skip("DOCS_TEST_DSN not set; skipping database-backed session store tests")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	docs := repository.NewDocumentRepository(db)
	ops := repository.NewOpRepository(db)
	doc := &domain.Document{OwnerID: 990001, Title: "session store test"}
	if err := docs.Create(doc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := docs.Delete(doc.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return repository.NewSessionStore(docs, ops), ops, doc.ID
}

func row(version int64, op string) domain.Op {
	return domain.Op{Version: version, UserID: 990001, Op: json.RawMessage(op)}
}

func TestAppendOpsIsAllOrNothing(t *testing.T) {
	store, _, docID := newStore(t)

	if err := store.AppendOps(docID, []domain.Op{row(1, `["a"]`), row(2, `[1,"b"]`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Version 3 is new, version 2 is taken: neither may be written.
	err := store.AppendOps(docID, []domain.Op{row(3, `[2,"c"]`), row(2, `[1,"x"]`)})
	if !errors.Is(err, domain.ErrVersionTaken) {
		t.Fatalf("append over a taken version = %v, want ErrVersionTaken", err)
	}

	got, err := store.OpsAfter(docID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("log = %+v, want versions 1 and 2", got)
	}
	var second []any
	if err := json.Unmarshal(got[1].Op, &second); err != nil || len(second) != 2 || second[1] != "b" {
		t.Fatalf("version 2 = %s, want the first writer's op", got[1].Op)
	}
}

func TestSnapshotNeverMovesBackwards(t *testing.T) {
	store, _, docID := newStore(t)

	if err := store.Snapshot(docID, "ab", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Snapshot(docID, "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Snapshot(docID, "stale", 2); err != nil {
		t.Fatal(err)
	}

	doc, err := store.Load(docID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Content != "ab" || doc.Version != 2 {
		t.Fatalf("document = %q at version %d, want \"ab\" at 2", doc.Content, doc.Version)
	}
}
