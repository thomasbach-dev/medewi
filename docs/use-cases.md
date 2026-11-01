# Medewi — Use Cases

Medewi is a per-coffee recipe system with guided brewing and automatic shot
logging for espresso machines running the [leva!](https://www.softwareandcircuits.com/)
firmware (softwareandcircuits "ito" controller). The reference machine is a
Rancilio Silvia with the ito retrofit, leva! 3.2, reachable over the LAN on
leva!'s plain-text TCP interface (port 23).

## Personas

- **The barista** — dials in coffees, tweaks pressure profiles and
  temperatures, wants better repeatability without being forced into a
  science experiment on every shot.
- **The regular** — wants a great coffee without coping with the machinery;
  has "their usual" and needs guidance through the process.

A household detail that shapes everything: coffee is bought in variety,
portioned into single doses, and frozen. Several coffees are "active" at any
time (e.g. a darker roast for cappuccino, a lighter one for long espressos),
each with its own grind setting, temperature, pressure profile, and target
output — state that currently lives in the barista's head.

## UC1: Guided drink preparation (regular)

1. Pick a drink on a phone: cappuccino, americano, maybe café crème.
2. The system answers: which coffee to fetch from the freezer, which grind
   setting to set on the grinder.
3. The system pushes the matching machine settings (temperature, pressure
   profile, dose volume) — the machine's brew button then "just does the
   right thing".
4. The regular grinds, tamps, presses the brew button on the machine.
5. Optional guidance for the rest of the drink:
   - *Americano*: "place the cup under the wand, open it, press go" — the
     machine doses the right amount of hot water (it meters flow).
   - *Cappuccino*: bring the boiler to steam temperature (leva! has a PID2
     for this, though the vendor recommends keeping the steam thermostat —
     a safety decision still open), then guide through steaming.

## UC2: Everyday coffee, zero friction (barista)

Pick the coffee, see its current parameters, brew. No data entry, ever.
Telemetry is captured silently. The system must never turn an ordinary
morning coffee into an experiment.

## UC3: Recipe management

Each (coffee, drink) pair has one living "ideal parameter set": grind
setting, dose in, temperature, pressure profile, target volume out.
Adjusting it right after tasting *is* the logging gesture; the edit history
*is* the dial-in journal.

## UC4: Dialing in a new coffee

A trajectory, not a session: every shot is a real drink; after tasting, one
adjustment is recorded and takes effect at the next drink — possibly days
later. Starting points may come from an AI discussion. Machine-side changes
(a new pressure profile, a changed dose volume) are part of the record.

## UC5: Automatic shot logging

Machine telemetry (pressure, flow, temperature, time) is captured per shot
without user interaction, and stays comparable over time to detect drift in
grind, machine, or technique. The leva! firmware stores no shot curves —
only its live telemetry stream carries them — so an always-on daemon is
required (see [architecture](architecture.md)).

## UC6: Export & AI-assisted improvement

Recipe history and shot telemetry are easily exportable in an LLM-friendly
format, so the barista can discuss improvements with an AI and feed the
result back as a recipe adjustment. The loop: taste → record → export →
discuss → apply next time.

## UC7: Monitoring

Machine health lands in a Prometheus/Grafana stack: reachability, power
state, boiler temperature and setpoint ("machine is warmed up", "machine
left on" alerts), shot counter and water throughput (descaling and
flow-meter-recalibration reminders). The same plumbing captures shot
telemetry — the exporter is on the critical path for UC5.

## Constraints

- Live in-shot control stays on the machine (rotary encoder = pressure,
  click = stop brew). The software never touches a running shot.
- The default path never forces data entry (anti-goal: mandatory science).
- Nothing may ever connect to the leva! bootloader port (2323): the first
  byte sent to it resets the controller.
