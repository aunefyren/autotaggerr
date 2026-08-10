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

// TestEarlierCutoff: merging two artists must not lose wants. Every other follow
// setting merges toward wanting more, and the year cutoff is the one where the
// inclusive value is *not* the larger number — no cutoff at all is zero, and it has to
// beat any year rather than losing to it.
func TestEarlierCutoff(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{"two cutoffs keep the earlier", 2020, 2010, 2010},
		{"order does not matter", 2010, 2020, 2010},
		{"identical", 2015, 2015, 2015},
		// Zero means "no cutoff", the most inclusive setting there is. Treating it as
		// the smaller number by accident would be right; treating it as "want nothing
		// before year zero" and picking the other side would silently drop albums.
		{"no cutoff on one side wins", 2020, 0, 0},
		{"no cutoff on the other side wins", 0, 2020, 0},
		{"neither side has one", 0, 0, 0},
		{"a nonsense negative is treated as no cutoff", -5, 2020, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlierCutoff(tc.a, tc.b); got != tc.want {
				t.Errorf("earlierCutoff(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
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
