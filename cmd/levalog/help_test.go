package main

import (
	"strings"
	"testing"
)

// subMarkers are a phrase of each subcommand's own help paragraph(s): in
// its SUBCOMMAND --help and in levalog --help, in no other subcommand's.
var subMarkers = map[string]string{
	"fields":   "fields lists the report fields of the XML in effect",
	"runs":     "runs --stats KEYS",
	"events":   "events --every SPAN",
	"programs": "a program that began before the input",
	"summary":  "a sessions: block",
	"extract":  "extract reads raw logs only",
}

// sharedMarker is a phrase of the shared input rules, only in levalog --help.
const sharedMarker = "A DIR is a capture directory"

// TestHelpPerSubcommand: SUBCOMMAND --help prints its synopsis, its own
// paragraphs, its examples, its flags and one line pointing to levalog
// --help, and not the other subcommands' paragraphs or the shared rules.
func TestHelpPerSubcommand(t *testing.T) {
	noEnv(t)
	for sub, marker := range subMarkers {
		code, out, errs := runCmd(t, sub, "--help")
		if code != 0 || out != "" {
			t.Errorf("%s --help: exit %d, stdout %q", sub, code, out)
		}
		if !strings.HasPrefix(errs, "usage: levalog "+sub+" ") {
			t.Errorf("%s --help does not start with its synopsis:\n%s", sub, errs)
		}
		for _, w := range []string{marker, "\nExamples:\n  levalog " + sub + " ", "\n" + sub + " flags:\n", "--dir string"} {
			if !strings.Contains(errs, w) {
				t.Errorf("%s --help lacks %q:\n%s", sub, w, errs)
			}
		}
		if n := strings.Count(errs, "levalog --help"); n != 1 || !strings.HasSuffix(errs, "levalog --help\n") {
			t.Errorf("%s --help: %d lines pointing to levalog --help, want one, last:\n%s", sub, n, errs)
		}
		if strings.Contains(errs, sharedMarker) {
			t.Errorf("%s --help repeats the shared input rules:\n%s", sub, errs)
		}
		for other, m := range subMarkers {
			if other != sub && strings.Contains(errs, m) {
				t.Errorf("%s --help holds the %s paragraph %q", sub, other, m)
			}
		}
		_, ex, _ := strings.Cut(errs, "\nExamples:\n")
		ex, _, _ = strings.Cut(ex, "\n\n")
		for _, l := range strings.Split(ex, "\n") {
			if !strings.HasPrefix(l, "  levalog "+sub+" ") && !strings.HasPrefix(l, "  levalog --xml") {
				t.Errorf("%s --help: example %q is not one of its own", sub, l)
			}
		}
	}
	// The flags of extract are its own.
	_, _, errs := runCmd(t, "extract", "--help")
	for _, w := range []string{"--split-sessions", "--out string", "--margin"} {
		if !strings.Contains(errs, w) {
			t.Errorf("extract --help lacks the flag %s", w)
		}
	}
}

// TestHelpFull: levalog --help is the whole text: the shared rules, every
// subcommand's paragraphs and every example.
func TestHelpFull(t *testing.T) {
	noEnv(t)
	code, _, errs := runCmd(t, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{sharedMarker, "levalog SUBCOMMAND --help", "Exit codes: 0 ok, 1 read error, 2 usage error."} {
		if !strings.Contains(errs, w) {
			t.Errorf("--help lacks %q", w)
		}
	}
	for sub, m := range subMarkers {
		if !strings.Contains(errs, m) || !strings.Contains(errs, "usage: levalog "+sub+" ") {
			t.Errorf("--help lacks the %s synopsis or paragraph %q", sub, m)
		}
		_, _, sub1 := runCmd(t, sub, "--help")
		_, ex, _ := strings.Cut(sub1, "\nExamples:\n")
		ex, _, _ = strings.Cut(ex, "\n\n")
		for _, l := range strings.Split(ex, "\n") {
			if !strings.Contains(errs, l+"\n") {
				t.Errorf("--help lacks %s's example %q", sub, l)
			}
		}
	}
}

// TestHelpNeutralTimes: the examples use neutral times, not a real window.
func TestHelpNeutralTimes(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "--help")
	if !strings.Contains(errs, "-s 09:00 -u 09:05") || strings.Contains(errs, "08:58:30") || strings.Contains(errs, "09:07") {
		t.Errorf("want the neutral -s 09:00 -u 09:05 in the examples, not the backflush window:\n%s", errs)
	}
}

// TestHelpSummarySessions: the help documents the sessions block in each
// format.
func TestHelpSummarySessions(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "summary", "--help")
	for _, w := range []string{"sessions:", `"sessions" array on every record`, "csv has no sessions", "first_file"} {
		if !strings.Contains(errs, w) {
			t.Errorf("summary --help lacks %q:\n%s", w, errs)
		}
	}
}

// TestHelpParagraphWidth: the paragraphs after each synopsis keep to the
// paragraph width.
func TestHelpParagraphWidth(t *testing.T) {
	for _, h := range subHelps {
		_, body, _ := strings.Cut(h.text, "\n\n")
		for _, l := range strings.Split(body, "\n") {
			if len(l) > 80 {
				t.Errorf("%s help: %d chars: %q", h.name, len(l), l)
			}
		}
	}
}

// TestHelpExtractSynopsis: the synopsis shows that one bound is enough.
func TestHelpExtractSynopsis(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "extract", "--help")
	if !strings.HasPrefix(errs, "usage: levalog extract (-s T [-u T] | -u T | --run N) ") {
		t.Errorf("extract synopsis:\n%s", errs)
	}
}

// TestHelpSummaryInputDiffers: summary help says it reads only the
// window's files, unlike extract.
func TestHelpSummaryInputDiffers(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "summary", "--help")
	if !strings.Contains(strings.Join(strings.Fields(errs), " "),
		"summary reads only the files of the window; extract reads back to the session's first file") {
		t.Errorf("summary --help lacks the input difference:\n%s", errs)
	}
}

// TestHelpExtractMargin: extract help shows --margin with -s/-u too.
func TestHelpExtractMargin(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "extract", "--help")
	flat := strings.Join(strings.Fields(errs), " ") // paragraphs rewrap
	for _, w := range []string{"-s 09:00 -u 09:05 --margin 30s", "--margin widens the -s/-u window, or the run window, on both sides"} {
		if !strings.Contains(flat, w) {
			t.Errorf("extract --help lacks %q", w)
		}
	}
}
