package migration

import "strings"

// Merging two rows into one always raises the same question: which side's value
// survives? For anything the user authored the answer here is "both" — a merge is
// MusicBrainz telling us two names referred to one thing, which is no reason to
// discard half of what someone asked for. These helpers are the union rules.

// unionCSV merges two comma-separated sets (CollectionArtist.FollowTypes), keeping
// the first side's order and dropping duplicates and blanks. An empty side
// contributes nothing rather than blanking the result: empty means "the default",
// not "none".
func unionCSV(a, b string) string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}

	for _, csv := range []string{a, b} {
		for _, part := range strings.Split(csv, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return strings.Join(out, ",")
}

// unionStrings merges two MBID sets, preserving the first side's order.
func unionStrings(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := map[string]bool{}

	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
