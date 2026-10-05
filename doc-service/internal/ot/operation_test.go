package ot

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestJSONRoundTrip(t *testing.T) {
	const wire = `[6,"brave ",-5,"there"]`
	var op Operation
	if err := json.Unmarshal([]byte(wire), &op); err != nil {
		t.Fatal(err)
	}
	want := Operation{Retain(6), Insert("brave "), Delete(5), Insert("there")}
	if !Equal(op, want) {
		t.Fatalf("decoded %v, want %v", op, want)
	}
	if got := op.String(); got != wire {
		t.Fatalf("encoded %s, want %s", got, wire)
	}
}

func TestUnmarshalRejectsNonIntegerAndNonString(t *testing.T) {
	for _, wire := range []string{
		`[1.5]`, `[true]`, `[null]`, `[[1]]`, `[{"n":1}]`, `{"op":[1]}`, `"abc"`, `[1e3]`, `[-0.5]`,
	} {
		// Malformed JSON (e.g. `[1,`) never reaches UnmarshalJSON: the decoder
		// rejects it first, and the socket layer closes with 1003.
		var op Operation
		err := json.Unmarshal([]byte(wire), &op)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: got %v, want ValidationError", wire, err)
		}
	}
	// An empty array is a valid (empty) operation; its base length is 0.
	var op Operation
	if err := json.Unmarshal([]byte(`[]`), &op); err != nil || len(op) != 0 {
		t.Fatalf("empty array: %v %v", op, err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		op   Operation
		max  int
		ok   bool
	}{
		{"canonical", Operation{Retain(2), Insert("x"), Delete(1), Retain(3)}, 0, true},
		{"empty", Operation{}, 0, true},
		{"zero retain", Operation{Retain(0)}, 0, false},
		{"empty insert", Operation{Insert("")}, 0, false},
		{"count and string", Operation{{N: 1, S: "x"}}, 0, false},
		{"adjacent retains", Operation{Retain(1), Retain(1)}, 0, false},
		{"adjacent deletes", Operation{Delete(1), Delete(1)}, 0, false},
		{"adjacent inserts", Operation{Insert("a"), Insert("b")}, 0, false},
		{"delete then insert", Operation{Delete(1), Insert("a")}, 0, false},
		{"insert then delete", Operation{Insert("a"), Delete(1)}, 0, true},
		{"within limit", Operation{Retain(3), Insert("ab")}, 5, true},
		{"over limit", Operation{Retain(3), Insert("abc")}, 5, false},
		{"over limit counts code points", Operation{Insert("😀😀😀")}, 3, true},
	}
	for _, tc := range cases {
		err := Validate(tc.op, tc.max)
		if (err == nil) != tc.ok {
			t.Errorf("%s: Validate(%s, %d) = %v, want ok=%v", tc.name, tc.op, tc.max, err, tc.ok)
		}
		if err != nil {
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("%s: error is %T, want *ValidationError", tc.name, err)
			}
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   Operation
		want Operation
	}{
		{"drops zeros", Operation{Retain(0), Insert(""), Retain(2)}, Operation{Retain(2)}},
		{"merges retains", Operation{Retain(1), Retain(2)}, Operation{Retain(3)}},
		{"merges deletes", Operation{Delete(1), Delete(2)}, Operation{Delete(3)}},
		{"merges inserts", Operation{Insert("a"), Insert("b")}, Operation{Insert("ab")}},
		{"insert before delete", Operation{Delete(2), Insert("x")}, Operation{Insert("x"), Delete(2)}},
		{"insert joins earlier insert across delete", Operation{Insert("a"), Delete(2), Insert("b")}, Operation{Insert("ab"), Delete(2)}},
		{"retain delete insert", Operation{Retain(1), Delete(1), Insert("x"), Retain(1)}, Operation{Retain(1), Insert("x"), Delete(1), Retain(1)}},
		{"retain between keeps order", Operation{Delete(1), Retain(1), Insert("x")}, Operation{Delete(1), Retain(1), Insert("x")}},
		{"already canonical", Operation{Insert("x"), Delete(1), Retain(1)}, Operation{Insert("x"), Delete(1), Retain(1)}},
	}
	for _, tc := range cases {
		got := Normalize(tc.in)
		if !Equal(got, tc.want) {
			t.Errorf("%s: Normalize(%s) = %s, want %s", tc.name, tc.in, got, tc.want)
		}
		if again := Normalize(got); !Equal(again, got) {
			t.Errorf("%s: not idempotent: %s -> %s", tc.name, got, again)
		}
		if err := Validate(got, 0); err != nil {
			t.Errorf("%s: normalized op fails Validate: %v", tc.name, err)
		}
	}
}

func TestNormalizePreservesMeaning(t *testing.T) {
	// Delete-then-insert and insert-then-delete at one position must produce
	// the same text, otherwise reordering would not be a normalization.
	doc := "héllo😀"
	a := Operation{Retain(1), Delete(2), Insert("EY"), Retain(3)}
	b := Normalize(a)
	if mustApply(t, doc, a) != mustApply(t, doc, b) {
		t.Fatalf("%s and %s differ on %q", a, b, doc)
	}
	if mustApply(t, doc, b) != "hEYlo😀" {
		t.Fatalf("got %q", mustApply(t, doc, b))
	}
}

func TestLengthsCountCodePoints(t *testing.T) {
	op := Operation{Retain(2), Insert("😀ş"), Delete(1)}
	if got := BaseLen(op); got != 3 {
		t.Errorf("BaseLen = %d, want 3", got)
	}
	if got := TargetLen(op); got != 4 {
		t.Errorf("TargetLen = %d, want 4 (emoji and ş are one position each)", got)
	}
}
