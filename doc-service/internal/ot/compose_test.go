package ot

import (
	"errors"
	"math/rand"
	"testing"
)

func TestComposeTable(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		a, b Operation
		want Operation
	}{
		{"retain/retain", "abc", Operation{Retain(3)}, Operation{Retain(3)}, Operation{Retain(3)}},
		{"retain/delete", "abc", Operation{Retain(3)}, Operation{Retain(1), Delete(2)}, Operation{Retain(1), Delete(2)}},
		{"delete passes through", "abc", Operation{Delete(1), Retain(2)}, Operation{Retain(2)}, Operation{Delete(1), Retain(2)}},
		{"b insert passes through", "abc", Operation{Retain(3)}, Operation{Retain(1), Insert("X"), Retain(2)}, Operation{Retain(1), Insert("X"), Retain(2)}},
		{"insert/retain keeps insert", "ab", Operation{Insert("XY"), Retain(2)}, Operation{Retain(4)}, Operation{Insert("XY"), Retain(2)}},
		{"insert/delete cancels", "ab", Operation{Insert("XY"), Retain(2)}, Operation{Delete(2), Retain(2)}, Operation{Retain(2)}},
		{"insert partially deleted", "ab", Operation{Insert("XYZ"), Retain(2)}, Operation{Retain(1), Delete(2), Retain(2)}, Operation{Insert("X"), Retain(2)}},
		{"delete the inserted and the original", "ab", Operation{Insert("X"), Retain(2)}, Operation{Delete(3)}, Operation{Delete(2)}},
		{"insert then insert same spot", "", Operation{Insert("b")}, Operation{Insert("a"), Retain(1)}, Operation{Insert("ab")}},
		{"empty ops", "", Operation{}, Operation{}, Operation{}},
	}
	for _, tc := range cases {
		got, err := Compose(tc.a, tc.b)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !Equal(got, tc.want) {
			t.Errorf("%s: Compose(%s, %s) = %s, want %s", tc.name, tc.a, tc.b, got, tc.want)
		}
		via := mustApply(t, mustApply(t, tc.doc, tc.a), tc.b)
		direct := mustApply(t, tc.doc, got)
		if via != direct {
			t.Errorf("%s: apply(apply(d,a),b) = %q but apply(d, c) = %q", tc.name, via, direct)
		}
	}
}

func TestComposeLengthMismatch(t *testing.T) {
	_, err := Compose(Operation{Retain(2)}, Operation{Retain(3)})
	if !errors.Is(err, ErrComposeLength) {
		t.Fatalf("err = %v, want ErrComposeLength", err)
	}
}

// The README's "single most valuable test": over random documents and ops,
// apply(apply(d, a), b) == apply(d, compose(a, b)).
func TestComposeProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < iterations(); i++ {
		doc := randomDoc(r, r.Intn(12))
		a := randomOp(r, doc)
		mid := mustApply(t, doc, a)
		b := randomOp(r, mid)
		checkCompose(t, doc, a, b)
	}
}

func checkCompose(t *testing.T, doc string, a, b Operation) {
	t.Helper()
	c, err := Compose(a, b)
	if err != nil {
		t.Fatalf("compose %s then %s on %q: %v", a, b, doc, err)
	}
	if err := Validate(c, 0); err != nil {
		t.Fatalf("compose %s then %s produced non-canonical %s: %v", a, b, c, err)
	}
	want := mustApply(t, mustApply(t, doc, a), b)
	got, err := Apply(doc, c)
	if err != nil {
		t.Fatalf("apply composed %s to %q: %v", c, doc, err)
	}
	if got != want {
		t.Fatalf("doc %q, a %s, b %s: apply(apply(d,a),b) = %q, apply(d, compose) = %q (compose = %s)", doc, a, b, want, got, c)
	}
}

// FuzzCompose derives a and b from the seed so the fuzzer explores the same
// space as the property test; `go test -fuzz=FuzzCompose` runs it open-ended.
func FuzzCompose(f *testing.F) {
	f.Add("hello world", int64(1))
	f.Add("", int64(2))
	f.Add("a😀b🎉c", int64(3))
	f.Fuzz(func(t *testing.T, doc string, seed int64) {
		r := rand.New(rand.NewSource(seed))
		a := randomOp(r, doc)
		b := randomOp(r, mustApply(t, doc, a))
		checkCompose(t, doc, a, b)
	})
}
