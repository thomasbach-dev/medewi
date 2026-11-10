# CLI conventions

Rules for every medewi command (`levacap`, `levasim`, `levalog`,
`medewid`, `medewictl`) and for any command added later. They keep the
tools predictable for the operator on brewhost and for scripts.

## Options

- GNU style via [`spf13/pflag`](https://github.com/spf13/pflag): long
  options are `--name`, values follow as `--name VALUE` or `--name=VALUE`.
  A single-dash long option (`-name`) is an error, not an alias.
- Short aliases only by intent, where a well-known tool sets the
  precedent. Today that is `-s`/`-u` for `--since`/`--until`. Do not add
  one just to save typing.
- Use the `=` form for a value that starts with a dash in scripts and
  docs (`--since=-5min`), so a reader never wonders whether it is a flag.
- Subcommands: global options come before the subcommand
  (`levalog --xml F decode …`, `medewictl --url U status`); the global
  flag set stops at the first non-option (`SetInterspersed(false)`).

## Time ranges

Commands that select a time window take `--since`/`-s` and
`--until`/`-u`, as `journalctl` does, so the same window can be pasted
into both:

```sh
sudo levalog decode -s '10:40' -u '10:45'
journalctl -u medewid -S '10:40' -U '10:45'
```

`--since` is inclusive, `--until` exclusive.

## Times and durations

Every time or duration we accept is valid in `systemd.time(7)` with the
same meaning; anything else is rejected, never guessed. The shared parser
is the `systime` package; use it instead of `time.ParseDuration` or a
hand-written format. The supported subset:

- **Spans:** groups of `NUMBER[.FRACTION] UNIT`, adjacent or separated by
  spaces (`1h30min`, `1h 30min`, `2.5s`). A lone number means seconds.
  Units: `us`/`usec`/`µs`, `ms`/`msec`, `s`/`sec`/`second(s)`,
  `m`/`min`/`minute(s)`, `h`/`hr`/`hour(s)`, `d`/`day(s)`, `w`/`week(s)`.
  Months and years (`M`, `y`) and negative spans are rejected.
- **Timestamps:** `YYYY-MM-DD HH:MM:SS`, `YYYY-MM-DD HH:MM`,
  `YYYY-MM-DD`, `HH:MM:SS` and `HH:MM` (today), `now`, `today`,
  `yesterday`, `tomorrow`, and relative `+SPAN`, `-SPAN` or `SPAN ago`.
  Wall-clock times are local time unless followed by ` UTC`, as in
  journalctl.

Examples: `medewictl release --until +1h30min`,
`levalog decode --since=-10min`, `levalog decode -s '2026-11-10 10:40 UTC'`.

## Environment variables

Environment variables are defaults; a flag always wins. The help text of
each flag names its variable (`(env MEDEWID_RAW_DIR)`), and the usage text
lists the fallbacks. Instance settings that differ per machine (addresses,
paths to secrets) come from the environment or the NixOS module, never
from committed files.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success (also `--help`) |
| 1 | runtime or read error: a file, the network or the machine failed |
| 2 | usage error: unknown flag, bad value, wrong combination of arguments |

Errors go to stderr as `COMMAND: message`, naming the flag in its `--name`
spelling. A missing optional input degrades with one warning instead of
failing (e.g. `levalog` without the vendor XML prints reports raw).
