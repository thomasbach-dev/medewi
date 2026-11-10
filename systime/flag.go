package systime

import "time"

// Span is a pflag.Value holding a time.Duration parsed with ParseSpan.
type Span time.Duration

// NewSpan returns a Span flag value that stores into *p.
func NewSpan(p *time.Duration) *Span { return (*Span)(p) }

// Set parses v with ParseSpan.
func (s *Span) Set(v string) error {
	d, err := ParseSpan(v)
	if err != nil {
		return err
	}
	*s = Span(d)
	return nil
}

// String formats the span with FormatSpan.
func (s *Span) String() string { return FormatSpan(time.Duration(*s)) }

// Type is the type name pflag shows in usage.
func (s *Span) Type() string { return "span" }

// Timestamp is a pflag.Value holding a time parsed with ParseTimestamp. Now
// and Loc default to time.Now and time.Local; tests set them.
type Timestamp struct {
	T   time.Time
	Now func() time.Time
	Loc *time.Location
}

// Set parses v with ParseTimestamp.
func (t *Timestamp) Set(v string) error {
	now, loc := time.Now, t.Loc
	if t.Now != nil {
		now = t.Now
	}
	if loc == nil {
		loc = time.Local
	}
	parsed, err := ParseTimestamp(v, now(), loc)
	if err != nil {
		return err
	}
	t.T = parsed
	return nil
}

// String is the RFC 3339 time, or empty when unset.
func (t *Timestamp) String() string {
	if t == nil || t.T.IsZero() {
		return ""
	}
	return t.T.Format(time.RFC3339)
}

// Type is the type name pflag shows in usage.
func (t *Timestamp) Type() string { return "timestamp" }
