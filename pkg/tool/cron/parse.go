// Package cron ports Claude Code's cron family (utils/cron.ts,
// utils/cronTasks.ts, utils/cronTasksLock.ts, utils/cronScheduler.ts and the
// ScheduleCronTool trio) into gbot as one Cron tool with create/list/delete
// actions plus a per-engine scheduler Manager.
package cron

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CronFields holds the expanded value sets of a parsed 5-field cron
// expression. Source: utils/cron.ts CronFields.
type CronFields struct {
	Minute     []int
	Hour       []int
	DayOfMonth []int
	Month      []int
	DayOfWeek  []int
}

type fieldRange struct{ min, max int }

// Source: utils/cron.ts FIELD_RANGES. dayOfWeek accepts 7 as a Sunday alias.
var fieldRanges = [5]fieldRange{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // dayOfMonth
	{1, 12}, // month
	{0, 6},  // dayOfWeek (0=Sunday)
}

var (
	starStepRe  = regexp.MustCompile(`^\*(?:/(\d+))?$`)
	rangeStepRe = regexp.MustCompile(`^(\d+)-(\d+)(?:/(\d+))?$`)
	digitsRe    = regexp.MustCompile(`^\d+$`)
)

// expandField parses a single cron field into a sorted, deduped value slice.
// Source: utils/cron.ts expandField — wildcard, N, star-slash-N (step),
// N-M (range, dow 7 alias), comma lists. Returns nil if invalid.
func expandField(field string, r fieldRange) []int {
	out := map[int]bool{}
	for part := range strings.SplitSeq(field, ",") {
		if m := starStepRe.FindStringSubmatch(part); m != nil {
			step := 1
			if m[1] != "" {
				step = atoiOr(m[1], -1)
			}
			if step < 1 {
				return nil
			}
			for i := r.min; i <= r.max; i += step {
				out[i] = true
			}
			continue
		}
		if m := rangeStepRe.FindStringSubmatch(part); m != nil {
			lo := atoiOr(m[1], -1)
			hi := atoiOr(m[2], -1)
			step := 1
			if m[3] != "" {
				step = atoiOr(m[3], -1)
			}
			// dayOfWeek: accept 7 as Sunday alias in ranges (5-7 → Fri,Sat,Sun).
			isDow := r.min == 0 && r.max == 6
			effMax := r.max
			if isDow {
				effMax = 7
			}
			if lo > hi || step < 1 || lo < r.min || hi > effMax {
				return nil
			}
			for i := lo; i <= hi; i += step {
				if isDow && i == 7 {
					out[0] = true
				} else {
					out[i] = true
				}
			}
			continue
		}
		if digitsRe.MatchString(part) {
			n := atoiOr(part, -1)
			// dayOfWeek: accept 7 as Sunday alias → 0.
			if r.min == 0 && r.max == 6 && n == 7 {
				n = 0
			}
			if n < r.min || n > r.max {
				return nil
			}
			out[n] = true
			continue
		}
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	values := make([]int, 0, len(out))
	for v := range out {
		values = append(values, v)
	}
	sort.Ints(values)
	return values
}

// atoiOr parses decimal digits; the regex guarantees well-formed input, so the
// fallback only fires on numbers beyond int range (parseInt's float rounding
// there also lands out of every field's range — same invalid outcome).
func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// ParseCronExpression parses a 5-field cron expression into expanded value
// sets. Returns nil if invalid or unsupported syntax.
// Source: utils/cron.ts parseCronExpression.
func ParseCronExpression(expr string) *CronFields {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil
	}
	var expanded [5][]int
	for i := range 5 {
		result := expandField(parts[i], fieldRanges[i])
		if result == nil {
			return nil
		}
		expanded[i] = result
	}
	return &CronFields{
		Minute:     expanded[0],
		Hour:       expanded[1],
		DayOfMonth: expanded[2],
		Month:      expanded[3],
		DayOfWeek:  expanded[4],
	}
}

// ComputeNextCronRun computes the next time strictly after `from` matching the
// cron fields, in the process's local timezone, walking forward
// minute-by-minute with month/day/hour jumps. Bounded at 366 days; returns
// nil if no match. Standard cron dom/dow OR semantics apply when both fields
// are constrained. Source: utils/cron.ts computeNextCronRun.
func ComputeNextCronRun(f *CronFields, from time.Time) *time.Time {
	minuteSet := setOf(f.Minute)
	hourSet := setOf(f.Hour)
	domSet := setOf(f.DayOfMonth)
	monthSet := setOf(f.Month)
	dowSet := setOf(f.DayOfWeek)

	domWild := len(f.DayOfMonth) == 31
	dowWild := len(f.DayOfWeek) == 7

	// Round up to the next whole minute (strictly after `from`).
	t := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute()+1, 0, 0, time.Local)

	const maxIter = 366 * 24 * 60
	for range maxIter {
		if !monthSet[int(t.Month())] {
			// Jump to start of next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.Local)
			continue
		}
		dom := t.Day()
		dow := int(t.Weekday())
		var dayMatches bool
		switch {
		case domWild && dowWild:
			dayMatches = true
		case domWild:
			dayMatches = dowSet[dow]
		case dowWild:
			dayMatches = domSet[dom]
		default:
			dayMatches = domSet[dom] || dowSet[dow]
		}
		if !dayMatches {
			// Jump to start of next day.
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.Local)
			continue
		}
		if !hourSet[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.Local)
			continue
		}
		if !minuteSet[t.Minute()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, time.Local)
			continue
		}
		next := t
		return &next
	}
	return nil
}

