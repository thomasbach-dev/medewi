# 0001 — Build on the leva! firmware

Date: 2026-10-30. Status: accepted.

## Context

The reference machine is a Rancilio Silvia retrofitted with the
softwareandcircuits **ito** controller running the closed-source **leva!**
firmware (3.2). leva! exposes a plain-text TCP interface (port 23): status
dumps, a live telemetry stream (pressure/flow/temperature), remote execution
of assignable functions (profile selection, doses, pump start/stop, setpoint
changes), and a "Virtual LCD" sub-protocol that can drive the entire menu
system remotely. It stores no per-shot data; curves exist only in the live
stream. Three foundations were considered:

1. **Beanconqueror** — the open-source coffee journal app has a
   machine-integration framework (Sanremo YOU, Xenia, Gaggiuino, …).
   Evaluated and rejected as foundation: its integrations are thin HTTP
   clients (leva! speaks raw TCP, so a bridge daemon is needed anyway), and
   it is at heart a manual journal — none of Medewi's core (guided flow,
   recipe push, always-on capture) fits its grain.
2. **Custom firmware** — the vendor ships a per-peripheral bring-up SDK and
   the toolchain; flashing is reversible (separate bootloader). Evaluated
   and deferred: it would mainly buy a cleaner protocol and deeper control
   (e.g. flow profiling) at the cost of re-engineering a decade of control
   and safety work (PID + autotune, phase-angle pump control, duty-cycle
   protection, UI) on a household's daily machine.
3. **Build on leva! over TCP** — accept the firmware as-is and put all new
   behavior in a companion system on the network.

## Decision

Option 3. Medewi treats leva! as a fixed, closed platform and integrates
exclusively through its TCP interface. Beanconqueror may return later as an
optional *client* of Medewi's API. Custom firmware is revisited only if
leva!'s control model becomes the limit.

## Consequences

- The port-23 protocol is the single integration surface; its quirks
  (single shared UART behind up to 4 TCP clients, 5-minute idle timeout,
  global stream-reporting state) shape the daemon.
- Shot curves must be captured live or are lost — an always-on component is
  mandatory ([0002](0002-daemon-owns-machine-and-data.md)).
- Remote settings changes are limited to what leva! exposes (selection and
  execution); arbitrary edits require driving menus via the Virtual-LCD
  protocol, which is deferred to a late phase.
- The bootloader port (2323) is hazardous (first byte resets the
  controller) and is treated as untouchable by all tooling.
