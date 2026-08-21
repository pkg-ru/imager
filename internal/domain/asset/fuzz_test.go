package asset

import (
	"testing"
)

// FuzzParse проверяет, что Parse никогда не паникует и что успешный разбор
// всегда даёт идемпотентную каноническую форму.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"/v1/photos/photo-1-jpg/c-120x80@2.webp",
		"/v1/logo-png/t-x50@3.png",
		"/v1/img-jpg/ct-180x@2.avif",
		"/v1/a/b/c/name-gif/c-10x10@3.gif",
		"/v1/photos/photo-1-jpg/thumb.webp",
		"/v1/photos/photo-1-jpg/trumb@2.webp",
		"/v1/photos/photo-1-jpg/x.webp",
		"",
		"/v1/../../etc/passwd-jpg/c-120x80@2.webp",
		"/v1/a%2fb/photo-jpg/c-120x80@2.webp",
		"/v1/photo-jpg/c-99999999999999999999x80@2.webp",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		req, err := Parse(raw)
		if err != nil {
			return
		}
		// Успешный разбор должен давать идемпотентную каноническую форму.
		built, err := req.Build()
		if err != nil {
			t.Fatalf("Build() error after successful Parse(%q): %v", raw, err)
		}
		req2, err := Parse("/v1/" + built)
		if err != nil {
			t.Fatalf("Parse(rebuild %q) error: %v", built, err)
		}
		built2, err := req2.Build()
		if err != nil {
			t.Fatalf("Build2() error: %v", err)
		}
		if built != built2 {
			t.Fatalf("canonicalization not idempotent: %q != %q", built, built2)
		}
		// CanonicalID должен быть стабильным.
		id1, err := req.CanonicalID()
		if err != nil {
			t.Fatalf("CanonicalID error: %v", err)
		}
		id2, err := req2.CanonicalID()
		if err != nil {
			t.Fatalf("CanonicalID2 error: %v", err)
		}
		if !id1.Equal(id2) {
			t.Fatalf("CanonicalID not stable: %q != %q", id1.Hash(), id2.Hash())
		}
	})
}

// FuzzParseSize проверяет, что ParseSize никогда не падает.
func FuzzParseSize(f *testing.F) {
	seeds := []string{"120x80", "x50", "180x", "x", "abc", "99999999999999999999x80"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseSize(s)
	})
}
