package logview

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thomasbach-dev/medewi/rawlog"
)

// Loaded is a decoded sequence of sources.
type Loaded struct {
	Entries  []Entry  // in path order; Entry.Source indexes Sources
	Sources  []Source // one per path
	Problems []string
}

// LastXML returns the session XML name in effect at the end of the newest
// raw log in dir that starts before the given time and names one, or ""
// when there is none. Files are decoded newest first, without a catalog,
// and the scan stops at the first one that yields a name. A file that fails
// to open is skipped with a warning "PATH: ERROR"; only an unreadable dir is
// an error.
func LastXML(dir string, before time.Time) (name string, warnings []string, err error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, fmt.Errorf("logview: %w", err)
	}
	type file struct {
		path  string
		start time.Time
	}
	var files []file
	for _, de := range des {
		if start, ok := rawlog.FileStart(de.Name()); ok && start.Before(before) {
			files = append(files, file{filepath.Join(dir, de.Name()), start})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].start.After(files[j].start) })
	for _, f := range files {
		src, err := Open(f.path)
		if err != nil {
			warnings = append(warnings, f.path+": "+err.Error())
			continue
		}
		if name := Decode(src, Options{}).XML; name != "" {
			return name, warnings, nil
		}
	}
	return "", warnings, nil
}

// Load opens and decodes paths in the order given. Within a run of raw logs
// from one directory the session XML carries from file to file; the first
// raw log of a run (also after a capture) starts from LastXML of its
// directory, and a capture starts with no XML. Callers pass contiguous
// ranges of a directory, so the carried XML is the one in effect.
func Load(paths []string, cat *Catalog, showDumps bool) (Loaded, error) {
	var l Loaded
	var xml, runDir string
	for i, p := range paths {
		src, err := Open(p)
		if err != nil {
			return Loaded{}, err
		}
		switch dir := filepath.Dir(src.Path); {
		case !src.HasWall:
			xml, runDir = "", ""
		case runDir != dir:
			var warns []string
			if xml, warns, err = LastXML(dir, src.Start); err != nil {
				return Loaded{}, err
			}
			l.Problems = append(l.Problems, warns...)
			runDir = dir
		}
		d := Decode(src, Options{Catalog: cat, XML: xml, ShowDumps: showDumps})
		xml = d.XML
		for j := range d.Entries {
			d.Entries[j].Source = i
		}
		l.Entries = append(l.Entries, d.Entries...)
		l.Sources = append(l.Sources, src)
		l.Problems = append(l.Problems, d.Problems...)
	}
	return l, nil
}
