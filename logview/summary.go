package logview

import (
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// EventKinds are the entry kinds in the order levalog lists them.
var EventKinds = []Kind{
	KindReport, KindFrame, KindCmd, KindReply, KindBanner,
	KindNotice, KindNote, KindFragment, KindPartial, KindRedacted,
}

// runKinds are the run kinds in the order summary lists them.
var runKinds = []RunKind{RunShot, RunFlush, RunDraw}

// maxReportPause is the longest pause between two reports that still
// counts towards a mode; longer ones are gaps.
const maxReportPause = 2 * time.Second

// Summary is what one source holds.
type Summary struct {
	Src, Kind string // Kind is "capture" or "rawlog"
	Start     time.Time
	HasStart  bool
	Duration  time.Duration // first to last event
	Firmware  string
	XML       []string // the XML names in effect, in order
	Notes     string
	Events    map[Kind]int
	Modes     map[string]time.Duration // "standby" for marker '?', "?" for a masked mode
	Ready     []time.Duration          // offsets of the [READY] notices
	Runs      map[RunKind]int          // nil without an XML
	RunNums   []int
	Masked    []string // XML keys masked in every non-standby report
	GapMax    time.Duration
	Gaps      int // pauses over 2 s between reports
	// Decoded is false when the source has reports but none had an XML;
	// Modes, Runs and Masked are then empty.
	Decoded bool
}

// SummaryScanner builds a Summary per source from a Stream: call Source
// before the entries of each source and Entry for each entry, then
// Summaries once.
//
// With Since or Until set, only entries whose wall time lies in
// [Since, Until) count (entries without wall time are dropped), a source
// without such an entry gets no summary, and Start and Duration are those
// of its first and last counted entry.
type SummaryScanner struct {
	Since, Until time.Time // zero: no bound

	done []sourceSum
	cur  *sourceSum
	keys map[*leva.RecordSpec][]string // XML keys of a spec, by position
}

// sourceSum is the state of one source.
type sourceSum struct {
	sum     Summary
	index   int // SourceInfo.Index
	reports bool
	decoded bool
	last    time.Duration // offset of the previous report
	hasLast bool
	mode    string        // mode of the previous report; "" counts nowhere
	entries int           // entries counted
	first   time.Duration // offset of the first counted entry

	nonStandby int
	masked     map[string]int // per key: non-standby reports where it is masked
	order      []string       // keys in the order first seen
}

// NewSummaryScanner returns an empty SummaryScanner.
func NewSummaryScanner() *SummaryScanner {
	return &SummaryScanner{keys: map[*leva.RecordSpec][]string{}}
}

// Source starts the summary of si.
func (s *SummaryScanner) Source(si SourceInfo) {
	s.finish()
	sum := Summary{
		Src: si.Path, Kind: "capture", Firmware: si.Firmware, Notes: si.Notes,
		Duration: si.Last - si.First, Events: map[Kind]int{}, Modes: map[string]time.Duration{},
	}
	switch {
	case si.HasWall:
		sum.Kind, sum.Start, sum.HasStart = "rawlog", si.Start, true
	case !si.Recorded.IsZero():
		// levacap takes recorded when it writes the capture, just after
		// its last event; t 0 is that much earlier (as Run.Start).
		sum.Start, sum.HasStart = si.Recorded.Add(-si.Last), true
	}
	s.cur = &sourceSum{sum: sum, index: si.Index, masked: map[string]int{}}
}

// Entry counts e in the summary of the current source.
func (s *SummaryScanner) Entry(e *Entry) {
	c := s.cur
	if c == nil || !s.inWindow(e) {
		return
	}
	sum := &c.sum
	c.entries++
	if s.windowed() {
		if c.entries == 1 {
			c.first = e.Offset
			sum.Start, sum.HasStart = e.Wall, true
		}
		sum.Duration = e.Offset - c.first
	}
	sum.Events[e.Kind]++
	if e.XML != "" && (len(sum.XML) == 0 || sum.XML[len(sum.XML)-1] != e.XML) {
		sum.XML = append(sum.XML, e.XML)
	}
	switch e.Kind {
	case KindBanner:
		if sum.Firmware == "" {
			sum.Firmware = latin1(e.Raw)
		}
	case KindNotice:
		if string(e.Raw) == "[READY]" {
			sum.Ready = append(sum.Ready, e.Offset)
		}
	case KindReport:
		var keys []string
		if e.Spec != nil {
			if keys = s.keys[e.Spec]; keys == nil {
				keys = sortedKeys(keyPositions(e.Spec))
				s.keys[e.Spec] = keys
			}
		}
		c.report(e, keys)
	}
}

// report counts the time since the previous report and notes e's mode and
// masked fields; keys are the XML keys of e's spec, by position.
func (c *sourceSum) report(e *Entry, keys []string) {
	sum := &c.sum
	if c.hasLast {
		d := e.Offset - c.last
		sum.GapMax = max(sum.GapMax, d)
		switch {
		case d > maxReportPause:
			sum.Gaps++
		case c.mode != "":
			sum.Modes[c.mode] += d
		}
	}
	c.last, c.hasLast, c.reports = e.Offset, true, true
	c.mode = ""
	if e.Spec == nil {
		return
	}
	c.decoded = true
	if pumpByte(e) == '?' {
		c.mode = "standby"
		return
	}
	c.mode = "?"
	if v, ok := FieldOf(e, "mode"); ok && v.Str != nil && strings.TrimSpace(*v.Str) != "" {
		c.mode = strings.TrimSpace(*v.Str)
	}
	if e.Record == nil {
		return
	}
	c.nonStandby++
	pos := keyPositions(e.Spec)
	for _, k := range keys {
		if !slices.Contains(c.order, k) {
			c.order = append(c.order, k)
		}
		if p := pos[k]; p < len(e.Record.Fields) && ParseField(e.Record.Fields[p]).Masked {
			c.masked[k]++
		}
	}
}

// sortedKeys are the keys of pos by position.
func sortedKeys(pos map[string]int) []string {
	keys := make([]string, 0, len(pos))
	for k := range pos {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return pos[a] - pos[b] })
	return keys
}

