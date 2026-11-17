package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	timeLayout   = "2006-01-02T150405Z"
	mcuSuffix    = ".mcu.txt"
	mcmSuffix    = ".mcm.hex"
	pairWindow   = 5 * time.Minute
	sniffMCmMark = ":"
)

// Name is a backup file name taken apart:
// TIMESTAMP_FIRMWARE[_NOTE].KIND.
type Name struct {
	Time     time.Time
	Firmware string
	Note     string
	Kind     Kind
}

func splitKind(base string) (stem string, kind Kind, ok bool) {
	switch {
	case strings.HasSuffix(base, mcuSuffix):
		return strings.TrimSuffix(base, mcuSuffix), KindMCu, true
	case strings.HasSuffix(base, mcmSuffix):
		return strings.TrimSuffix(base, mcmSuffix), KindMCm, true
	}
	return "", "", false
}

// ParseName takes a backup file's base name apart. FIRMWARE never contains
// "_", so everything after the second "_" is the note.
func ParseName(base string) (Name, bool) {
	stem, kind, ok := splitKind(base)
	if !ok {
		return Name{}, false
	}
	ts, rest, ok := strings.Cut(stem, "_")
	if !ok {
		return Name{}, false
	}
	t, err := time.Parse(timeLayout, ts)
	if err != nil {
		return Name{}, false
	}
	fw, note, _ := strings.Cut(rest, "_")
	if fw == "" {
		return Name{}, false
	}
	return Name{Time: t, Firmware: fw, Note: note, Kind: kind}, true
}

// Backup is one MCu/MCm pair (either half may be missing).
type Backup struct {
	Src              string // the MCu file's path, else the MCm file's
	Note             string
	Firmware         string
	Time             time.Time
	MCuPath, MCmPath string
	MCu              *MCu // set by Load
	MCm              *MCm
}

type file struct {
	path   string
	kind   Kind
	stem   string // "" when the name did not parse
	name   Name
	parsed bool
}

func newFile(path string, kind Kind) file {
	f := file{path: path, kind: kind}
	if n, ok := ParseName(filepath.Base(path)); ok {
		f.name, f.parsed = n, true
		f.kind = n.Kind
		f.stem, _, _ = splitKind(filepath.Base(path))
	}
	return f
}

// Resolve turns FILE and DIR arguments into backups. The FILE arguments
// are paired among themselves like a directory's files (same stem, then
// by time), but only within one directory; a pair sits at the position of
// its first file. A DIR's backups (its own files, then those of its
// subdirectories, which are always read, in name order) are ordered by
// the time in their file names, stable; backups without a parseable time
// come after the dated ones. Skipped and unpaired files are reported in notes.
func Resolve(args []string) (bks []Backup, notes []string, err error) {
	type item struct {
		pos int
		bks []Backup
	}
	var items []item
	argPos := map[string]int{} // file path -> argument position
	var dirs []string          // directories of the file arguments, in order
	byDir := map[string][]file{}
	for i, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, nil, err
		}
		if st.IsDir() {
			b, n, err := resolveDir(a, true)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, item{i, b})
			notes = append(notes, n...)
			continue
		}
		kind, err := sniff(a)
		if err != nil {
			return nil, nil, err
		}
		f := newFile(a, kind)
		if _, ok := argPos[a]; !ok {
			argPos[a] = i
		}
		d := filepath.Dir(a)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	for _, d := range dirs {
		b, n := pair(byDir[d])
		notes = append(notes, n...)
		for _, bk := range b {
			pos := argPos[bk.MCuPath]
			if bk.MCuPath == "" || (bk.MCmPath != "" && argPos[bk.MCmPath] < pos) {
				pos = argPos[bk.MCmPath]
			}
			items = append(items, item{pos, []Backup{bk}})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].pos < items[j].pos })
	for _, it := range items {
		bks = append(bks, it.bks...)
	}
	return bks, notes, nil
}

