package ot

// Apply returns op applied to doc. It walks the components with a cursor into
// the document's code points: retain copies, insert appends, delete skips.
// The base-length check up front is what makes the walk unable to overrun or
// underrun the input; each step re-checks its own bound so an op whose
// length sum wrapped (Validate refuses those on the wire) still returns an
// error instead of indexing past the slice. O(len(doc)).
func Apply(doc string, op Operation) (string, error) {
	in := []rune(doc)
	if BaseLen(op) != len(in) {
		return "", ErrBaseLength
	}
	out := make([]rune, 0, max(TargetLen(op), 0))
	pos := 0
	for _, c := range op {
		switch {
		case c.IsInsert():
			out = append(out, []rune(c.S)...)
		case c.IsRetain():
			if c.N > len(in)-pos {
				return "", ErrBaseLength
			}
			out = append(out, in[pos:pos+c.N]...)
			pos += c.N
		case c.IsDelete():
			if -c.N > len(in)-pos {
				return "", ErrBaseLength
			}
			pos -= c.N
		}
	}
	return string(out), nil
}
