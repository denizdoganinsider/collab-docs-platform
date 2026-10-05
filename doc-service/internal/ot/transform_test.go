package ot

import (
	"errors"
	"math/rand"
	"testing"
)

func TestTransformHandCases(t *testing.T) {
	cases := []struct {
		name   string
		doc    string
		a, b   Operation
		wantA2 Operation
		wantB2 Operation
		result string
	}{
		{
			"insert vs insert same position",
			"ab",
			Operation{Retain(1), Insert("X"), Retain(1)},
			Operation{Retain(1), Insert("Y"), Retain(1)},
			Operation{Retain(1), Insert("X"), Retain(2)},
			Operation{Retain(2), Insert("Y"), Retain(1)},
			"aXYb",
		},
		{
			"delete overlapping delete",
			"abcde",
			Operation{Retain(1), Delete(3), Retain(1)}, // bcd
			Operation{Retain(2), Delete(3)},            // cde
			Operation{Retain(1), Delete(1)},            // b remains to delete
			Operation{Retain(1), Delete(1)},            // e remains to delete
			"a",
		},
		{
			"insert inside a deleted range survives at the start of the deletion",
			"abcd",
			Operation{Retain(1), Delete(2), Retain(1)},
			Operation{Retain(2), Insert("X"), Retain(2)},
			Operation{Retain(1), Delete(1), Retain(1), Delete(1), Retain(1)},
			Operation{Retain(1), Insert("X"), Retain(1)},
			"aXd",
		},
		{
			"delete inside an inserted range",
			"abcd",
			Operation{Retain(2), Insert("XY"), Retain(2)},
			Operation{Retain(1), Delete(2), Retain(1)},
			Operation{Retain(1), Insert("XY"), Retain(1)},
			Operation{Retain(1), Delete(1), Retain(2), Delete(1), Retain(1)},
			"aXYd",
		},
		{
			"identical deletes cancel",
			"abc",
			Operation{Delete(3)},
			Operation{Delete(3)},
			Operation{},
			Operation{},
			"",
		},
	}
	for _, tc := range cases {
		a2, b2, err := Transform(tc.a, tc.b)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !Equal(a2, tc.wantA2) || !Equal(b2, tc.wantB2) {
			t.Errorf("%s: Transform(%s, %s) = (%s, %s), want (%s, %s)", tc.name, tc.a, tc.b, a2, b2, tc.wantA2, tc.wantB2)
		}
		left := mustApply(t, mustApply(t, tc.doc, tc.a), b2)
		right := mustApply(t, mustApply(t, tc.doc, tc.b), a2)
		if left != right || left != tc.result {
			t.Errorf("%s: via a then b' = %q, via b then a' = %q, want %q", tc.name, left, right, tc.result)
		}
	}
}

// Both peers insert at the same position of the same state. Whoever's op is
// logged first keeps the earlier position; the other side's text follows.
// Both the server and the client pass the logged op as `a`, so the texts
// converge. Passing the arguments the other way round converges on the
// *other* text, which is exactly the bug this test exists to catch.
func TestTransformConcurrentInsertLogOrderWins(t *testing.T) {
	doc := "hello"
	logged := Operation{Retain(5), Insert(" from A")}
	incoming := Operation{Retain(5), Insert(" from B")}

	// Server: the logged op is a; the incoming op is transformed past it.
	_, incoming2, err := Transform(logged, incoming)
	if err != nil {
		t.Fatal(err)
	}
	server := mustApply(t, mustApply(t, doc, logged), incoming2)

	// Client B: holds `incoming` as outstanding, receives `logged` from the
	// server; the server op is a.
	logged2, _, err := Transform(logged, incoming)
	if err != nil {
		t.Fatal(err)
	}
	clientB := mustApply(t, mustApply(t, doc, incoming), logged2)

	if server != "hello from A from B" {
		t.Fatalf("server text %q: the logged op must win the position", server)
	}
	if clientB != server {
		t.Fatalf("client B %q diverged from server %q", clientB, server)
	}

	// The wrong argument order converges to a different text: proof that the
	// convention is load-bearing, not cosmetic.
	swapped, _, _ := Transform(incoming, logged)
	if wrong := mustApply(t, mustApply(t, doc, logged), swapped); wrong == server {
		t.Fatalf("swapping the arguments should change the outcome; both gave %q", wrong)
	}
}

func TestTransformLengthMismatch(t *testing.T) {
	_, _, err := Transform(Operation{Retain(2)}, Operation{Retain(3)})
	if !errors.Is(err, ErrTransformLength) {
		t.Fatalf("err = %v, want ErrTransformLength", err)
	}
}

// TP1 over random ops: apply(apply(d, a), b') == apply(apply(d, b), a').
func TestTransformProperty(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < iterations(); i++ {
		doc := randomDoc(r, r.Intn(12))
		a := randomOp(r, doc)
		b := randomOp(r, doc)
		checkTransform(t, doc, a, b)
	}
}

func checkTransform(t *testing.T, doc string, a, b Operation) {
	t.Helper()
	a2, b2, err := Transform(a, b)
	if err != nil {
		t.Fatalf("transform %s, %s on %q: %v", a, b, doc, err)
	}
	for _, op := range []Operation{a2, b2} {
		if err := Validate(op, 0); err != nil {
			t.Fatalf("transform %s, %s produced non-canonical %s: %v", a, b, op, err)
		}
	}
	left := mustApply(t, mustApply(t, doc, a), b2)
	right := mustApply(t, mustApply(t, doc, b), a2)
	if left != right {
		t.Fatalf("doc %q, a %s, b %s: a then b' = %q, b then a' = %q (a' %s, b' %s)", doc, a, b, left, right, a2, b2)
	}
}

func FuzzTransform(f *testing.F) {
	f.Add("hello world", int64(1))
	f.Add("", int64(2))
	f.Add("a😀b🎉c", int64(3))
	f.Fuzz(func(t *testing.T, doc string, seed int64) {
		r := rand.New(rand.NewSource(seed))
		a := randomOp(r, doc)
		b := randomOp(r, doc)
		checkTransform(t, doc, a, b)
	})
}
