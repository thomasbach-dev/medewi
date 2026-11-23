// Package program finds runs of the leva! machine programs (warm-up,
// autotune, pressure and flow test, backflush, descale, purge) in a stream
// of decoded report samples. It is pure: no I/O, no clock; time comes from
// Sample.At.
//
// A Detector collects pump pulses and the mode and setpoint changes around
// them; Runs classifies everything seen so far. The rules use firmware
// behaviour only; display frames refine a run's status. See
// docs/superpowers/specs/2026-11-23-program-detection-design.md.
package program
