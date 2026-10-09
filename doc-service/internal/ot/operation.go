package ot

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Component is one step of an Operation, applied left to right over the whole
// document: retain (N > 0) skips N code points, delete (N < 0) removes -N code
// points, insert (S != "", N == 0) inserts S at the current position.
//
// The zero Component is neither; Validate rejects it and Normalize drops it.
type Component struct {
	N int
	S string
}

func Retain(n int) Component    { return Component{N: n} }
func Delete(n int) Component    { return Component{N: -n} }
func Insert(s string) Component { return Component{S: s} }

func (c Component) IsRetain() bool { return c.N > 0 }
func (c Component) IsDelete() bool { return c.N < 0 }
func (c Component) IsInsert() bool { return c.N == 0 && c.S != "" }
func (c Component) isZero() bool   { return c.N == 0 && c.S == "" }

// Len is the component's length in code points: the retain or delete count,
// or the insert's rune count.
func (c Component) Len() int {
	switch {
	case c.N < 0:
		return -c.N
	case c.N > 0:
		return c.N
	}
	return utf8.RuneCountInString(c.S)
}

// Operation is a sequence of components. Its JSON form is the README's array:
// a positive integer retains, a non-empty string inserts, a negative integer
// deletes — [6, "brave ", -5, "there"].
type Operation []Component

var (
	// ErrBaseLength: BaseLen(op) differs from the length of the document.
	ErrBaseLength = errors.New("ot: operation base length does not match document length")
	// ErrComposeLength: TargetLen(a) differs from BaseLen(b).
	ErrComposeLength = errors.New("ot: compose: target length of a does not match base length of b")
	// ErrTransformLength: the two operations were not made against the same
	// document length.
	ErrTransformLength = errors.New("ot: transform: operations have different base lengths")
)

// ValidationError is a malformed operation from the wire (400 / error frame).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "ot: " + e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// BaseLen is the length of a document the operation can be applied to:
// retains plus deletes.
func BaseLen(op Operation) int {
	n := 0
	for _, c := range op {
		if !c.IsInsert() {
			n += c.Len()
		}
	}
	return n
}

// TargetLen is the length of the document after the operation: retains plus
// inserts.
func TargetLen(op Operation) int {
	n := 0
	for _, c := range op {
		if !c.IsDelete() {
			n += c.Len()
		}
	}
	return n
}

// MaxComponentLen bounds a single retain, delete or insert. The length sums
// (BaseLen, TargetLen) are plain ints: a wire op with components near
// MaxInt64 would wrap them past the document-length checks and reach a
// slice index. Bounded per component, the sums of any op that fits in a
// frame cannot overflow.
const MaxComponentLen = 1 << 30

// Validate checks the canonical-form invariants on an operation that arrived
// from the wire: no zero components, no component longer than MaxComponentLen
// (or than maxTargetLen, when set), no two adjacent components of the same
// kind, no delete directly followed by an insert, and a result no longer than
// maxTargetLen code points (0 disables that bound). JSON type errors are
// caught earlier, by UnmarshalJSON.
func Validate(op Operation, maxTargetLen int) error {
	limit := MaxComponentLen
	if maxTargetLen > 0 && maxTargetLen < limit {
		limit = maxTargetLen
	}
	for i, c := range op {
		if c.isZero() {
			return invalid("component %d is empty", i)
		}
		if c.N != 0 && c.S != "" {
			return invalid("component %d is both a count and a string", i)
		}
		if c.N == math.MinInt64 || c.Len() > limit {
			return invalid("component %d is longer than %d code points", i, limit)
		}
		if i == 0 {
			continue
		}
		prev := op[i-1]
		switch {
		case prev.IsRetain() && c.IsRetain(), prev.IsDelete() && c.IsDelete(), prev.IsInsert() && c.IsInsert():
			return invalid("components %d and %d must be merged", i-1, i)
		case prev.IsDelete() && c.IsInsert():
			return invalid("delete at %d must follow, not precede, the insert at %d", i-1, i)
		}
	}
	if maxTargetLen > 0 {
		if n := TargetLen(op); n > maxTargetLen {
			return invalid("operation result is %d code points, limit is %d", n, maxTargetLen)
		}
	}
	return nil
}

// Normalize returns the canonical form of op: zero components dropped,
// adjacent components of one kind merged, and every delete-then-insert pair
// reordered to insert-then-delete (the two are equivalent: both replace the
// deleted range with the inserted text). Idempotent. The input is not
// modified.
func Normalize(op Operation) Operation {
	out := make(Operation, 0, len(op))
	for _, c := range op {
		switch {
		case c.isZero():
			continue
		case c.IsInsert():
			// An insert belongs before a trailing delete; after the move it
			// may touch an earlier insert, which it then joins.
			i := len(out)
			if i > 0 && out[i-1].IsDelete() {
				i--
			}
			if i > 0 && out[i-1].IsInsert() {
				out[i-1].S += c.S
				continue
			}
			out = append(out, Component{})
			copy(out[i+1:], out[i:])
			out[i] = c
		default:
			if n := len(out); n > 0 && (out[n-1].N > 0) == (c.N > 0) && !out[n-1].IsInsert() {
				out[n-1].N += c.N
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

// Equal reports whether two operations have identical components.
func Equal(a, b Operation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (op Operation) String() string {
	b, _ := json.Marshal(op)
	return string(b)
}

// MarshalJSON writes the README array form.
func (op Operation) MarshalJSON() ([]byte, error) {
	parts := make([]any, len(op))
	for i, c := range op {
		if c.IsInsert() {
			parts[i] = c.S
		} else {
			parts[i] = c.N
		}
	}
	return json.Marshal(parts)
}

// UnmarshalJSON reads the array form. Anything but a JSON array of integers
// and strings is a ValidationError: floats, booleans, null and nested values
// are refused; canonical form is Validate's job.
func (op *Operation) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return invalid("operation must be a JSON array")
	}
	out := make(Operation, 0, len(raw))
	for i, r := range raw {
		s := strings.TrimSpace(string(r))
		switch {
		case strings.HasPrefix(s, `"`):
			var text string
			if err := json.Unmarshal(r, &text); err != nil {
				return invalid("component %d is not a valid string", i)
			}
			out = append(out, Component{S: text})
		case s == "" || !(s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) || strings.ContainsAny(s, ".eE"):
			return invalid("component %d must be an integer or a string, not %s", i, s)
		default:
			var n int
			if err := json.Unmarshal(r, &n); err != nil {
				return invalid("component %d must be an integer or a string", i)
			}
			out = append(out, Component{N: n})
		}
	}
	*op = out
	return nil
}
