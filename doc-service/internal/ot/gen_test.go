package ot

import (
	"math/rand"
	"testing"
)

// The generator behind the property tests. Documents mix ASCII with
// multi-byte and non-BMP runes so that a byte/code-point confusion anywhere
// in the package shows up as a failed property, not as a passing test over
// plain ASCII.
var alphabet = []rune("abcdefgh \nşğüİ日本😀🎉")

func randomDoc(r *rand.Rand, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(out)
}

func randomText(r *rand.Rand) string {
	return randomDoc(r, 1+r.Intn(4))
}

// randomOp walks the document, choosing at each step to retain, delete or
// insert, and may also insert at the very end. The result is canonical
// (Normalize) and has BaseLen == len(doc).
func randomOp(r *rand.Rand, doc string) Operation {
	n := len([]rune(doc))
	var op Operation
	pos := 0
	for pos < n {
		k := 1 + r.Intn(n-pos)
		switch r.Intn(3) {
		case 0:
			op = append(op, Retain(k))
			pos += k
		case 1:
			op = append(op, Delete(k))
			pos += k
		case 2:
			op = append(op, Insert(randomText(r)))
		}
	}
	if r.Intn(3) == 0 {
		op = append(op, Insert(randomText(r)))
	}
	return Normalize(op)
}

// iterations is the property-test budget: the README's 10 000 by default,
// a tenth of that under -short.
func iterations() int {
	if testing.Short() {
		return 1_000
	}
	return 10_000
}

func mustApply(t *testing.T, doc string, op Operation) string {
	t.Helper()
	out, err := Apply(doc, op)
	if err != nil {
		t.Fatalf("apply %s to %q: %v", op, doc, err)
	}
	return out
}
