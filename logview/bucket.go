package logview

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// BucketCol is one aggregated column of a bucket row. Plain names the
// column Key instead of <Key>_<Agg.Name> (the default last).
type BucketCol struct {
	Key   string
	Agg   Agg
	Plain bool
}

// Name is the column name.
func (c BucketCol) Name() string {
	if c.Plain {
		return c.Key
	}
	return c.Key + "_" + c.Agg.Name
}

// BucketRow is one bucket: the reports of one span of time, aggregated.
type BucketRow struct {
	// Source and XML are those of the bucket's first report; with run
	// selection, those of the run's first sample.
	Source SourceInfo
	XML    string
	Run    *Run // with run selection, else nil
	// Start is the bucket start on the source's base (its t), Wall the
	// same on the wall clock (raw logs only).
	Start   time.Duration
	Wall    time.Time
	HasWall bool
	RunT    *time.Duration // the bucket start in run time; nil without Run
	Reports int            // non-standby reports in the bucket
	Values  []AggValue     // per column
}

// ReportKeys are the report keys events shows for specs: pump, then the
// XML keys of all specs, each once in first-seen order, with mode first.
func ReportKeys(specs ...*leva.RecordSpec) ([]string, error) {
	keys := []string{"pump"}
	for _, spec := range specs {
		cols, err := Columns(spec)
		if err != nil {
			return nil, err
		}
		for _, c := range cols[len(MedewiColumns):] {
			if !slices.Contains(keys, c.Key) {
				keys = append(keys, c.Key)
			}
		}
	}
	if i := slices.Index(keys, "mode"); i > 1 {
		keys = slices.Insert(slices.Delete(keys, i, i+1), 1, "mode")
	}
	return keys, nil
}

// bucket is an open bucket.
type bucket struct {
	run     *Run
	idx     int64 // start / every
	src     SourceInfo
	xml     string
	reports int
	accs    []Acc // per key
}

// runBuckets are the open buckets of one run, and the last entry seen in
// it.
type runBuckets struct {
	run     *Run
	last    int // Entry.Index
	buckets []*bucket
}

// Bucketer groups the non-standby reports of rows into buckets of a fixed
// span. With byRun, each run is bucketed from run_t 0 and its buckets come
// out in run order once a later row shows the run is over; else each
// source is bucketed on its own, raw logs on multiples of every since the
// Unix epoch, captures from t 0. Buckets without a report are omitted.
type Bucketer struct {
	every   time.Duration
	cols    []BucketCol
	keys    []string // distinct column keys
	keyAggs [][]Agg  // per key, the aggregates of its columns
	colKey  []int    // per column, index in keys
	byRun   bool
	sources map[int]SourceInfo
	cur     *bucket       // without byRun
	runs    []*runBuckets // with byRun: open runs, in run order
	missing map[string]int
	lastIdx int // Entry.Index of the last report counted in missing
}

// NewBucketer returns a Bucketer for buckets of every with cols.
func NewBucketer(every time.Duration, cols []BucketCol, byRun bool) *Bucketer {
	b := &Bucketer{every: every, cols: cols, byRun: byRun, sources: map[int]SourceInfo{}, missing: map[string]int{}, lastIdx: -1}
	for _, c := range cols {
		i := slices.Index(b.keys, c.Key)
		if i < 0 {
			i = len(b.keys)
			b.keys = append(b.keys, c.Key)
			b.keyAggs = append(b.keyAggs, nil)
		}
		b.keyAggs[i] = append(b.keyAggs[i], c.Agg)
		b.colKey = append(b.colKey, i)
	}
	return b
}

// Source announces the source whose rows follow; without byRun it closes
// the open bucket.
func (b *Bucketer) Source(si SourceInfo) []BucketRow {
	b.sources[si.Index] = si
	if b.byRun {
		return nil
	}
	return b.closeCur()
}

// Add counts the report of r and returns the buckets r closed. Entries
// that are not reports, and standby reports, are ignored.
func (b *Bucketer) Add(r Row) []BucketRow {
	e := r.Entry
	if e.Kind != KindReport || pumpByte(e) == '?' {
		return nil
	}
	var out []BucketRow
	var bk *bucket
	at := e.Offset
	if b.byRun {
		if r.Run == nil || r.RunT == nil {
			return nil
		}
		at = *r.RunT
		out = b.closeRuns(r.Run.N, e)
		bk = b.runBucket(r.Run, e, floorDiv(int64(at), int64(b.every)))
	} else {
		t := int64(e.Offset)
		if e.HasWall {
			t = e.Wall.UnixNano()
		}
		idx := floorDiv(t, int64(b.every))
		if b.cur != nil && (b.cur.idx != idx || b.cur.src.Index != e.Source) {
			out = b.closeCur()
		}
		if b.cur == nil {
			b.cur = b.newBucket(nil, idx, e)
		}
		bk = b.cur
	}
	bk.reports++
	src := srcRef(e.Path, e.Src)
	// A report in two overlapping run windows comes twice: count it once.
	first := e.Index != b.lastIdx
	b.lastIdx = e.Index
	for i, k := range b.keys {
		v, ok := FieldOf(e, k)
		if !ok {
			if first {
				b.missing[k]++
			}
			continue
		}
		bk.accs[i].Add(v, at, src)
	}
	return out
}

