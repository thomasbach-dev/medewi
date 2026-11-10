# Log decoding CLI and CLI conventions — design

Issue: [#19](https://github.com/thomasbach-dev/medewi/issues/19). Related:
[#30](https://github.com/thomasbach-dev/medewi/issues/30) (MCP endpoint,
will reuse the decoder).

## Intent

The main use is **troubleshooting on brewhost**. Example: medewid once
lost its machine connection and would not reconnect. To find out why, the
operator wants to see what was on the wire around that moment. They read
the daemon's own log separately with `journalctl` over the same time
window; the two do not have to be merged. Decoding local captures
(`captures/*/log.jsonl`) for analysis is the second use, as #19 describes.

Reading raw logs remotely (an agent on the laptop browsing brewhost's
logs without ssh) is wanted later, but **not in this unit of work**: it
belongs to #30 or a later `medewictl` subcommand. This design only makes
that cheap: the decoding lives in a package, not in `main`.

The same unit of work sets **CLI conventions** for all medewi commands
and brings the existing commands in line with them.

## Goals

1. A `levalog decode` command (`cmd/levalog`, pure Go). It reads raw logs
   (`*.jsonl.gz`) and captures (`log.jsonl`, plain or gzip) and prints one
   line per event, as specified in #19:
   - time: ms from the file start, plus wall time where the file gives it
     (raw logs: file name = UTC start, `t` = ms since then);
   - direction (`tx`, `rx`, `note`);
   - reports decoded with the vendor XML (`name=value`, `?` masks kept),
     banners and replies as text, display frames as backlight/key/layout
     plus the four 16-char rows, marks and notes inline;
   - glued lines (a cut-off report or frame running into the next record)
     shown as such, and never a crash. Reuse `leva`'s splitting.
2. **Time windows in the first version**: `--since/-s` and `--until/-u`,
   as in journalctl. Given a directory (default: the raw-log directory,
   `/var/lib/medewid/raw`), levalog picks the files that overlap the
   window, reads them in time order and filters events to the window.
   File arguments still work.
3. **Shared time parsing** (new small package) that accepts a subset of
   `systemd.time(7)`. Every form we accept must be valid there with the
   same meaning; we do not need every form it lists. The first subset:
   - timestamps: `YYYY-MM-DD HH:MM[:SS]`, `YYYY-MM-DD`, `HH:MM[:SS]`
     (today), `now`, `today`, `yesterday`, in **local time** unless a
     zone is given (same as journalctl); a trailing ` UTC` should also
     work;
   - relative: `+SPAN` / `-SPAN` (and `SPAN ago`, if cheap);
   - spans: the systemd units we need (`us`, `ms`, `s`, `min`, `m`, `h`,
     `d`, combinable as in `1h30min`).
   `medewictl release --until +1h` moves onto this parser. Its accepted
   input becomes a superset of today's (`+` and h/min/s), so nothing that
   works today breaks.
4. **Switch all commands to a GNU-style flag library** (`spf13/pflag`, pure
   Go). Long options are `--name`; short aliases are allowed where useful
   (`-s`, `-u`). `-name` with a single dash stops working, so existing
   scripts that use it break. This is deliberate.
   Commands: `levacap`, `levasim`, `medewid`, `medewictl`, `levalog`.
5. **Update everything that invokes or documents them**: `nix/module.nix`
   (the `ExecStart` flags), `nix/vm-test.nix`, README, `docs/` and
   runbooks, usage texts, and error messages (e.g. `bad -until` →
   `bad --until`). Update `vendorHash` in `flake.nix`.
6. **A written guideline**, `docs/cli-conventions.md`, linked from
   CLAUDE.md (a short "CLI conventions" pointer in Hard rules or
   Commands). It covers: GNU-style long options; journalctl-style
   `--since/-s` and `--until/-u` for time ranges; times and durations
   only in forms valid in `systemd.time(7)` (listing the supported
   subset); and env vars as defaults that flags override (already the
   pattern).
7. `levalog` ships as a flake package and is installed on brewhost by the
   NixOS module, next to medewid, so the operator can run
   `sudo levalog decode -s '10:40' -u '10:45'` there.

## Non-goals

- Remote access to raw logs (no new HTTP route, no `medewictl rawlog`).
  That comes with #30.
- Merging journald lines into the decode output.
- The later commands from #19 (field timelines as TSV, change-only
  output, LCD-only view). `--since/--until` covers the "window around a
  mark" case for now.
