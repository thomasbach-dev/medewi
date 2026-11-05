package leva

import "strings"

// FirmwareVersion extracts the version from an id banner, e.g.
// "$id: leva! (3.2)" gives "3.2". It returns "" if there is no parenthesised
// suffix.
func FirmwareVersion(id string) string {
	id = strings.TrimSpace(id)
	if !strings.HasSuffix(id, ")") {
		return ""
	}
	i := strings.LastIndex(id, "(")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(id[i+1 : len(id)-1])
}
