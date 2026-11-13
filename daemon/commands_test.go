package daemon

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/sim"
)

func ecoLines(n int) [][]byte { return repeat(n, line('-', "ECO", 80, 79, 0, -1, 0, 0)) }

// coldLines is the machine with PID1 control off: mode CLD, boiler cold.
func coldLines(n int) [][]byte { return repeat(n, line('-', "CLD", 21.4, 0, 0, -1, 0, 0)) }

// oddLines carry a mode field the daemon does not know.
func oddLines(n int) [][]byte { return repeat(n, line('-', "XYZ", 93, 93, 0, -1, 0, 0)) }

// pumpLines keeps a brew open: the pump marker is on in every line.
func pumpLines(n int) [][]byte { return repeat(n, line('*', "PID", 93, 93, 9, 9, 120, 10)) }

// powerSim is a sim whose MCe switches to an eco stream and MCa back to an
// active one.
func powerSim(lines [][]byte) sim.Config {
	cfg := simConfig(lines)
	cfg.Switch = map[string][]fixture.Event{
		"MCe": stream(ecoLines(20)...),
		"MCa": stream(idleLines(20)...),
	}
	return cfg
}

// shrink sets *p to v for the test. Call it before runDaemon, so the
// daemon has stopped before the old value is restored.
func shrink[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func powerSent(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		if c == "MCa" || c == "MCe" || c == "MCE" {
			out = append(out, c)
		}
	}
	return out
}

func reqCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitActive(t *testing.T, d *Daemon) {
	t.Helper()
	waitFor(t, "connected, spec ok, active", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK && st.Power == PowerActive
	})
}

