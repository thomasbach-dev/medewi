# Raw Traffic Log for medewid — Design

Date: 2026-11-04. Status: draft, agreed in conversation with the barista;
revised after review (redaction inverted to an allowlist).

## Intent

medewid keeps everything it exchanges with the machine for a bounded time
(default 7 days), so a failure noticed days later (a misclassified brew, a
reconnect storm, a stuck power change) can be troubleshot from the exact
bytes, and roughly replayed through `levasim`.

## Requirements

1. **What is logged:** every chunk the daemon sends (`tx`) and every
   received line (`rx`) the allowlist (5) keeps, from the Tap registered
   right after `leva.Dial` to the end of the session. Received bytes are
   logged per complete line (terminator included), not per TCP read; a
   partial line longer than 4096 bytes is emitted in 4096-byte pieces, and
   a partial line left at session end is emitted then. A line's time is
   the arrival of its first byte. `leva.Conn.send` fires the `tx` tap
   before the write (today: after), so a reply can never be tapped before
   its command; a failed write still shows as sent. Known gap: banners
   that arrive inside `Dial` before the Tap is registered.
2. **Format:** the fixture event format (`fixture.Event`: `t` ms, `dir`,
   base64 `data`), one JSON line per event, gzip-compressed. A new
   direction `fixture.Note` (`"note"`) carries plain-text annotations;
   `sim` replays only `rx` events, so notes are ignored on replay.
   `rawlog.ReadFile` reads a file, tolerating the missing gzip trailer of
   the file being written or one left by a crash.
   Replay recipe (approximate: the sim replays every `rx` line, banners and
   replies included, as the report stream):
   `gzip -dc FILE 2>/dev/null | grep '}$' > DIR/log.jsonl; levasim -fixtures DIR`
   (the `grep` keeps only complete lines, so a file still being written or
   left by a crash replays: a complete event line always ends in `}`, and
   base64 never contains `}`).
3. **Files:** one file per session, rotated when it is `RotateEvery` (1 h)
   old by the monotonic clock. Name: UTC wall time at open,
   `20061102T150405.000Z.jsonl.gz`, created with `O_EXCL` (on a collision
   the millisecond is bumped). `t` counts monotonic ms from the file's
   open, so a wall-clock jump never makes it negative. Mode `0600`,
   directory `0700`. A gzip flush every `FlushEvery` (5 s) bounds the loss
   after a crash to those seconds.
4. **Retention:** at startup and after each new file is opened, delete
   `*.jsonl.gz` files whose name time is older than `Retention`, then the
   oldest files while the directory total exceeds `MaxBytes`. The open
   file is never deleted and is excluded from that total, so the directory
   can exceed the cap by one file; other files are left alone. The NixOS
   unit orders after `time-sync.target`, which makes a not-yet-synced clock
   less likely to delete the logs of a fresh boot; a hard guarantee that the
   clock is synced needs `systemd-time-wait-sync`, the operator's choice
   (the module does not enable it).
