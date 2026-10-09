package session

import "collab-docs-platform/doc-service/internal/ot"

type ringEntry struct {
	version int64
	op      ot.Operation
}

// ring keeps the last cap operations in version order. It is what an
// incoming op is transformed against, so its length is how far behind a
// client may be before it has to reload.
type ring struct {
	buf   []ringEntry
	start int
	n     int
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]ringEntry, capacity)}
}

func (r *ring) push(e ringEntry) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
		return
	}
	r.buf[r.start] = e
	r.start = (r.start + 1) % len(r.buf)
}

func (r *ring) at(i int) ringEntry {
	return r.buf[(r.start+i)%len(r.buf)]
}

// after returns the entries with version > v, oldest first, and false when
// the ring no longer holds all of them. current is the document version.
func (r *ring) after(v, current int64) ([]ringEntry, bool) {
	if v < 0 || v > current {
		return nil, false
	}
	need := current - v
	if need > int64(r.n) {
		return nil, false
	}
	out := make([]ringEntry, 0, need)
	for i := r.n - int(need); i < r.n; i++ {
		out = append(out, r.at(i))
	}
	return out, true
}