func setOf(values []int) map[int]bool {
	s := make(map[int]bool, len(values))
	for _, v := range values {
		s[v] = true
	}
	return s
}

// NextCronRunMs is the epoch-ms wrapper: next fire time strictly after fromMs,
// false if invalid or no match within 366 days.
// Source: utils/cron.ts (cronTasks.ts) nextCronRunMs.
func NextCronRunMs(cron string, fromMs int64) (int64, bool) {
	fields := ParseCronExpression(cron)
	if fields == nil {
		return 0, false
	}
	next := ComputeNextCronRun(fields, time.UnixMilli(fromMs))
	if next == nil {
		return 0, false
	}
	return next.UnixMilli(), true
}

var dayNames = [7]string{
	"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
}

// monthNames feeds the pinned-date arm below (abbreviated, matching the
// "Nov 12" output form). No CC counterpart — CC has no pinned-date arm.
var monthNames = [12]string{
	"Jan", "Feb", "Mar", "Apr", "May", "Jun",
	"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
}

// formatLocalTime formats h:m on January 1, 2000 in the local timezone. The
// fixed reference date avoids the one DST-gap day per year (source comment:
// "Using new Date() (today) would roll 2am→3am"). Source: utils/cron.ts
// formatLocalTime — the `utc` variant is CCR-remote-only and not ported.
// 24h layout, not CC's "3:04 PM" (user ruling: 24h).
func formatLocalTime(minute, hour int) string {
	return time.Date(2000, time.January, 1, hour, minute, 0, 0, time.Local).Format("15:04")
}

// CronToHuman renders common cron patterns as English; anything else falls
// through to the raw cron string. Source: utils/cron.ts cronToHuman (utc
// option dropped — gbot schedules in local time only).
func CronToHuman(cron string) string {
	parts := strings.Fields(cron)
	if len(parts) != 5 {
		return cron
	}
	minute, hour, dayOfMonth, month, dayOfWeek := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Every N minutes: step/N * * * *
	if m := regexp.MustCompile(`^\*/(\d+)$`).FindStringSubmatch(minute); m != nil &&
		hour == "*" && dayOfMonth == "*" && month == "*" && dayOfWeek == "*" {
		n := atoiOr(m[1], 0)
		if n == 1 {
			return "Every minute"
		}
		return "Every " + strconv.Itoa(n) + " minutes"
	}

	// Every hour: 0 * * * *
	if digitsRe.MatchString(minute) &&
		hour == "*" && dayOfMonth == "*" && month == "*" && dayOfWeek == "*" {
		m := atoiOr(minute, 0)
		if m == 0 {
			return "Every hour"
		}
		return "Every hour at :" + pad2(m)
	}

	// Every N hours: 0 step/N * * *
	if m := regexp.MustCompile(`^\*/(\d+)$`).FindStringSubmatch(hour); m != nil &&
		digitsRe.MatchString(minute) &&
		dayOfMonth == "*" && month == "*" && dayOfWeek == "*" {
		n := atoiOr(m[1], 0)
		mi := atoiOr(minute, 0)
		suffix := ""
		if mi != 0 {
			suffix = " at :" + pad2(mi)
		}
		if n == 1 {
			return "Every hour" + suffix
		}
		return "Every " + strconv.Itoa(n) + " hours" + suffix
	}

	if !digitsRe.MatchString(minute) || !digitsRe.MatchString(hour) {
		return cron
	}
	m := atoiOr(minute, 0)
	h := atoiOr(hour, 0)

	// Daily at specific time: M H * * *
	if dayOfMonth == "*" && month == "*" && dayOfWeek == "*" {
		return "Every day at " + formatLocalTime(m, h)
	}

	// Specific day of week: M H * * D (7 = Sunday alias)
	if dayOfMonth == "*" && month == "*" && digitsRe.MatchString(dayOfWeek) {
		dayIndex := atoiOr(dayOfWeek, 0) % 7
		if dayIndex >= 0 && dayIndex <= 6 {
			return "Every " + dayNames[dayIndex] + " at " + formatLocalTime(m, h)
		}
	}

	// Weekdays: M H * * 1-5
	if dayOfMonth == "*" && month == "*" && dayOfWeek == "1-5" {
		return "Weekdays at " + formatLocalTime(m, h)
	}

	// Pinned date: M H D Mon * — the one-shot reminder shape. CC's cronToHuman
	// has no such arm (fully pinned expressions fall through to the raw
	// string); gbot adds it deliberately so one-shot reminders read as a date
	// instead of raw cron.
	if digitsRe.MatchString(dayOfMonth) && digitsRe.MatchString(month) && dayOfWeek == "*" {
		if mo := atoiOr(month, 0); mo >= 1 && mo <= 12 {
			return formatLocalTime(m, h) + ", " + monthNames[mo-1] + " " + strconv.Itoa(atoiOr(dayOfMonth, 0))
		}
	}

	return cron
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
