package utilities

import (
	"testing"
	"time"
)

func TestValidatePasswordFormat(t *testing.T) {
	tests := []struct {
		pw   string
		want bool
	}{
		{"Abcdef12", true},   // 8 chars, upper, lower, digit
		{"short1A", false},   // too short
		{"alllower1", false}, // no uppercase
		{"ALLUPPER1", false}, // no lowercase
		{"NoDigitsHere", false},
	}
	for _, tt := range tests {
		got, _, err := ValidatePasswordFormat(tt.pw)
		if err != nil {
			t.Fatalf("ValidatePasswordFormat(%q) error: %v", tt.pw, err)
		}
		if got != tt.want {
			t.Errorf("ValidatePasswordFormat(%q) = %v, want %v", tt.pw, got, tt.want)
		}
	}
}

func TestRemoveIntFromArray(t *testing.T) {
	got := RemoveIntFromArray([]int{1, 2, 3, 2, 4}, 2)
	want := []int{1, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("RemoveIntFromArray len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RemoveIntFromArray[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestIntToPaddedStringEdges(t *testing.T) {
	cases := map[int]string{0: "00", 9: "09", 10: "10", 123: "123"}
	for in, want := range cases {
		if got := IntToPaddedString(in); got != want {
			t.Errorf("IntToPaddedString(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSetClockHelpers(t *testing.T) {
	base := time.Date(2024, 3, 15, 8, 30, 45, 123, time.UTC)

	minv := SetClockToMinimum(base)
	if minv.Hour() != 0 || minv.Minute() != 0 || minv.Second() != 0 {
		t.Errorf("SetClockToMinimum = %v, want 00:00:00", minv)
	}
	maxv := SetClockToMaximum(base)
	if maxv.Hour() != 23 || maxv.Minute() != 59 || maxv.Second() != 59 {
		t.Errorf("SetClockToMaximum = %v, want 23:59:59", maxv)
	}
	// day must be preserved
	if minv.Day() != 15 || maxv.Day() != 15 {
		t.Errorf("SetClock* changed the day: min=%v max=%v", minv, maxv)
	}
}

func TestTimeToMySQLTimestamp(t *testing.T) {
	ts := TimeToMySQLTimestamp(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))
	if ts != "2024-01-02 03:04:05.000" {
		t.Errorf("TimeToMySQLTimestamp = %q, want 2024-01-02 03:04:05.000", ts)
	}
}

func TestFindNextSunday(t *testing.T) {
	// 2024-03-15 is a Friday; next Sunday is 2024-03-17.
	got, err := FindNextSunday(time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FindNextSunday error: %v", err)
	}
	if got.Weekday() != time.Sunday {
		t.Errorf("FindNextSunday weekday = %v, want Sunday", got.Weekday())
	}
	if got.Day() != 17 {
		t.Errorf("FindNextSunday day = %d, want 17", got.Day())
	}

	// When already Sunday, it returns that same day.
	sun := time.Date(2024, 3, 17, 9, 0, 0, 0, time.UTC)
	got2, _ := FindNextSunday(sun)
	if got2.Day() != 17 {
		t.Errorf("FindNextSunday(sunday) day = %d, want 17", got2.Day())
	}
}

func TestFindEarlierMondayAndSunday(t *testing.T) {
	// 2024-03-15 (Friday): earlier Monday is 2024-03-11, earlier Sunday is 2024-03-10.
	fri := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)

	mon, err := FindEarlierMonday(fri)
	if err != nil {
		t.Fatalf("FindEarlierMonday error: %v", err)
	}
	if mon.Weekday() != time.Monday || mon.Day() != 11 {
		t.Errorf("FindEarlierMonday = %v, want Monday 11th", mon)
	}

	sun, err := FindEarlierSunday(fri)
	if err != nil {
		t.Fatalf("FindEarlierSunday error: %v", err)
	}
	if sun.Weekday() != time.Sunday || sun.Day() != 10 {
		t.Errorf("FindEarlierSunday = %v, want Sunday 10th", sun)
	}
}

func TestNormalizePathForExternalTool(t *testing.T) {
	// On non-Windows this is effectively identity; just assert no error and a
	// non-empty result for a normal path.
	got, err := NormalizePathForExternalTool("/music/artist/track.flac")
	if err != nil {
		t.Fatalf("NormalizePathForExternalTool error: %v", err)
	}
	if got == "" {
		t.Error("expected a non-empty normalized path")
	}
}
