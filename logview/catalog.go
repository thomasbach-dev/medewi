package logview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thomasbach-dev/medewi/leva"
)

// Catalog resolves the vendor XML for a session by the name the machine
// reports (MC? "XML=NAME"). It is either one file, used for every session,
// or a directory of NAME.xml files loaded on first use.
type Catalog struct {
	path  string
	isDir bool

	file *leva.RecordSpec // file mode

	mu    sync.Mutex
	cache map[string]cached // dir mode
}

type cached struct {
	spec    *leva.RecordSpec
	problem string
}

// LoadCatalog opens path: a file (parsed now, so an invalid XML is an
// error) or a directory of NAME.xml files (parsed lazily and cached).
func LoadCatalog(path string) (*Catalog, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("logview: %w", err)
	}
	c := &Catalog{path: path, isDir: fi.IsDir()}
	if c.isDir {
		c.cache = map[string]cached{}
		return c, nil
	}
	if c.file, err = leva.LoadSpecFile(path); err != nil {
		return nil, fmt.Errorf("logview: %s: %w", path, err)
	}
	return c, nil
}

// Lookup returns the spec for the XML name of a session ("" when the log
// names none). A nil spec comes with a problem text, except for a nil
// *Catalog, which stays silent: reports are shown raw.
func (c *Catalog) Lookup(name string) (spec *leva.RecordSpec, problem string) {
	if c == nil {
		return nil, ""
	}
	if !c.isDir {
		if base := strings.TrimSuffix(filepath.Base(c.path), ".xml"); name == "" || name == base {
			return c.file, ""
		}
		return nil, fmt.Sprintf("log names %s, --xml is %s", name, c.path)
	}
	if name == "" {
		xmls, _ := filepath.Glob(filepath.Join(c.path, "*.xml"))
		if len(xmls) != 1 {
			return nil, fmt.Sprintf("no XML name in the log and %d XMLs in %s", len(xmls), c.path)
		}
		name = strings.TrimSuffix(filepath.Base(xmls[0]), ".xml")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.cache[name]; ok {
		return r.spec, r.problem
	}
	var r cached
	file := name + ".xml"
	if name != filepath.Base(name) || name == ".." {
		r.problem = fmt.Sprintf("%s not in %s", file, c.path)
	} else if _, err := os.Stat(filepath.Join(c.path, file)); err != nil {
		r.problem = fmt.Sprintf("%s not in %s", file, c.path)
	} else if r.spec, err = leva.LoadSpecFile(filepath.Join(c.path, file)); err != nil {
		r.problem = fmt.Sprintf("%s: %v", file, err)
	}
	c.cache[name] = r
	return r.spec, r.problem
}
