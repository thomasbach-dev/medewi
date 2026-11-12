package logview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syntheticPath = "../testdata/spec/synthetic.xml"

func xmlDir(t *testing.T, names ...string) string {
	t.Helper()
	data, err := os.ReadFile(syntheticPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n+".xml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCatalogFile(t *testing.T) {
	c, err := LoadCatalog(syntheticPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "synthetic"} {
		if spec, problem := c.Lookup(name); spec == nil || problem != "" {
			t.Errorf("Lookup(%q) = %v, %q", name, spec, problem)
		}
	}
	spec, problem := c.Lookup("other")
	if spec != nil || problem != "log names other, --xml is "+syntheticPath {
		t.Errorf("Lookup(other) = %v, %q", spec, problem)
	}
}

func TestCatalogDir(t *testing.T) {
	dir := xmlDir(t, "a", "b")
	c, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec, problem := c.Lookup("b"); spec == nil || problem != "" {
		t.Errorf("Lookup(b) = %v, %q", spec, problem)
	}
	if spec, problem := c.Lookup("b"); spec == nil || problem != "" { // cached
		t.Errorf("Lookup(b) again = %v, %q", spec, problem)
	}
	if spec, problem := c.Lookup("c"); spec != nil || problem != "c.xml not in "+dir {
		t.Errorf("Lookup(c) = %v, %q", spec, problem)
	}
	if spec, problem := c.Lookup("../x"); spec != nil || problem == "" {
		t.Errorf("Lookup(../x) = %v, %q", spec, problem)
	}
	if spec, problem := c.Lookup(""); spec != nil || problem != "no XML name in the log and 2 XMLs in "+dir {
		t.Errorf("Lookup(\"\") = %v, %q", spec, problem)
	}
}

func TestCatalogDirSingle(t *testing.T) {
	c, err := LoadCatalog(xmlDir(t, "only"))
	if err != nil {
		t.Fatal(err)
	}
	if spec, problem := c.Lookup(""); spec == nil || problem != "" {
		t.Errorf("Lookup(\"\") = %v, %q", spec, problem)
	}
}

func TestCatalogNil(t *testing.T) {
	var c *Catalog
	if spec, problem := c.Lookup("x"); spec != nil || problem != "" {
		t.Errorf("nil catalog = %v, %q", spec, problem)
	}
}

func TestCatalogErrors(t *testing.T) {
	if _, err := LoadCatalog(filepath.Join(t.TempDir(), "missing.xml")); err == nil {
		t.Error("missing path: no error")
	}
	bad := filepath.Join(t.TempDir(), "bad.xml")
	if err := os.WriteFile(bad, []byte("<cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(bad); err == nil || !strings.Contains(err.Error(), "bad.xml") {
		t.Errorf("invalid XML: err = %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.xml"), []byte("<cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec, problem := c.Lookup("bad"); spec != nil || problem == "" {
		t.Errorf("Lookup(bad) = %v, %q", spec, problem)
	}
}
