package ot

// head is a component being consumed piecewise by the two-pointer merges.
// Inserts are held as runes so they can be split at code-point boundaries.
type head struct {
	n int
	s []rune
}

func (h *head) empty() bool { return h.n == 0 && len(h.s) == 0 }

// cursor hands out an operation's components one at a time, reloading the
// head whenever the previous one is used up.
type cursor struct {
	op Operation
	i  int
	h  head
}

func (c *cursor) load() {
	if c.h.empty() && c.i < len(c.op) {
		comp := c.op[c.i]
		c.i++
		if comp.IsInsert() {
			c.h = head{s: []rune(comp.S)}
		} else {
			c.h = head{n: comp.N}
		}
	}
}

func (c *cursor) done() bool { return c.h.empty() && c.i >= len(c.op) }

// Compose returns c such that apply(apply(d, a), b) == apply(d, c).
// Precondition: TargetLen(a) == BaseLen(b), i.e. b was made against the
// document a produces. Two-pointer merge over the components, following the
// README table; the result is normalized.
func Compose(a, b Operation) (Operation, error) {
	if TargetLen(a) != BaseLen(b) {
		return nil, ErrComposeLength
	}
	var out Operation
	ca, cb := &cursor{op: a}, &cursor{op: b}
	for {
		ca.load()
		cb.load()
		if ca.done() && cb.done() {
			break
		}
		// a's deletes and b's inserts pass through unchanged: a's delete
		// removes original text b never saw; b's insert adds text a never saw.
		if ca.h.n < 0 {
			out = append(out, Delete(-ca.h.n))
			ca.h.n = 0
			continue
		}
		if len(cb.h.s) > 0 {
			out = append(out, Insert(string(cb.h.s)))
			cb.h.s = nil
			continue
		}
		if ca.done() || cb.done() {
			// Unreachable when the precondition holds; kept as a guard
			// against a corrupted operation.
			return nil, ErrComposeLength
		}
		switch {
		case ca.h.n > 0 && cb.h.n > 0: // retain / retain
			m := min(ca.h.n, cb.h.n)
			out = append(out, Retain(m))
			ca.h.n -= m
			cb.h.n -= m
		case ca.h.n > 0 && cb.h.n < 0: // retain / delete
			m := min(ca.h.n, -cb.h.n)
			out = append(out, Delete(m))
			ca.h.n -= m
			cb.h.n += m
		case len(ca.h.s) > 0 && cb.h.n > 0: // insert / retain: keep part of the insert
			m := min(len(ca.h.s), cb.h.n)
			out = append(out, Insert(string(ca.h.s[:m])))
			ca.h.s = ca.h.s[m:]
			cb.h.n -= m
		case len(ca.h.s) > 0 && cb.h.n < 0: // insert / delete: cancel
			m := min(len(ca.h.s), -cb.h.n)
			ca.h.s = ca.h.s[m:]
			cb.h.n += m
		}
	}
	return Normalize(out), nil
}
