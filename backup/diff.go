package backup

import (
	"encoding/hex"
	"fmt"
	"sort"
)

// Change is one difference between two backups. Section is "setting" or
// "eeprom"; Key is the setting path or the offset ("0x0173").
type Change struct{ Section, Key, A, B, Note string }

// Diff compares two backups: changed settings first (in B's file order,
// settings only in A last), then changed EEPROM bytes. Known entries come
// first by offset, then unknown changed bytes as contiguous runs. A half
// missing on either side yields no rows for that half. Without a layout
// (haveLayout false) every changed byte is unknown.
func Diff(a, b *Backup, l Layout, haveLayout bool) []Change {
	var out []Change
	if a.MCu != nil && b.MCu != nil {
		out = diffSettings(a.MCu, b.MCu)
	}
	if a.MCm != nil && b.MCm != nil {
		if !haveLayout {
			l = Layout{}
		}
		out = append(out, diffEEPROM(a.MCm.Image, b.MCm.Image, l)...)
	}
	return out
}

// damagedSections returns the sections holding a problem; all is set when
// a problem concerns the whole file or lies before any setting.
func damagedSections(u *MCu, into map[string]bool) (all bool) {
	for _, p := range u.Problems {
		sec, found := "", false
		for _, s := range u.Settings {
			if s.Line > p.Line {
				break
			}
			sec, found = s.Section, true
		}
		if p.Line == 0 || !found {
			all = true
			continue
		}
		into[sec] = true
	}
	return all
}

func diffSettings(a, b *MCu) []Change {
	damaged := map[string]bool{}
	all := damagedSections(a, damaged)
	all = damagedSections(b, damaged) || all
	note := func(s Setting, n string) string {
		if all || damaged[s.Section] {
			return "damaged"
		}
		return n
	}
	inA := map[string]Setting{}
	for _, s := range a.Settings {
		if !s.Menu {
			inA[s.Path] = s
		}
	}
	var out []Change
	seen := map[string]bool{}
	for _, s := range b.Settings {
		if s.Menu {
			continue
		}
		seen[s.Path] = true
		old, ok := inA[s.Path]
		switch {
		case !ok:
			out = append(out, Change{"setting", s.Path, "", s.Value, note(s, "added")})
		case old.Value != s.Value:
			out = append(out, Change{"setting", s.Path, old.Value, s.Value, note(s, "")})
		}
	}
	for _, s := range a.Settings {
		if !s.Menu && !seen[s.Path] {
			out = append(out, Change{"setting", s.Path, s.Value, "", note(s, "removed")})
		}
	}
	return out
}

func offsetKey(off int) string { return fmt.Sprintf("0x%04X", off) }

func diffEEPROM(a, b []byte, l Layout) []Change {
	var out []Change
	covered := map[int]bool{}
	entries := append([]Entry(nil), l.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Offset < entries[j].Offset })
	for _, e := range entries {
		ra, va, oka := e.Decode(a)
		rb, vb, okb := e.Decode(b)
		if !oka && !okb {
			continue
		}
		for i := 0; i < e.Width(); i++ {
			covered[e.Offset+i] = true
		}
		if oka && okb && ra == rb {
			continue
		}
		c := Change{
			Section: "eeprom", Key: offsetKey(e.Offset),
			Note: fmt.Sprintf("%s (%s)", e.Name, e.Confidence),
		}
		if oka {
			c.A = e.Format(va)
		}
		if okb {
			c.B = e.Format(vb)
		}
		switch {
		case !okb:
			c.Note += "; only in A"
		case !oka:
			c.Note += "; only in B"
		}
		out = append(out, c)
	}

	n := max(len(a), len(b))
	both := min(len(a), len(b))
	changed := func(i int) bool {
		if covered[i] {
			return false
		}
		return i >= both || a[i] != b[i]
	}
	for i := 0; i < n; {
		if !changed(i) {
			i++
			continue
		}
		start := i
		for i < n && changed(i) && (i < both) == (start < both) {
			i++
		}
		c := Change{Section: "eeprom", Key: offsetKey(start)}
		switch {
		case start >= both && len(a) > len(b):
			c.A, c.Note = hex.EncodeToString(a[start:i]), "only in A"
		case start >= both:
			c.B, c.Note = hex.EncodeToString(b[start:i]), "only in B"
		default:
			c.A, c.B = hex.EncodeToString(a[start:i]), hex.EncodeToString(b[start:i])
		}
		out = append(out, c)
	}
	return out
}