func (s *SummaryScanner) windowed() bool { return !s.Since.IsZero() || !s.Until.IsZero() }

// inWindow reports whether e counts: always without a window, else when
// its wall time lies in [Since, Until).
func (s *SummaryScanner) inWindow(e *Entry) bool {
	if !s.windowed() {
		return true
	}
	return e.HasWall && (s.Since.IsZero() || !e.Wall.Before(s.Since)) && (s.Until.IsZero() || e.Wall.Before(s.Until))
}

func (s *SummaryScanner) finish() {
	if s.cur != nil {
		s.done = append(s.done, *s.cur)
		s.cur = nil
	}
}

// Summaries returns the summaries in source order (with a window, only of
// the sources that had an entry in it), with each run counted
// in the source of its first sample.
func (s *SummaryScanner) Summaries(runs []Run) []Summary {
	s.finish()
	out := make([]Summary, 0, len(s.done))
	for _, c := range s.done {
		if s.windowed() && c.entries == 0 {
			continue
		}
		sum := c.sum
		sum.Decoded = c.decoded || !c.reports
		if !sum.Decoded {
			sum.Modes = map[string]time.Duration{}
			out = append(out, sum)
			continue
		}
		sum.Runs = map[RunKind]int{}
		for _, k := range runKinds {
			sum.Runs[k] = 0
		}
		for _, r := range runs {
			if r.First.Source == c.index {
				sum.Runs[r.Kind]++
				sum.RunNums = append(sum.RunNums, r.N)
			}
		}
		if c.nonStandby > 0 {
			for _, k := range c.order {
				if c.masked[k] == c.nonStandby {
					sum.Masked = append(sum.Masked, k)
				}
			}
		}
		out = append(out, sum)
	}
	return out
}

// summaryHeader are the keys of a summary, in output order.
var summaryHeader = []string{
	"src", "kind", "start", "duration", "firmware", "xml", "notes", "events",
	"modes", "ready", "runs", "masked", "gap_max", "gaps",
}

