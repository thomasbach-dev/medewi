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

## UC8: Shot visualization & notes

Watch the running shot live on a phone — pressure (and its setpoint), flow,
volume, temperature — then browse the history and the finished shot's
curves afterwards. Right after tasting, an optional one-line note/verdict
can be attached to the shot: the lightweight end of UC3's journal.
Observation only; never required (UC2's anti-goal holds).

## UC9: Maintenance tracking

"When did I last backflush / descale / deep-clean?" answered without a
notebook. Detected program runs (backflush, descale) timestamp themselves;
manual entries cover what the stream cannot tell (detergent vs plain-water
backflush) or the machine cannot see (group gasket, shower screen, water
filter). Reminders derive from UC7's throughput counters; the panel shows
last-done/due per kind.

## UC10: Coffee inventory

The coffees in the house are entities (name, roaster, roast date); since
beans are portioned into single frozen doses, the inventory unit is the
dose. A "current coffee" is selected once; every shot links to it and
consumes a dose, so "how much is left" is an exact count, not an estimate.
First slice of the recipe schema behind UC2/UC3.

## UC11: Machine settings visibility

Decode the firmware's settings backups (MCu/MCm) and list the machine's
current configuration as readable name/value pairs; diff two backups to
see what changed. Feeds UC6 (an AI cannot discuss settings it cannot see)
and provides the read-back half of verify-after-write for any future
settings push. The license key inside the dumps is always redacted and
dumps are never committed.

## UC12: Machine tuning

Shorten the warm-up ("time to first good shot") and tune the heating
algorithm — leva!'s PID parameters — by measure-and-compare: capture a
baseline, change one variable via the machine's menu, compare the
transients. The software provides evidence and comparisons; until a
guarded write path exists, hands stay on the machine's own menu.

## UC13: Firmware updates

The rare firmware update gets an orchestrated procedure, not ad-hoc
bravery: release the machine, take a settings backup, let the **vendor's
flasher** do the flashing, then run the conformance suite as the
post-flash gate and diff the settings backups (UC11). The flashing itself
stays outside medewi — see the bootloader constraint below.

## Constraints

- Live in-shot control stays on the machine (rotary encoder = pressure,
  click = stop brew). The software never touches a running shot.
- The default path never forces data entry (anti-goal: mandatory science).
- Nothing may ever connect to the leva! bootloader port (2323): the first
  byte sent to it resets the controller.