- Full `systemd.time(7)` coverage (weekday names, calendar events,
  timezone names beyond UTC/local).

## Anti-goals

- Accepting a time or duration form whose meaning differs from
  systemd's. Where Go's `time.ParseDuration` and systemd disagree (e.g.
  `M` is a month in systemd), we use systemd's meaning or reject the
  form. We never guess. Existing `time.Duration` flags (e.g. levacap's
  `--display-every`) move onto the shared span parser.
- Printing anything that came from an `MCu`/`MCm` dump by default. The
  raw log never holds such lines (rawlog allowlist), but a capture can
  (levacap's `commands` scenario sends `MCu`/`MCm`). **Decided by the
  user: levalog hides dump lines by default**, i.e. lines starting with
  `|` or `:`, and replies to `MCu`/`MCm`. In their place it prints one
  `dump redacted (N lines)` note. Use the same rules as
  `rawlog.Session` (dump detection, reply window, quiet period): pull
  them out into a shared function used by both, with tests, rather than
  writing them a second time. A `--show-dumps` flag prints them
  unredacted (my call: the escape hatch "by default" implies). The
  guards for real logs (`private/`, `captures/` gitignored, the
  private-strings tripwire) stay untouched.
- Committing real logs as test data. Tests use synthetic fixtures.
- Ever talking to the machine. levalog only reads files.

## Constraints

- Hard rules in CLAUDE.md apply. Pure Go, no cgo. TDD. Conventional
  commits. One rebased feature branch and one PR against main, worked in
  a separate git worktree. `nix flake check -L` must pass before every
  commit.
- Reading raw logs on brewhost needs root: medewid runs with
  `DynamicUser`, and the directory is 0700.
- The vendor XML comes from `--xml` / `MEDEWI_XML_PATH`. Without it,
  levalog still prints reports, raw and undecoded, with a single warning
  rather than failing. On brewhost the XML is the module's `xmlFile`.
- A raw-log file may be the one being written, or may have lost its gzip
  trailer in a crash. Read such files the way `rawlog.ReadFile` does.

## Decided by the user

- Troubleshooting on the device comes first; remote access is deferred.
- The daemon journal and raw traffic are viewed separately.
- Time ranges use `--since/-s` and `--until/-u`; time formats are a subset
  of `systemd.time(7)`.
- Long `--option` style is enforced by a GNU-style flag library, and
  breaking single-dash usage is accepted.
- The guideline, the flag migration and #19 ship together in one PR.
- Dump lines are hidden by default.

## Builder's calls (made by me, open to change)

- Package names and layout: the decoder (e.g. `logview/` or
  `rawlog/decode`), the time parser (e.g. `systime/`).
- The exact output layout of a decoded line.
- Whether `levalog` gets a `--xml` flag in addition to the env var: yes,
  following the convention.
- Captures show ms offsets only. The manifest's `recorded` field is taken
  when levacap writes its output (the end of the capture, see
  `cmd/levacap/main.go` `writeOutput`), not at the start, so it gives no
  exact wall time. `--since/--until` on a capture is rejected with a
  clear error.
- Defaults on brewhost. The module installs `levalog` as a wrapper with
  `--xml ${cfg.xmlFile}` built in, because plain `sudo` drops
  `MEDEWI_XML_PATH`. levalog reads its raw-log directory from `--dir`,
  then `MEDEWID_RAW_DIR`, then the default `/var/lib/medewid/raw`. The
  VM test (`nix/vm-test.nix`) runs `levalog decode` once against the
  log that levasim traffic produced.
- Marks: inline for raw logs, where they are `note` events. levacap keeps
  capture marks only in the manifest's free-text `notes`, so for
  captures levalog prints that `notes` text once in a header line and
  does not parse it. levacap stays unchanged.
- Raw files are picked by their start time (the file name), since
  sessions run one after another.
- Order of commits within the PR (e.g. flag migration first, then the
  time parser, then levalog).
