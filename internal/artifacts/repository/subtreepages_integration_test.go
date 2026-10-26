//go:build integration

package repository

import (
	"fmt"
	"slices"
	"testing"
)

func TestSubtreeItemsMatchesSubtreeAcrossPages(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	for i := range subtreePageSize + 20 {
		deploy(t, s, fmt.Sprintf("a/%c/f%04d.txt", 'a'+i%3, i), "x")
	}

	deploy(t, s, "b/other.txt", "y")

	for _, path := range []string{
		"",
		"a",
		"a/b",
		"b/other.txt",
	} {
		t.Run("/"+path, func(t *testing.T) {
			t.Parallel()

			item, err := s.Stat(t.Context(), "generic-local", path)
			if err != nil {
				t.Fatal(err)
			}

			want, err := s.Subtree(t.Context(), item)
			if err != nil {
				t.Fatal(err)
			}

			var got []Item

			for it, err := range s.SubtreeItems(t.Context(), item) {
				if err != nil {
					t.Fatal(err)
				}

				got = append(got, it)
			}

			if !slices.EqualFunc(got, want, func(a, b Item) bool {
				return a.ID == b.ID
			}) {
				t.Errorf("SubtreeItems yielded %d items, Subtree returned %d, or in another order", len(got), len(want))
			}
		})
	}
}
