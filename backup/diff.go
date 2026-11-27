package backup

import (
	"encoding/hex"
	"fmt"
	"sort"
)

// Change is one difference between two backups. Section is "setting" or
// "eeprom"; Key is the setting path or the offset ("0x0173").
type Change struct{ Section, Key, A, B, Note string }

// Diff compares two backups: first one row per section (depth-0 label)
// that has settings on one side only (Key "<label>/", A or B "<n>
// settings", Note "section only in A" or "only in B", with " (damaged)"
// appended when either side has problems; A's sections first, then B's,
// each in file order; depth-0 leaves such as Cancel are not sections) in place of that section's path rows; then
// changed settings (in B's file order, settings only in A last), then
// changed EEPROM bytes. Known entries come
// first by offset, then unknown changed bytes as contiguous runs. The
// layout's state block is compared by meaning: when it sits at different
// places in A and B, one row says so (Key and B: its place in B, A: its
// place in A) and comes first; the block's rows carry B's offsets and
// name A's in the note. A half
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
	// Lost lines can sit anywhere after a glue, so with any damage a path
	// on one side only is "damaged", never added or removed.
	anyDamage := len(a.Problems) > 0 || len(b.Problems) > 0
	oneSided := func(side, n string) string {
		if anyDamage {
			return "damaged; only in " + side
		}
		return n
	}
	secNote := func(side string) string {
		if anyDamage {
			return "section only in " + side + " (damaged)"
		}
		return "section only in " + side
	}
	countA, orderA := sectionCounts(a)
	countB, orderB := sectionCounts(b)
	var out []Change
	for _, sec := range orderA {
		if countB[sec] == 0 {
			out = append(out, Change{"setting", sec + "/", fmt.Sprintf("%d settings", countA[sec]), "", secNote("A")})
		}
	}
	for _, sec := range orderB {
		if countA[sec] == 0 {
			out = append(out, Change{"setting", sec + "/", "", fmt.Sprintf("%d settings", countB[sec]), secNote("B")})
		}
	}
	// Depth-0 leaves are no sections; they diff as plain paths.
	oneSection := func(s Setting) bool {
		return (s.Depth != 0 || s.Menu) && (countA[s.Section] == 0 || countB[s.Section] == 0)
	}
	inA := map[string]Setting{}
	for _, s := range a.Settings {
		if !s.Menu {
			inA[s.Path] = s
		}
	}
	seen := map[string]bool{}
	for _, s := range b.Settings {
		if s.Menu || oneSection(s) {
			continue
		}
		seen[s.Path] = true
		old, ok := inA[s.Path]
		switch {
		case !ok:
			out = append(out, Change{"setting", s.Path, "", s.Value, oneSided("B", "added")})
		case old.Value != s.Value:
			out = append(out, Change{"setting", s.Path, old.Value, s.Value, note(s, "")})
		}
	}
	for _, s := range a.Settings {
		if !s.Menu && !seen[s.Path] && !oneSection(s) {
			out = append(out, Change{"setting", s.Path, s.Value, "", oneSided("A", "removed")})
		}
	}
	return out
}

// sectionCounts returns the number of non-menu settings below a depth-0
// menu per section and the sections that have any, in file order.
func sectionCounts(u *MCu) (count map[string]int, order []string) {
	count = map[string]int{}
	for _, s := range u.Settings {
		if s.Menu || s.Depth == 0 {
			continue
		}
		if count[s.Section] == 0 {
			order = append(order, s.Section)
		}
		count[s.Section]++
	}
	return count, order
}

func offsetKey(off int) string { return fmt.Sprintf("0x%04X", off) }

func diffEEPROM(a, b []byte, l Layout) []Change {
	var out []Change
	covered := map[int]bool{}
	entries := l.Entries
	// inA names the place in A of a byte of the moved state block.
	inA := func(int) string { return "" }
	baseA, okA := l.State.Find(a)
	baseB, okB := l.State.Find(b)
	inBlock := func(int) bool { return false }
	if okA && okB {
		entries = sorted(l.Entries, l.State.at(baseB))
	}
	if okA && okB && baseA != baseB {
		// Compare the block by meaning: put A's block where B's is.
		size := l.State.Size
		moved := append([]byte(nil), a...)
		clear(moved[baseA : baseA+size])
		copy(moved[baseB:], a[baseA:baseA+size])
		a = moved
		inBlock = func(off int) bool { return off >= baseB && off < baseB+size }
		inA = func(off int) string {
			if !inBlock(off) {
				return ""
			}
			return "; at " + offsetKey(off+baseA-baseB) + " in A"
		}
		out = append(out, Change{"eeprom", offsetKey(baseB), offsetKey(baseA), offsetKey(baseB), "state block moved"})
	}
	entries = append([]Entry(nil), entries...)
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
			Note: fmt.Sprintf("%s (%s)", e.Name, e.Confidence) + inA(e.Offset),
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
		for i < n && changed(i) && (i < both) == (start < both) && inBlock(i) == inBlock(start) {
			i++
		}
		c := Change{Section: "eeprom", Key: offsetKey(start)}
		if inBlock(start) {
			c.Note = "in the state block" + inA(start)
		}
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