func TestSetPowerEcoVerified(t *testing.T) {
	s := startSim(t, powerSim(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	got, err := d.SetPower(reqCtx(t), PowerEco)
	if err != nil || got != PowerEco {
		t.Fatalf("SetPower(eco) = %q, %v; want eco, nil", got, err)
	}
	got, err = d.SetPower(reqCtx(t), PowerActive)
	if err != nil || got != PowerActive {
		t.Fatalf("SetPower(active) = %q, %v; want active, nil", got, err)
	}
	if p := powerSent(s.Received()); !slices.Equal(p, []string{"MCe", "MCa"}) {
		t.Errorf("power commands sent = %q, want [MCe MCa]", p)
	}
	assertOnlyAllowed(t, s, "MCe", "MCa")
}

func TestSetPowerVerifyTimeout(t *testing.T) {
	shrink(t, &verifyTimeout, 300*time.Millisecond)
	s := startSim(t, simConfig(idleLines(20))) // MCe changes nothing
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	got, err := d.SetPower(reqCtx(t), PowerEco)
	if !errors.Is(err, ErrVerifyFailed) || got != PowerActive {
		t.Fatalf("SetPower(eco) = %q, %v; want active, ErrVerifyFailed", got, err)
	}
	if p := powerSent(s.Received()); !slices.Equal(p, []string{"MCe"}) {
		t.Errorf("power commands sent = %q, want [MCe]", p)
	}
}

// noReportSim answers the handshake but its stream is empty: MCr starts
// reports that never come, so the daemon never sees a report line.
func noReportSim() sim.Config { return dumpSim(nil, fakeMCm) }

func waitSpecOK(t *testing.T, d *Daemon) {
	t.Helper()
	waitFor(t, "connected and spec ok", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK
	})
}

// TestSetPowerRefusals pins Review Focus 4: each wrong situation has its own
// error and sends no machine command. With dump set, the case requests both
// dumps instead of a power change.
func TestSetPowerRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     func() sim.Config
		prepare func(t *testing.T, s *sim.Server, d *Daemon)
		want    PowerState
		dump    bool
		wantErr error
	}{
		{
			name: "brew open",
			cfg:  func() sim.Config { return powerSim(pumpLines(20)) },
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) {
				waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })
			},
			want:    PowerEco,
			wantErr: ErrBrewOpen,
		},
		{
			name: "disconnected",
			cfg:  func() sim.Config { return powerSim(idleLines(20)) },
			prepare: func(t *testing.T, s *sim.Server, d *Daemon) {
				waitActive(t, d)
				if err := s.Stop(); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "disconnected", func() bool { return !d.Status().Connected })
			},
			want:    PowerEco,
			wantErr: ErrDisconnected,
		},
		{
			name: "spec mismatch",
			cfg: func() sim.Config {
				cfg := powerSim(idleLines(20))
				cfg.Responses = map[string][]string{"MC?": {"XML=other"}}
				return cfg
			},
			prepare: func(t *testing.T, s *sim.Server, d *Daemon) {
				waitFor(t, "connected and MC? sent", func() bool {
					return d.Status().Connected && count(s.Received(), "MC?") > 0
				})
			},
			want:    PowerEco,
			wantErr: ErrSpecMismatch,
		},
		{
			name: "spec mismatch dump",
			cfg: func() sim.Config {
				cfg := dumpSim(idleLines(20), fakeMCm)
				cfg.Responses["MC?"] = []string{"XML=other"}
				return cfg
			},
			prepare: func(t *testing.T, s *sim.Server, d *Daemon) {
				waitFor(t, "connected and MC? sent", func() bool {
					return d.Status().Connected && count(s.Received(), "MC?") > 0
				})
			},
			dump:    true,
			wantErr: ErrDisconnected,
		},
		{
			name:    "no report yet",
			cfg:     noReportSim,
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) { waitSpecOK(t, d) },
			want:    PowerEco,
			wantErr: ErrDisconnected,
		},
		{
			name:    "no report yet dump",
			cfg:     noReportSim,
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) { waitSpecOK(t, d) },
			dump:    true,
			wantErr: ErrDisconnected,
		},
		{
			name: "cold mode",
			cfg:  func() sim.Config { return powerSim(coldLines(20)) },
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) {
				waitFor(t, "cold", func() bool { return d.Status().Power == PowerCold })
			},
			want:    PowerActive,
			wantErr: ErrColdMode,
		},
		{
			name: "unknown mode",
			cfg:  func() sim.Config { return powerSim(oddLines(20)) },
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) {
				waitFor(t, "reporting, mode unknown", func() bool {
					st := d.Status()
					return st.Reporting && st.Power == PowerUnknown
				})
			},
			want:    PowerEco,
			wantErr: ErrPowerUnknown,
		},
		{
			name:    "already active",
			cfg:     func() sim.Config { return powerSim(idleLines(20)) },
			prepare: func(t *testing.T, _ *sim.Server, d *Daemon) { waitActive(t, d) },
			want:    PowerActive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startSim(t, tc.cfg())
			d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
			tc.prepare(t, s, d)

			start := time.Now()
			if tc.dump {
				for name, dump := range map[string]func(context.Context) (Dump, error){
					"MCm": d.DumpMCm, "MCu": d.DumpMCu,
				} {
					if _, err := dump(reqCtx(t)); !errors.Is(err, tc.wantErr) {
						t.Errorf("%s dump error = %v, want %v", name, err, tc.wantErr)
					}
				}
			} else {
				got, err := d.SetPower(reqCtx(t), tc.want)
				if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
					t.Fatalf("SetPower(%q) error = %v, want %v", tc.want, err, tc.wantErr)
				}
				if tc.wantErr == nil && got != tc.want {
					t.Errorf("SetPower(%q) = %q", tc.want, got)
				}
			}
			if el := time.Since(start); el > time.Second {
				t.Errorf("refusal took %v, want prompt", el)
			}
			time.Sleep(100 * time.Millisecond) // a stray command would arrive by now
			if p := powerSent(s.Received()); len(p) != 0 {
				t.Errorf("sent %q on refusal", p)
			}
			assertOnlyAllowed(t, s)
		})
	}
}

// fakeMCm is a made-up Intel HEX dump, not a real machine's.
var fakeMCm = []string{
	":10000000000102030405060708090A0B0C0D0E0F78",
	":10001000101112131415161718191A1B1C1D1E1F68",
	":00000001FF",
}

func dumpSim(lines [][]byte, mcm []string) sim.Config {
	cfg := simConfig(lines)
	cfg.Responses["MCu"] = []string{"user setting 1 = 42", "user setting 2 = 7"}
	cfg.Responses["MCm"] = mcm
	return cfg
}

