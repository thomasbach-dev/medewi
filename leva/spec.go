// Package leva speaks the leva! firmware's telemetry protocol.
package leva

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// ValueType is the declared type of a telemetry value.
type ValueType int

// Supported value types.
const (
	String ValueType = iota
	Float
	Int
)

// ValueSpec describes one fixed-width field of a telemetry line.
type ValueSpec struct {
	Index int
	Name  string // XML export attribute; may be empty
	At    int
	Len   int
	Type  ValueType
	Unit  string
	// Factor scales the raw value to Unit (the XML factor attribute). Zero
	// means 1, so specs built in Go need not set it.
	Factor float64
}

// RecordSpec describes a whole telemetry record.
type RecordSpec struct {
	Values []ValueSpec
}

type xmlValue struct {
	At     *int   `xml:"at,attr"`
	Len    *int   `xml:"len,attr"`
	Export string `xml:"export,attr"`
	Type   string `xml:"type,attr"`
	Unit   string `xml:"unit,attr"`
	Factor string `xml:"factor,attr"`
}

type xmlCfg struct {
	Values []xmlValue `xml:"values>value"`
}

// ParseSpec parses a Status Monitor XML file into a RecordSpec. Input that is
// not valid UTF-8 is transcoded from ISO-8859-1 before parsing; a declared
// non-UTF-8 encoding is also treated as ISO-8859-1.
func ParseSpec(r io.Reader) (*RecordSpec, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	transcoded := false
	if !utf8.Valid(data) {
		transcoded = true
		data, err = charmap.ISO8859_1.NewDecoder().Bytes(data)
		if err != nil {
			return nil, fmt.Errorf("transcode spec: %w", err)
		}
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) {
		if transcoded {
			return in, nil // already UTF-8; never decode twice
		}
		return charmap.ISO8859_1.NewDecoder().Reader(in), nil
	}
	var cfg xmlCfg
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if len(cfg.Values) == 0 {
		return nil, errors.New("parse spec: no <values> entries found")
	}
	spec := &RecordSpec{}
	for i, v := range cfg.Values {
		t, err := parseType(v.Type)
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", i, err)
		}
		if v.At == nil || *v.At < 0 {
			return nil, fmt.Errorf("value %d: missing or negative at", i)
		}
		if v.Len == nil || *v.Len <= 0 {
			return nil, fmt.Errorf("value %d: missing or non-positive len", i)
		}
		factor := 1.0
		if v.Factor != "" {
			factor, err = strconv.ParseFloat(v.Factor, 64)
			if err != nil || !(factor > 0) || math.IsInf(factor, 0) {
				return nil, fmt.Errorf("value %d: invalid factor %q", i, v.Factor)
			}
		}
		spec.Values = append(spec.Values, ValueSpec{
			Index: i, Name: v.Export, At: *v.At, Len: *v.Len, Type: t, Unit: v.Unit, Factor: factor,
		})
	}
	return spec, nil
}

func parseType(s string) (ValueType, error) {
	switch strings.ToLower(s) {
	case "string", "":
		return String, nil
	case "float":
		return Float, nil
	case "int":
		return Int, nil
	}
	return 0, fmt.Errorf("unknown value type %q", s)
}

// LoadSpecFile reads and parses the spec at path.
func LoadSpecFile(path string) (*RecordSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseSpec(f)
}
