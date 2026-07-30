package migration

import (
	"reflect"
	"testing"
)

func TestUnionCSV(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want string
	}{
		{"disjoint", "Album,EP", "Single", "Album,EP,Single"},
		{"overlapping", "Album,EP", "EP,Single", "Album,EP,Single"},
		{"identical", "Album", "Album", "Album"},
		// Empty means "the default follow types", not "follow nothing" — so an empty
		// side must contribute nothing rather than blanking the result.
		{"empty side keeps the other", "Album,EP", "", "Album,EP"},
		{"empty other side", "", "Album", "Album"},
		{"both empty", "", "", ""},
		{"whitespace and blanks are dropped", "Album, ,EP ", " Single", "Album,EP,Single"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unionCSV(tc.a, tc.b); got != tc.want {
				t.Errorf("unionCSV(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestUnionStrings(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"disjoint keeps first order", []string{"b", "a"}, []string{"c"}, []string{"b", "a", "c"}},
		{"duplicates collapse", []string{"a"}, []string{"a"}, []string{"a"}},
		{"nil sides", nil, []string{"a"}, []string{"a"}},
		{"both nil is nil, not an empty set", nil, nil, nil},
		{"blank entries dropped", []string{"", "a"}, []string{""}, []string{"a"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unionStrings(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("unionStrings(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