func shrinkDumpTimings(t *testing.T) {
	shrink(t, &dumpFirstLine, time.Second)
	shrink(t, &dumpIdleGap, 200*time.Millisecond)
}

func TestDumpMCm(t *testing.T) {
	shrinkDumpTimings(t)
	s := startSim(t, dumpSim(idleLines(20), fakeMCm))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d) // reports are running: the dump interleaves with them

	before := time.Now()
	dump, err := d.DumpMCm(reqCtx(t))
	if err != nil {
		t.Fatalf("DumpMCm: %v", err)
	}
	if !slices.Equal(dump.Lines, fakeMCm) {
		t.Errorf("Lines = %q, want %q", dump.Lines, fakeMCm)
	}
	if dump.Firmware != "3.2" {
		t.Errorf("Firmware = %q, want 3.2", dump.Firmware)
	}
	if dump.Done.Before(before) || dump.Done.Location() != time.UTC {
		t.Errorf("Done = %v, want a UTC time after the request", dump.Done)
	}

	u, err := d.DumpMCu(reqCtx(t))
	if err != nil || len(u.Lines) != 2 || u.Lines[0] != "user setting 1 = 42" {
		t.Errorf("DumpMCu = %+v, %v", u, err)
	}
	assertOnlyAllowed(t, s, "MCm", "MCu")
}

// TestDumpWhateverTheMode pins that a dump needs a report seen on the
// connection, not a known power mode: cold mode (PID1 off) and an
// unrecognised mode field both still allow a backup.
func TestDumpWhateverTheMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines [][]byte
		power PowerState
	}{
		{"cold", coldLines(20), PowerCold},
		{"unknown", oddLines(20), PowerUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shrinkDumpTimings(t)
			s := startSim(t, dumpSim(tc.lines, fakeMCm))
			d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
			waitFor(t, "reporting in "+string(tc.power), func() bool {
				st := d.Status()
				return st.Connected && st.Reporting && st.Power == tc.power
			})
			if dump, err := d.DumpMCm(reqCtx(t)); err != nil || !slices.Equal(dump.Lines, fakeMCm) {
				t.Errorf("DumpMCm = %q, %v; want the dump", dump.Lines, err)
			}
			if _, err := d.DumpMCu(reqCtx(t)); err != nil {
				t.Errorf("DumpMCu: %v", err)
			}
			assertOnlyAllowed(t, s, "MCm", "MCu")
		})
	}
}

// TestDumpMCmTruncated pins Review Focus 5.
func TestDumpMCmTruncated(t *testing.T) {
	shrinkDumpTimings(t)
	s := startSim(t, dumpSim(idleLines(20), fakeMCm[:2]))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	if _, err := d.DumpMCm(reqCtx(t)); !errors.Is(err, ErrIncompleteDump) {
		t.Fatalf("DumpMCm error = %v, want ErrIncompleteDump", err)
	}
}

func TestDumpRefusedWhileBrewOpen(t *testing.T) {
	shrinkDumpTimings(t)
	s := startSim(t, dumpSim(pumpLines(20), fakeMCm))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })

	for name, dump := range map[string]func(context.Context) (Dump, error){
		"MCm": d.DumpMCm, "MCu": d.DumpMCu,
	} {
		if _, err := dump(reqCtx(t)); !errors.Is(err, ErrBrewOpen) {
			t.Errorf("%s dump error = %v, want ErrBrewOpen", name, err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	assertOnlyAllowed(t, s)
}

func TestSetPowerInvalid(t *testing.T) {
	s := startSim(t, powerSim(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)
	if _, err := d.SetPower(reqCtx(t), "bogus"); !errors.Is(err, ErrInvalidPower) {
		t.Errorf("SetPower(bogus) error = %v, want ErrInvalidPower", err)
	}
	time.Sleep(100 * time.Millisecond)
	assertOnlyAllowed(t, s)
}

func TestRequestAfterRunReturns(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	d, stop := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)
	stop()
	if _, err := d.SetPower(reqCtx(t), PowerEco); !errors.Is(err, ErrDisconnected) {
		t.Errorf("SetPower after Run returned: %v, want ErrDisconnected", err)
	}
}