func sniff(path string) (Kind, error) {
	if _, k, ok := splitKind(filepath.Base(path)); ok {
		return k, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(string(data), sniffMCmMark) {
		return KindMCm, nil
	}
	return KindMCu, nil
}

func resolveDir(dir string, descend bool) (bks []Backup, notes []string, err error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files []file
	var subs []string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			subs = append(subs, p)
			continue
		}
		f := newFile(p, "")
		if !f.parsed {
			notes = append(notes, "skipped "+p+": not a backup file name")
			continue
		}
		files = append(files, f)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if !files[i].name.Time.Equal(files[j].name.Time) {
			return files[i].name.Time.Before(files[j].name.Time)
		}
		return files[i].path < files[j].path
	})
	b, n := pair(files)
	bks, notes = append(bks, b...), append(notes, n...)
	if descend {
		sort.Strings(subs)
		for _, s := range subs {
			b, n, err := resolveDir(s, false)
			if err != nil {
				return nil, nil, err
			}
			bks, notes = append(bks, b...), append(notes, n...)
		}
	}
	if descend {
		sort.SliceStable(bks, func(i, j int) bool {
			zi, zj := bks[i].Time.IsZero(), bks[j].Time.IsZero()
			if zi || zj {
				return !zi && zj
			}
			return bks[i].Time.Before(bks[j].Time)
		})
	}
	return bks, notes, nil
}

// pair joins files into backups: same stem first, then each MCu with the
// closest later MCm of the same firmware within five minutes. Output order
// follows the first file of each backup.
func pair(files []file) ([]Backup, []string) {
	mate := make([]int, len(files)) // index of partner + 1
	for i, a := range files {
		if mate[i] != 0 || a.kind != KindMCu || !a.parsed {
			continue
		}
		for j, b := range files {
			if mate[j] == 0 && b.kind == KindMCm && b.parsed && b.stem == a.stem {
				mate[i], mate[j] = j+1, i+1
				break
			}
		}
	}
	for i, a := range files {
		if mate[i] != 0 || a.kind != KindMCu || !a.parsed {
			continue
		}
		best := -1
		for j, b := range files {
			if mate[j] != 0 || b.kind != KindMCm || !b.parsed || b.name.Firmware != a.name.Firmware {
				continue
			}
			d := b.name.Time.Sub(a.name.Time)
			if d < 0 || d > pairWindow {
				continue
			}
			if best < 0 || d < files[best].name.Time.Sub(a.name.Time) {
				best = j
			}
		}
		if best >= 0 {
			mate[i], mate[best] = best+1, i+1
		}
	}
	var bks []Backup
	var notes []string
	for i, f := range files {
		if mate[i] != 0 && mate[i]-1 < i {
			continue // emitted with its partner
		}
		var u, m *file
		if f.kind == KindMCu {
			u = &files[i]
		} else {
			m = &files[i]
		}
		if mate[i] != 0 {
			p := &files[mate[i]-1]
			if p.kind == KindMCu {
				u = p
			} else {
				m = p
			}
		} else {
			notes = append(notes, "unpaired "+string(f.kind)+" file "+f.path)
		}
		var b Backup
		first := u
		if u != nil {
			b.MCuPath = u.path
		} else {
			first = m
		}
		if m != nil {
			b.MCmPath = m.path
		}
		b.Src = first.path
		b.Note, b.Firmware, b.Time = first.name.Note, first.name.Firmware, first.name.Time
		bks = append(bks, b)
	}
	return bks, notes
}

// Load reads and parses the backup's files.
func (b *Backup) Load() error {
	if b.MCuPath != "" {
		data, err := os.ReadFile(b.MCuPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", b.MCuPath, err)
		}
		b.MCu = ParseMCu(SplitLines(data))
	}
	if b.MCmPath != "" {
		data, err := os.ReadFile(b.MCmPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", b.MCmPath, err)
		}
		b.MCm = ParseMCm(SplitLines(data))
	}
	return nil
}
