// Package cron parses standard 5-field cron expressions and computes their
// occurrences in an IANA time zone.
//
// # Syntax
//
// Fields are minute (0-59), hour (0-23), day-of-month (1-31), month (1-12 or
// JAN-DEC) and day-of-week (0-7 or SUN-SAT, 0 and 7 are Sunday). Each field is
// a comma-separated list of items; an item is `*`, a value `a`, a range
// `a-b`, or one of those with a step: `*/n`, `a-b/n`, `a/n` (a to the field
// maximum, step n). Names are case-insensitive. Seconds are not supported and
// every occurrence has zero seconds.
//
// Day matching follows Vixie cron: when both day-of-month and day-of-week are
// restricted (neither is exactly `*`) a day matches if EITHER matches;
// otherwise both must match. `0 0 13 * 5` therefore fires on every 13th and
// on every Friday.
//
// Parse refuses malformed expressions with a *ParseError naming the field,
// and expressions that can never fire (such as `0 0 30 2 *`) with ErrNever.
//
// # Time zones and daylight saving time
//
// Occurrences are local wall-clock times in the schedule's location, walked
// day by day and then over the matching hours and minutes in ascending order:
//
//   - A local time that does not exist because clocks jump forward (the
//     spring-forward gap, e.g. 03:30 on 2026-03-29 in Europe/Sofia) is
//     SKIPPED; it is not moved to the end of the gap.
//   - A local time that occurs twice because clocks jump back (e.g. 03:30 on
//     2026-10-25 in Europe/Sofia) runs ONCE, at its earlier instant (the
//     first pass, still on daylight time). Every occurrence must be strictly
//     after the reference instant, so the second pass of the repeated hour is
//     never generated; a schedule evaluated from inside the second pass
//     continues with the first wall-clock time after the repeated hour.
//
// Next searches at most five years ahead; a schedule with no occurrence in
// that window (only possible for 29 February around a non-leap century)
// reports none.
package cron