// WriteSummaries prints ss: a block of "key: value" lines per summary with
// a blank line between (text), one object per summary (jsonl) or one row
// per summary after a header (csv). start is local time in loc (text) or
// UTC; durations are seconds.
func WriteSummaries(w io.Writer, f Format, ss []Summary, loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	switch f {
	case FormatJSONL:
		for _, s := range ss {
			if _, err := w.Write(summaryJSON(s)); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write(summaryHeader)
		for _, s := range ss {
			start := ""
			if s.HasStart {
				start = utcWall(s.Start)
			}
			_ = cw.Write(summaryCells(s, start))
		}
		cw.Flush()
		return cw.Error()
	}
	for i, s := range ss {
		start := ""
		if s.HasStart {
			start = s.Start.In(loc).Format(wallLayout)
		}
		s.Notes = strings.ReplaceAll(s.Notes, "\n", " ")
		var b strings.Builder
		if i > 0 {
			b.WriteByte('\n')
		}
		for k, v := range summaryCells(s, start) {
			b.WriteString(strings.TrimRight(summaryHeader[k]+": "+v, " "))
			b.WriteByte('\n')
		}
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	return nil
}

// summaryCells are the values of s as text, in summaryHeader order.
func summaryCells(s Summary, start string) []string {
	return []string{
		s.Src, s.Kind, start, seconds(s.Duration), s.Firmware, strings.Join(s.XML, " "), s.Notes,
		pairs(eventPairs(s.Events), "="), pairs(modePairs(s.Modes), "="), readyText(s.Ready),
		runsText(s), strings.Join(s.Masked, " "), seconds(s.GapMax), strconv.Itoa(s.Gaps),
	}
}

type pair struct{ k, v string }

func pairs(ps []pair, sep string) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.k + sep + p.v
	}
	return strings.Join(out, " ")
}

// eventPairs are the non-zero event counts in EventKinds order.
func eventPairs(m map[Kind]int) []pair {
	var out []pair
	for _, k := range EventKinds {
		if n := m[k]; n > 0 {
			out = append(out, pair{string(k), strconv.Itoa(n)})
		}
	}
	return out
}

// modePairs are the seconds per mode, by mode name.
func modePairs(m map[string]time.Duration) []pair {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]pair, len(keys))
	for i, k := range keys {
		out[i] = pair{k, seconds(m[k])}
	}
	return out
}

// runPairs are the run counts in runKinds order; none without Runs.
func runPairs(m map[RunKind]int) []pair {
	if m == nil {
		return nil
	}
	out := make([]pair, len(runKinds))
	for i, k := range runKinds {
		out[i] = pair{string(k), strconv.Itoa(m[k])}
	}
	return out
}

func readyText(ts []time.Duration) string {
	if len(ts) == 0 {
		return "0"
	}
	return fmt.Sprintf("%d (%s)", len(ts), strings.Join(secondsList(ts), " "))
}

func runsText(s Summary) string {
	t := pairs(runPairs(s.Runs), "=")
	if r := runRanges(s.RunNums); r != "" {
		t += " (" + r + ")"
	}
	return t
}

func secondsList(ts []time.Duration) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = seconds(t)
	}
	return out
}

// runRanges prints ascending run numbers as N,M and N-M ranges, the form
// events --run takes.
func runRanges(nums []int) string {
	var parts []string
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		p := strconv.Itoa(nums[i])
		if j > i {
			p += "-" + strconv.Itoa(nums[j])
		}
		parts = append(parts, p)
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// jsonPairs prints ps as an object; raw values are numbers.
func jsonPairs(ps []pair, extra ...string) string {
	items := make([]string, 0, len(ps)+len(extra))
	for _, p := range ps {
		items = append(items, jsonString(p.k)+":"+p.v)
	}
	items = append(items, extra...)
	return "{" + strings.Join(items, ",") + "}"
}

// jsonList prints items, already JSON values, as an array.
func jsonList(items []string) string { return "[" + strings.Join(items, ",") + "]" }

func summaryJSON(s Summary) []byte {
	var o jsonObj
	o.str("src", s.Src)
	o.str("kind", s.Kind)
	start := ""
	if s.HasStart {
		start = utcWall(s.Start)
	}
	o.strOrNull("start", start)
	o.raw("duration", seconds(s.Duration))
	o.strOrNull("firmware", s.Firmware)
	xml := make([]string, len(s.XML))
	for i, x := range s.XML {
		xml[i] = jsonString(x)
	}
	o.raw("xml", jsonList(xml))
	o.strOrNull("notes", s.Notes)
	o.raw("events", jsonPairs(eventPairs(s.Events)))
	o.raw("modes", jsonPairs(modePairs(s.Modes)))
	o.raw("ready", jsonList(secondsList(s.Ready)))
	var numbers []string
	if s.Runs != nil {
		nums := make([]string, len(s.RunNums))
		for i, n := range s.RunNums {
			nums[i] = strconv.Itoa(n)
		}
		numbers = []string{`"numbers":` + jsonList(nums)}
	}
	o.raw("runs", jsonPairs(runPairs(s.Runs), numbers...))
	masked := make([]string, len(s.Masked))
	for i, k := range s.Masked {
		masked[i] = jsonString(k)
	}
	o.raw("masked", jsonList(masked))
	o.raw("gap_max", seconds(s.GapMax))
	o.raw("gaps", strconv.Itoa(s.Gaps))
	return o.line()
}
