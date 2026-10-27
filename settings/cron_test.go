package settings

import (
	"reflect"
	"testing"
	"time"
)

// TestSummarizeCron: the shapes the defaults and a hand-written schedule take read as
// sentences; anything else is left to the next runs rather than guessed at.
func TestSummarizeCron(t *testing.T) {
	for expression, want := range map[string]string{
		"0 0 18 * * 7":   "Sundays at 18:00",
		"0 0 18 * * SUN": "Sundays at 18:00",
		"0 0 18 * * 0":   "Sundays at 18:00",
		"0 30 6 * * 1,4": "Mondays and Thursdays at 06:30",
		"0 0 7 * * 1-5":  "Weekdays at 07:00",
		"0 0 3 * * *":    "Every day at 03:00",
		"15 0 3 * * ?":   "Every day at 03:00:15",
		"0 0 4 1 * *":    "Monthly on day 1 at 04:00",
		"0 30 * * * *":   "Every hour at :30",
		"0 0 */6 * * *":  "Every 6 hours at :00",
		"0 */5 * * * *":  "Every 5 minutes",
		"0 */1 * * * *":  "Every minute",
		"*/10 * * * * *": "Every 10 seconds",
		"0 0 3 * 1 *":    "",
		"0 0 3,15 * * *": "",
		"0 0 3 * * 2-4":  "",
	} {
		if got := summarizeCron(expression); got != want {
			t.Errorf("summarizeCron(%q) = %q, want %q", expression, got, want)
		}
	}
}

// TestDescribeSchedule: next runs are on the clock of the time given, the day strip
// names the weekday of a weekly job and nothing for a daily one, and a schedule too
// frequent for points is a band.
func TestDescribeSchedule(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Skip("no tzdata")
	}
	// A Wednesday morning.
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, oslo)

	weekly := DescribeSchedule("0 0 18 * * 7", now)
	if weekly.Error != "" || weekly.Summary != "Sundays at 18:00" {
		t.Fatalf("weekly = %+v", weekly)
	}
	if want := []string{"Sun 1 Nov, 18:00", "Sun 8 Nov, 18:00", "Sun 15 Nov, 18:00"}; !reflect.DeepEqual(weekly.Next, want) {
		t.Errorf("next = %v, want %v", weekly.Next, want)
	}
	if want := []ScheduleTime{{Minute: 18 * 60, Label: "18:00", Days: "Sun"}}; !reflect.DeepEqual(weekly.Day, want) {
		t.Errorf("day = %+v, want %+v", weekly.Day, want)
	}

	daily := DescribeSchedule("0 0 3 * * *", now)
	if want := []ScheduleTime{{Minute: 180, Label: "03:00"}}; !reflect.DeepEqual(daily.Day, want) {
		t.Errorf("daily day = %+v, want %+v", daily.Day, want)
	}
	if len(daily.Next) != 3 || daily.Next[0] != "Thu 29 Oct, 03:00" {
		t.Errorf("daily next = %v", daily.Next)
	}

	hourly := DescribeSchedule("0 0 */6 * * *", now)
	if len(hourly.Day) != 4 || hourly.Dense {
		t.Errorf("every 6 hours = %+v, want four points", hourly)
	}

	frequent := DescribeSchedule("0 */5 * * * *", now)
	if !frequent.Dense || len(frequent.Day) != 0 || frequent.Summary != "Every 5 minutes" {
		t.Errorf("every 5 minutes = %+v, want a band", frequent)
	}
	if len(frequent.Next) != 3 || frequent.Next[0] != "Wed 28 Oct, 09:05" {
		t.Errorf("frequent next = %v", frequent.Next)
	}

	bad := DescribeSchedule("0 0 18 * *", now)
	if bad.Error == "" || len(bad.Next) != 0 {
		t.Errorf("five fields = %+v, want an error", bad)
	}
}

// TestDescribeCarriesSchedules: every cron field in the view has its schedule, and a
// schedule naming a switch names a bool field that exists.
func TestDescribeCarriesSchedules(t *testing.T) {
	view := Describe(baseConfig())
	types := map[string]string{}
	for _, section := range view.Sections {
		for _, field := range section.Fields {
			types[field.Key] = field.Type
		}
	}
	for _, section := range view.Sections {
		for _, field := range section.Fields {
			if field.Type == TypeCron && field.Schedule == nil {
				t.Errorf("%s has no schedule", field.Key)
			}
			if field.Type != TypeCron && field.Schedule != nil {
				t.Errorf("%s is not a schedule but carries one", field.Key)
			}
			if field.EnabledBy != "" && types[field.EnabledBy] != TypeBool {
				t.Errorf("%s is enabled by %q, which is not a switch on this page", field.Key, field.EnabledBy)
			}
		}
	}
}

// TestEverySectionHasAPage: the index lists Pages(), and the page renders the sections
// naming it, so a section on no page — or a page with no section — is a setting nobody
// can reach or an empty screen. Sections of one page sit together, in page order.
func TestEverySectionHasAPage(t *testing.T) {
	order := map[string]int{}
	for i, page := range Pages() {
		if page.Group == "" || page.Title == "" {
			t.Errorf("page %s has no group or title", page.ID)
		}
		if _, dup := order[page.ID]; dup {
			t.Errorf("page %s is listed twice", page.ID)
		}
		order[page.ID] = i
	}
	used := map[string]bool{}
	last := -1
	for _, section := range Sections() {
		i, ok := order[section.Page]
		if !ok {
			t.Errorf("section %s is on page %q, which is not in Pages()", section.ID, section.Page)
			continue
		}
		if i < last {
			t.Errorf("section %s is out of page order", section.ID)
		}
		last = i
		used[section.Page] = true
	}
	for _, page := range Pages() {
		if !used[page.ID] {
			t.Errorf("page %s has no section", page.ID)
		}
	}
}
