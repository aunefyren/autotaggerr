package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"codnect.io/chrono"
)

// Schedule is a cron expression made readable: what it means in words, when it next
// fires, and where in a day it lands. A six-field expression is an identifier, not an
// answer — "0 0 18 * * 7" is legible only to someone who already knows it means Sunday
// evening — so every schedule the page shows carries this beside it.
//
// Times are formatted here, in the server's zone, because that is the clock the
// scheduler runs on. A browser in another zone formatting the instants itself would
// show the scan at a time it never runs.
type Schedule struct {
	// Summary is the expression in words ("Every day at 03:00"), or empty when it
	// does not reduce to a sentence; Next always says when it runs.
	Summary string `json:"summary,omitempty"`
	// Next is the next few runs, formatted on the server's clock.
	Next []string `json:"next"`
	// Day is where the runs of the coming week land on a 24-hour clock: one entry per
	// time of day, naming the weekdays when it is not every day. Empty when Dense.
	Day []ScheduleTime `json:"day"`
	// Dense marks a schedule too frequent to draw as points (more than a run every
	// half hour); the page draws it as a band and Summary says how often.
	Dense bool `json:"dense,omitempty"`
	// Error is why the expression was rejected, for a live preview of a typed value.
	Error string `json:"error,omitempty"`
}

// ScheduleTime is one time of day a schedule fires at.
type ScheduleTime struct {
	Minute int    `json:"minute"`         // minutes after midnight, 0–1439
	Label  string `json:"label"`          // "18:00"
	Days   string `json:"days,omitempty"` // "Sun", "Mon, Thu"; empty for every day
}

const (
	scheduleNextCount = 3
	// scheduleWeek bounds the walk that places runs on the day strip: a week, because a
	// weekly job is the slowest the default schedules go.
	scheduleWeek = 7 * 24 * time.Hour
	// scheduleDenseAt is the distinct-times count past which a schedule is a band:
	// more than one point every half hour does not read as points.
	scheduleDenseAt = 48
	// scheduleWalkCap bounds the walk for a schedule that fires every second.
	scheduleWalkCap = 4000
)

// DescribeSchedule resolves an expression against now, in now's location. An invalid
// expression comes back with Error and nothing else.
func DescribeSchedule(expression string, now time.Time) Schedule {
	expression = strings.TrimSpace(expression)
	if err := validCron(expression); err != nil {
		return Schedule{Error: err.Error(), Next: []string{}, Day: []ScheduleTime{}}
	}
	cron, err := chrono.ParseCronExpression(expression)
	if err != nil {
		return Schedule{Error: err.Error(), Next: []string{}, Day: []ScheduleTime{}}
	}

	out := Schedule{Summary: summarizeCron(expression), Next: []string{}, Day: []ScheduleTime{}}
	days := map[int]map[time.Weekday]bool{}
	end := now.Add(scheduleWeek)
	t := now
	for i := 0; i < scheduleWalkCap; i++ {
		t = cron.NextTime(t)
		if t.IsZero() || t.After(end) {
			break
		}
		if len(out.Next) < scheduleNextCount {
			out.Next = append(out.Next, t.Format("Mon 2 Jan, 15:04"))
		}
		minute := t.Hour()*60 + t.Minute()
		if days[minute] == nil {
			days[minute] = map[time.Weekday]bool{}
		}
		days[minute][t.Weekday()] = true
		if len(days) > scheduleDenseAt {
			out.Dense = true
			break
		}
	}
	// The next runs are not bounded by the week: a weekly job still has three. The walk
	// above stopped on the first run past the week, which nothing has listed yet.
	for len(out.Next) < scheduleNextCount && !t.IsZero() {
		if t.After(end) {
			out.Next = append(out.Next, t.Format("Mon 2 Jan, 15:04"))
		}
		t = cron.NextTime(t)
	}
	if out.Dense {
		return out
	}

	minutes := make([]int, 0, len(days))
	for minute := range days {
		minutes = append(minutes, minute)
	}
	sort.Ints(minutes)
	for _, minute := range minutes {
		out.Day = append(out.Day, ScheduleTime{
			Minute: minute,
			Label:  fmt.Sprintf("%02d:%02d", minute/60, minute%60),
			Days:   weekdayList(days[minute]),
		})
	}
	return out
}

