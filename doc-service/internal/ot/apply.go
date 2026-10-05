package ot

// Apply returns op applied to doc. It walks the components with a cursor into
// the document's code points: retain copies, insert appends, delete skips.
// The base-length check up front is what makes the walk unable to overrun or
// underrun the input. O(len(doc)).
func Apply(doc string, op Operation) (string, error) {
	in := []rune(doc)
	if BaseLen(op) != len(in) {
		return "", ErrBaseLength
	}
	out := make([]rune, 0, TargetLen(op))
	pos := 0
	for _, c := range op {
		switch {
		case c.IsInsert():
			out = append(out, []rune(c.S)...)
		case c.IsRetain():
			out = append(out, in[pos:pos+c.N]...)
			pos += c.N
		case c.IsDelete():
			pos -= c.N
		}
	}
	return string(out), nil
}