// Close returns the buckets still open.
func (b *Bucketer) Close() []BucketRow {
	out := b.closeCur()
	for _, rb := range b.runs {
		out = append(out, b.emitRun(rb)...)
	}
	b.runs = nil
	return out
}

// Missing lists the keys that some reports lacked (no XML for the report
// or its XML lacks the key), one line per key with a count.
func (b *Bucketer) Missing() []string {
	out := make([]string, 0, len(b.missing))
	for k, n := range b.missing {
		out = append(out, fmt.Sprintf("key %s: %d reports whose XML lacks it", k, n))
	}
	sort.Strings(out)
	return out
}

func (b *Bucketer) newBucket(run *Run, idx int64, e *Entry) *bucket {
	accs := make([]Acc, len(b.keys))
	for i := range accs {
		accs[i] = NewAcc(b.keyAggs[i])
	}
	bk := &bucket{run: run, idx: idx, src: b.sources[e.Source], xml: e.XML, accs: accs}
	if run != nil {
		bk.src, bk.xml = b.sources[run.First.Source], run.XML
	}
	return bk
}

func (b *Bucketer) closeCur() []BucketRow {
	if b.cur == nil {
		return nil
	}
	r := b.row(b.cur)
	b.cur = nil
	return []BucketRow{r}
}

// closeRuns emits the open runs before run n that are over by entry e.
// On e's timeline, a run is over when it did not hold e: SelectAll gives
// an entry's rows in run order, so such a run got no row for e and,
// windows being ordered, gets none later. A run on another timeline is
// over only when that is a capture's, a source the input has left; a run
// on the wall clock may continue in a later raw log. The closed runs are a
// prefix of the open ones, so buckets come out in run order.
func (b *Bucketer) closeRuns(n int, e *Entry) []BucketRow {
	var out []BucketRow
	etl := timeline(e.HasWall, e.Source)
	k := 0
	for ; k < len(b.runs) && b.runs[k].run.N < n; k++ {
		rb := b.runs[k]
		tl := timeline(rb.run.First.HasWall, rb.run.First.Source)
		if tl == etl && rb.last >= e.Index || tl != etl && tl == timeline(true, 0) {
			break
		}
		out = append(out, b.emitRun(rb)...)
	}
	b.runs = b.runs[k:]
	return out
}

func (b *Bucketer) runBucket(run *Run, e *Entry, idx int64) *bucket {
	var rb *runBuckets
	for _, x := range b.runs {
		if x.run == run {
			rb = x
			break
		}
	}
	if rb == nil {
		rb = &runBuckets{run: run}
		b.runs = append(b.runs, rb)
		sort.SliceStable(b.runs, func(i, j int) bool { return b.runs[i].run.N < b.runs[j].run.N })
	}
	rb.last = e.Index
	for i := len(rb.buckets) - 1; i >= 0; i-- {
		if rb.buckets[i].idx == idx {
			return rb.buckets[i]
		}
	}
	bk := b.newBucket(run, idx, e)
	rb.buckets = append(rb.buckets, bk)
	return bk
}

func (b *Bucketer) emitRun(rb *runBuckets) []BucketRow {
	sort.SliceStable(rb.buckets, func(i, j int) bool { return rb.buckets[i].idx < rb.buckets[j].idx })
	out := make([]BucketRow, len(rb.buckets))
	for i, bk := range rb.buckets {
		out[i] = b.row(bk)
	}
	return out
}

func (b *Bucketer) row(bk *bucket) BucketRow {
	start := time.Duration(bk.idx) * b.every
	r := BucketRow{Source: bk.src, XML: bk.xml, Run: bk.run, Reports: bk.reports, Values: make([]AggValue, len(b.cols))}
	switch {
	case bk.run != nil:
		rt := start
		r.RunT = &rt
		if f := bk.run.First; f.HasWall {
			r.Wall, r.HasWall = f.Wall.Add(rt), true
			r.Start = r.Wall.Sub(bk.src.Start)
		} else {
			r.Start = f.Offset + rt
		}
	case bk.src.HasWall:
		r.Wall, r.HasWall = time.Unix(0, int64(start)).UTC(), true
		r.Start = r.Wall.Sub(bk.src.Start)
	default:
		r.Start = start
	}
	for i, c := range b.cols {
		r.Values[i] = bk.accs[b.colKey[i]].Result(c.Agg)
	}
	return r
}

// floorDiv is a/b rounded towards minus infinity, for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}