// weekdayList names the days a time of day fires on, or "" when it is all seven.
func weekdayList(set map[time.Weekday]bool) string {
	if len(set) == 7 {
		return ""
	}
	names := []string{}
	// Monday first, the way a week is read here.
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		if set[d] {
			names = append(names, d.String()[:3])
		}
	}
	return strings.Join(names, ", ")
}

// summarizeCron puts the common shapes of a six-field expression into words. It is
// deliberately narrow: a shape it does not know returns "", and the next runs say what
// the expression does. A wrong sentence would be worse than none.
func summarizeCron(expression string) string {
	f := strings.Fields(expression)
	if len(f) != 6 {
		return ""
	}
	sec, min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4], f[5]
	all := func(v string) bool { return v == "*" || v == "?" }
	num := func(v string) (int, bool) {
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	step := func(v string) (int, bool) {
		if !strings.HasPrefix(v, "*/") {
			return 0, false
		}
		return num(strings.TrimPrefix(v, "*/"))
	}
	every := func(n int, unit string) string {
		if n == 1 {
			return "Every " + unit
		}
		return fmt.Sprintf("Every %d %ss", n, unit)
	}

	s, secFixed := num(sec)
	m, minFixed := num(min)
	h, hourFixed := num(hour)
	clock := func() string {
		if s != 0 {
			return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
		}
		return fmt.Sprintf("%02d:%02d", h, m)
	}

	switch {
	case secFixed && minFixed && hourFixed && all(mon):
		if all(dom) && all(dow) {
			return "Every day at " + clock()
		}
		if all(dom) {
			if days := describeWeekdays(dow); days != "" {
				return days + " at " + clock()
			}
			return ""
		}
		if d, ok := num(dom); ok && all(dow) {
			return fmt.Sprintf("Monthly on day %d at %s", d, clock())
		}
	case secFixed && minFixed && all(dom) && all(mon) && all(dow):
		if all(hour) {
			return fmt.Sprintf("Every hour at :%02d", m)
		}
		if n, ok := step(hour); ok {
			return fmt.Sprintf("%s at :%02d", every(n, "hour"), m)
		}
	case secFixed && all(hour) && all(dom) && all(mon) && all(dow):
		if n, ok := step(min); ok {
			return every(n, "minute")
		}
		if all(min) {
			return "Every minute"
		}
	case all(min) && all(hour) && all(dom) && all(mon) && all(dow):
		if n, ok := step(sec); ok {
			return every(n, "second")
		}
	}
	return ""
}

// describeWeekdays reads a day-of-week field — numbers (1 Monday … 7 Sunday, 0 also
// Sunday) or names, as a list or one range — into words: "Sundays", "Mondays and
// Thursdays", "Weekdays". "" for anything else.
func describeWeekdays(field string) string {
	names := map[string]time.Weekday{
		"0": time.Sunday, "1": time.Monday, "2": time.Tuesday, "3": time.Wednesday,
		"4": time.Thursday, "5": time.Friday, "6": time.Saturday, "7": time.Sunday,
		"SUN": time.Sunday, "MON": time.Monday, "TUE": time.Tuesday, "WED": time.Wednesday,
		"THU": time.Thursday, "FRI": time.Friday, "SAT": time.Saturday,
	}
	field = strings.ToUpper(field)
	if field == "1-5" || field == "MON-FRI" {
		return "Weekdays"
	}
	if field == "6,7" || field == "6,0" || field == "SAT,SUN" {
		return "Weekends"
	}
	var days []string
	for _, part := range strings.Split(field, ",") {
		d, ok := names[part]
		if !ok {
			return ""
		}
		days = append(days, d.String()+"s")
	}
	switch len(days) {
	case 1:
		return days[0]
	case 2:
		return days[0] + " and " + days[1]
	}
	return strings.Join(days[:len(days)-1], ", ") + " and " + days[len(days)-1]
}
