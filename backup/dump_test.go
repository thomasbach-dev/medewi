package backup_test

import (
	"testing"

	"github.com/thomasbach-dev/medewi/backup"
	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestCheckDump(t *testing.T) {
	mcu, mcm := backuptest.ValidMCu(), backuptest.ValidMCm()
	for name, tc := range map[string]struct {
		cmd   string
		lines []string
		bad   bool
	}{
		"valid MCu":           {"MCu", mcu, false},
		"MCu without Cancel":  {"MCu", mcu[:len(mcu)-1], true},
		"empty MCu":           {"MCu", nil, true},
		"valid MCm":           {"MCm", mcm, false},
		"MCm without EOF":     {"MCm", mcm[:len(mcm)-1], true},
		"blank-only MCm":      {"MCm", []string{"", " "}, true},
		"MCm trailing blank":  {"MCm", append(append([]string(nil), mcm...), ""), false},
		"other command":       {"MCb", []string{"anything"}, false},
		"other command empty": {"MCb", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			ps := backup.CheckDump(tc.cmd, tc.lines)
			if bad := len(ps) > 0; bad != tc.bad {
				t.Errorf("CheckDump(%s) = %v, want problems: %v", tc.cmd, ps, tc.bad)
			}
		})
	}
}
