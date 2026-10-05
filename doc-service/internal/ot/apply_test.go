package ot

import (
	"errors"
	"testing"
)

func TestApplyReadmeExample(t *testing.T) {
	op := Operation{Retain(6), Insert("brave "), Delete(5), Insert("there")}
	got := mustApply(t, "hello world", op)
	if got != "hello brave there" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyBaseLengthMismatch(t *testing.T) {
	for _, op := range []Operation{
		{Retain(3)},            // too short for "hello"
		{Retain(6)},            // too long
		{Retain(5), Delete(1)}, // overruns
		{Insert("x")},          // base 0 on a non-empty doc
	} {
		if _, err := Apply("hello", op); !errors.Is(err, ErrBaseLength) {
			t.Errorf("%s: err = %v, want ErrBaseLength", op, err)
		}
	}
}

func TestApplyEmpty(t *testing.T) {
	if got := mustApply(t, "", Operation{}); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := mustApply(t, "", Operation{Insert("😀")}); got != "😀" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyNonBMP(t *testing.T) {
	// Each emoji is one position; a byte-based cursor would land inside one.
	doc := "a😀b🎉c"
	got := mustApply(t, doc, Operation{Retain(1), Delete(1), Retain(1), Insert("ş"), Delete(1), Retain(1)})
	if got != "abşc" {
		t.Fatalf("got %q", got)
	}
}
