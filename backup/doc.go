// Package backup parses and checks leva! settings backups: the MCu menu
// dump and (see mcm.go) the MCm firmware dump.
//
// The package is pure: it does no machine I/O and reads no files. Dumps
// are Latin-1 on the wire; Latin1 converts text to UTF-8 for output.
package backup
