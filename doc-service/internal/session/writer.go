package session

import "collab-docs-platform/doc-service/internal/domain"

// Store is the persistence a session needs. AppendOps is all-or-nothing and
// reports a version someone else already wrote as domain.ErrVersionTaken.
type Store interface {
	Load(docID int64) (*domain.Document, error)
	OpsAfter(docID, from int64, limit int) ([]domain.Op, error)
	AppendOps(docID int64, ops []domain.Op) error
	Snapshot(docID int64, content string, version int64) error
}

// pending is an op the session has applied in memory and not yet seen
// committed. Nobody is told about it until it is.
type pending struct {
	client Client
	seq    int64
	op     domain.Op
}

type snapshot struct {
	content string
	version int64
}

// job is one trip to the database: the ops queued since the last trip, then
// (optionally) a snapshot. The snapshot is taken at the version of the last
// op in the same job, so it is never ahead of the log.
type job struct {
	ops  []pending
	snap *snapshot
}

type result struct {
	job     job
	err     error // AppendOps failed: the session cannot continue
	snapErr error // Snapshot failed: retried on the next interval
}

// writer is the session's only path to the database after load. It runs in
// its own goroutine so a slow commit delays acks, not the session loop; one
// job at a time keeps inserts in version order.
func writer(docID int64, store Store, jobs <-chan job, results chan<- result) {
	for j := range jobs {
		res := result{job: j}
		if len(j.ops) > 0 {
			rows := make([]domain.Op, len(j.ops))
			for i, p := range j.ops {
				rows[i] = p.op
			}
			res.err = store.AppendOps(docID, rows)
		}
		if res.err == nil && j.snap != nil {
			res.snapErr = store.Snapshot(docID, j.snap.content, j.snap.version)
		}
		results <- res
	}
}
