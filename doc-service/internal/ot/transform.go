package ot

// Transform takes two operations made against the same document state and
// returns (a', b') such that apply(apply(d, a), b') == apply(apply(d, b), a').
// Precondition: BaseLen(a) == BaseLen(b).
//
// Tie-break for concurrent inserts at the same position: a's insert is placed
// first. The convention for the whole system is that the op already in the
// log wins the position, so both sides always pass the logged op as a:
//
//	server: _, clientOp' = Transform(loggedOp, clientOp)   // applies clientOp'
//	client: serverOp', outstanding' = Transform(serverOp, outstanding)
//
// If the two sides ever disagree on which argument is a, concurrent inserts
// at one position converge to different texts and nothing else in the system
// will say why. Both results are normalized.
func Transform(a, b Operation) (Operation, Operation, error) {
	if BaseLen(a) != BaseLen(b) {
		return nil, nil, ErrTransformLength
	}
	var a2, b2 Operation
	ca, cb := &cursor{op: a}, &cursor{op: b}
	for {
		ca.load()
		cb.load()
		if ca.done() && cb.done() {
			break
		}
		// Inserts first, a before b: the inserted text appears in a' and is
		// skipped over by b' (and vice versa).
		if len(ca.h.s) > 0 {
			a2 = append(a2, Insert(string(ca.h.s)))
			b2 = append(b2, Retain(len(ca.h.s)))
			ca.h.s = nil
			continue
		}
		if len(cb.h.s) > 0 {
			a2 = append(a2, Retain(len(cb.h.s)))
			b2 = append(b2, Insert(string(cb.h.s)))
			cb.h.s = nil
			continue
		}
		if ca.done() || cb.done() {
			return nil, nil, ErrTransformLength
		}
		switch {
		case ca.h.n > 0 && cb.h.n > 0: // retain / retain
			m := min(ca.h.n, cb.h.n)
			a2 = append(a2, Retain(m))
			b2 = append(b2, Retain(m))
			ca.h.n -= m
			cb.h.n -= m
		case ca.h.n < 0 && cb.h.n < 0: // delete / delete: both removed it
			m := min(-ca.h.n, -cb.h.n)
			ca.h.n += m
			cb.h.n += m
		case ca.h.n > 0 && cb.h.n < 0: // retain / delete: b' still deletes it
			m := min(ca.h.n, -cb.h.n)
			b2 = append(b2, Delete(m))
			ca.h.n -= m
			cb.h.n += m
		case ca.h.n < 0 && cb.h.n > 0: // delete / retain: a' still deletes it
			m := min(-ca.h.n, cb.h.n)
			a2 = append(a2, Delete(m))
			ca.h.n += m
			cb.h.n -= m
		}
	}
	return Normalize(a2), Normalize(b2), nil
}