5. **The license key never lands in the log.** `MCu` and `MCm` replies
   contain it, and the machine sends replies to every connected client,
   also for commands another client sent. Received lines are therefore
   dropped unless they are
   - a report line (starts `{`; truncated reports are kept too; a `{`
     line longer than 1024 bytes including its terminator is dropped and
     counted as redacted: a report missing its CRLF may have glued a dump
     line onto it),
   - a banner (starts `$id:`), or
   - inside a reply window: up to `ReplyWindow` (5 s) after medewid sent
     a command other than `MCu`/`MCm`, and other than the stream toggles
     `MCr`/`MC ` (no reply).

   Dump lines — starting with `|` (MCu) or `:` (Intel HEX, MCm;
   no legitimate `:`-led reply is known) — are always dropped. A dump in sight also
   shuts the windows: a dump line or an `MCu`/`MCm` tx closes the open
   window and no window opens for `DumpQuiet` (60 s) after the latest of
   them. Reason: a real `MCu` reply interleaves letter-led lines (one of
   them key-related) with its `|` lines, so letter lines are only safe
   far from any dump. Remaining risk, accepted: the first line of ANOTHER
   client's `MCu` reply (a letter-led header, before its first `|` line)
   arriving inside one of medewid's windows is kept; on the real capture
   that header carries no key. The `DumpQuiet` clock is shared by all
   sessions, so it survives reconnects once a dump line was seen. Also
   accepted: a reconnect of medewid in the middle of another client's dump,
   of which medewid never saw any line before, can keep letter-led lines of
   that reply inside the new session's `MC?` window; only one client may
   talk to the machine (CLAUDE.md), so this is not expected in practice.

   The window state is taken at a line's first byte; the shape is judged
   when the line completes. A line split into 4096-byte pieces is judged
   once, by its start. Each run of dropped lines becomes one `note` event,
   `dropped N lines`, written before the next kept event or at session
   end. The `Session` sees `tx` itself, so the daemon needs no redaction
   calls. Verified on a real `MCu`/`MCm` capture: reply lines start with
   `|`, `:` or letters, never `{`.
6. **Never slows the connection:** the tap only enqueues into a bounded
   buffer (4096 events). When it is full, events are dropped and counted.
   A writer goroutine does all disk work. Write errors are counted and
   logged; the current file is abandoned and a new one is opened at the
   next rotation or session. The daemon never fails because of the log at
   runtime. `Tap` after `Session.Close` or `Log.Close` is a no-op (taps can
   fire after `remove()` returns); an item of a session that is no longer
   current at the writer is dropped. Note text is base64 in the JSON like
   all data (`jq -r .data | base64 -d` reads it).
7. **Config (medewid):**
   | Variable | Flag | Default |
   | --- | --- | --- |
   | `MEDEWID_RAW_DIR` | `-raw-dir` | `/var/lib/medewid/raw` |
   | `MEDEWID_RAW_RETENTION` | `-raw-retention` | `168h`; `0` disables the log |
   | `MEDEWID_RAW_MAX_MB` | `-raw-max-mb` | `256` |
   If the directory cannot be opened at startup, medewid exits 1 when it
   was set explicitly, and only warns (running without the log) when the
   default is unusable, as in a local trial run. An override via
   `environmentFile` must stay under `/var/lib/medewid` (as for
   `MEDEWID_DB`).
8. **Metrics:** `medewi_rawlog_dropped_total` (buffer full),
   `medewi_rawlog_write_errors_total`, `medewi_rawlog_redacted_lines_total`
   (lines dropped by requirement 5).
9. **Size:** about 100 MB/day raw; expected 5–10 MB/day compressed (to be
   measured on the first real week). `MaxBytes` is the backstop.

## Non-goals

- Display frames (`MC@`): the daemon never sends it.
- Shipping logs off the host, or an API to fetch them.
- Reading `.gz` directly in `levasim`; capturing the pre-Tap banners
  (would need a `leva.Dial` tap option; later if needed).

## Shape

New package `rawlog` (no dependency on `leva` or `daemon`):

- `Open(Config) (*Log, error)`, `(*Log).Close() error`;
  `(*Log).Session() *Session` per connection;
  `(*Session).Tap(dir string, p []byte)` (fits `leva.Conn.Tap`),
  `(*Session).Close()`; `ReadFile(path) ([]fixture.Event, error)`.
  `leva.Conn.send` changes to fire the `tx` tap before writing. All
  methods are no-ops on a nil receiver.
- Counters `Dropped()`, `WriteErrors()`, `Redacted()`; medewid registers
  them as Prometheus counter funcs.

The daemon takes `Config.Raw *rawlog.Log`, opens a Session right after
`Dial`, taps it, and closes it at the very end of the session, after
`conn.Close` (by then the final `MC ` is already sent and logged).
