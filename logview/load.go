package logview

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thomasbach-dev/medewi/rawlog"
)

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
		if d, _ := DecodeEach(src, Options{}, nil); d.XML != "" {
			return d.XML, warnings, nil
		}
	}
	return "", warnings, nil
}

// Stream decodes a sequence of log files one at a time and hands on what
// it finds, so memory stays bounded by one file however long the input is:
// no file's events or entries are kept once it is decoded.
//
// Within a run of raw logs from one directory the session XML carries from
// file to file; the first raw log of a run (also after a capture) starts
// from LastXML of its directory, and a capture starts with no XML. Callers
// pass contiguous ranges of a directory, so the carried XML is the one in
// effect. A Stream may be extended with Add after it has run; the XML
// carries on.
type Stream struct {
	Catalog   *Catalog // nil: reports stay raw
	ShowDumps bool     // keep MCu/MCm dump lines

	// OnSource, when set, is called for each file once it is opened,
	// before its entries.
	OnSource func(SourceInfo) error
	// OnEntry, when set, is called for every entry in input order, with
	// Source and Index numbering files and entries across all Add calls.
	// Entries are not reused.
	OnEntry func(*Entry) error

	// Problems collects the decoders' problems and LastXML's warnings.
	Problems []string

	sources, entries int
	xml, runDir      string
}

// Add opens and decodes paths, in the order given, after those added
// before. It stops at the first error: a file that fails to open or an
// error of OnSource or OnEntry.
func (s *Stream) Add(paths ...string) error {
	for _, p := range paths {
		src, err := Open(p)
		if err != nil {
			return err
		}
		switch dir := filepath.Dir(src.Path); {
		case !src.HasWall:
			s.xml, s.runDir = "", ""
		case s.runDir != dir:
			var warns []string
			if s.xml, warns, err = LastXML(dir, src.Start); err != nil {
				return err
			}
			s.Problems = append(s.Problems, warns...)
			s.runDir = dir
		}
		i := s.sources
		s.sources++
		if s.OnSource != nil {
			if err := s.OnSource(src.Info(i)); err != nil {
				return err
			}
		}
		d, err := DecodeEach(src, Options{Catalog: s.Catalog, XML: s.xml, ShowDumps: s.ShowDumps}, func(e *Entry) error {
			e.Source, e.Index = i, s.entries
			s.entries++
			if s.OnEntry != nil {
				return s.OnEntry(e)
			}
			return nil
		})
		s.xml = d.XML
		s.Problems = append(s.Problems, d.Problems...)
		if err != nil {
			return err
		}
	}
	return nil
}
