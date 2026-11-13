package daemon

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/store"
)

//go:embed panel.html
var panelHTML []byte

const (
	defaultBrewLimit = 20
	maxBrewLimit     = 500
	maxBodyBytes     = 1 << 10
)

// Store returns the brew database the daemon records into.
func (d *Daemon) Store() *store.Store { return d.cfg.Store }

// NewHandler serves the HTTP API and the ops panel for d. metrics, if not
// nil, is mounted at /metrics. Cross-origin browser requests that are not
// GET, HEAD or OPTIONS are refused with 403 (http.CrossOriginProtection).
func NewHandler(d *Daemon, metrics http.Handler) http.Handler {
	a := &api{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("POST /api/power", a.power)
	mux.HandleFunc("POST /api/release", a.release)
	mux.HandleFunc("POST /api/reclaim", a.reclaim)
	mux.HandleFunc("POST /api/mark", a.mark)
	mux.HandleFunc("GET /api/brews", a.brews)
	mux.HandleFunc("GET /api/brews/{id}/points", a.points)
	mux.HandleFunc("GET /api/backup/mcu", func(w http.ResponseWriter, r *http.Request) { a.backup(w, r, d.DumpMCu) })
	mux.HandleFunc("GET /api/backup/mcm", func(w http.ResponseWriter, r *http.Request) { a.backup(w, r, d.DumpMCm) })
	if metrics != nil {
		mux.Handle("GET /metrics", metrics)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(panelHTML)
	})
	return http.NewCrossOriginProtection().Handler(mux)
}

type api struct{ d *Daemon }

type brewJSON struct {
	ID        int64      `json:"id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Kind      string     `json:"kind"`
	DurationS float64    `json:"duration_s"`
	PeakBar   float64    `json:"peak_bar"`
	VolumeML  float64    `json:"volume_ml"`
	Notes     string     `json:"notes"`
}

func toBrewJSON(b store.Brew) brewJSON {
	return brewJSON{b.ID, b.StartedAt, b.EndedAt, b.Kind, b.DurationS, b.PeakBar, b.VolumeML, b.Notes}
}

type pointJSON struct {
	TMs      int64    `json:"t_ms"`
	Temp     *float64 `json:"temp"`
	Setpoint *float64 `json:"setpoint"`
	Pressure *float64 `json:"pressure"`
	Flow     *float64 `json:"flow"`
	Heat     *float64 `json:"heat"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("api: write response", "err", err)
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}

func (a *api) status(w http.ResponseWriter, _ *http.Request) {
	s := a.d.Status()
	var last *brewJSON
	if s.LastBrew != nil {
		b := toBrewJSON(*s.LastBrew)
		last = &b
	}
	var display *displayJSON
	if s.Display != nil {
		display = &displayJSON{s.Display.Rows, s.Display.At}
	}
	writeJSON(w, http.StatusOK, struct {
		Connected     bool         `json:"connected"`
		SpecOK        bool         `json:"spec_ok"`
		Ready         bool         `json:"ready"`
		MachineReady  bool         `json:"machine_ready"`
		BrewOpen      bool         `json:"brew_open"`
		Power         PowerState   `json:"power"`
		Firmware      string       `json:"firmware"`
		Temp          *float64     `json:"temp"`
		Setpoint      *float64     `json:"setpoint"`
		LastBrew      *brewJSON    `json:"last_brew"`
		Display       *displayJSON `json:"display"`
		ReleasedUntil *time.Time   `json:"released_until"`
	}{s.Connected, s.SpecOK, s.Ready, s.MachineReady, s.BrewOpen, s.Power, s.Firmware, s.Temp, s.Setpoint, last, display, s.ReleasedUntil})
}

type displayJSON struct {
	Rows [4]string `json:"rows"`
	At   time.Time `json:"at"`
}

// decodeJSON reads a size-bounded JSON body into v. It refuses any other
// content type with 415: a JSON body cannot come from a "simple"
// cross-origin form or fetch. On failure it has written the error.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content type must be application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return false
	}
	return true
}

func (a *api) mark(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.d.Mark(body.Text); err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *api) power(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State PowerState `json:"state"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	got, err := a.d.SetPower(r.Context(), body.State)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": got})
		return
	}
	if errors.Is(err, ErrVerifyFailed) {
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"ok": false, "state": got, "error": err.Error()})
		return
	}
	writeErr(w, statusFor(err), err)
}

func (a *api) release(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Until time.Time `json:"until"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Until.IsZero() {
		writeErr(w, http.StatusBadRequest, errors.New("until is required"))
		return
	}
	res, err := a.d.Release(r.Context(), body.Until)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "until": res.Until, "banner": res.Banner})
	case errors.Is(err, ErrReleaseStore):
		// First: a store write that ran into its deadline wraps a ctx error.
		slog.Warn("api: release", "err", err)
		writeErr(w, http.StatusInternalServerError, ErrReleaseStore)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The release was persisted and is in effect; only the wait ended.
		slog.Info("api: release in effect; client went away", "until", body.Until)
	default:
		writeErr(w, statusFor(err), err)
	}
}

func (a *api) reclaim(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := a.d.Reclaim(r.Context())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, ErrReleaseStore):
		slog.Warn("api: reclaim", "err", err)
		writeErr(w, http.StatusInternalServerError, ErrReleaseStore)
	default:
		writeErr(w, statusFor(err), err)
	}
}

// statusFor maps a daemon refusal or failure to its HTTP status.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrInvalidPower), errors.Is(err, ErrInvalidMark), errors.Is(err, ErrInvalidRelease):
		return http.StatusBadRequest
	case errors.Is(err, ErrBrewOpen), errors.Is(err, ErrSpecMismatch), errors.Is(err, ErrNoRawLog), errors.Is(err, ErrReleased),
		errors.Is(err, ErrColdMode), errors.Is(err, ErrPowerUnknown):
		return http.StatusConflict
	case errors.Is(err, ErrDisconnected):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrVerifyFailed):
		return http.StatusGatewayTimeout
	case errors.Is(err, ErrIncompleteDump):
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

func (a *api) brews(w http.ResponseWriter, r *http.Request) {
	limit := defaultBrewLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxBrewLimit {
			writeErr(w, http.StatusBadRequest, errors.New("limit must be an integer from 1 to 500"))
			return
		}
		limit = n
	}
	bs, err := a.d.Store().Brews(r.Context(), limit)
	if err != nil {
		slog.Warn("api: list brews", "err", err)
		writeErr(w, http.StatusInternalServerError, errors.New("store error"))
		return
	}
	out := make([]brewJSON, len(bs))
	for i, b := range bs {
		out[i] = toBrewJSON(b)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) points(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such brew"))
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "csv" && format != "json" {
		writeErr(w, http.StatusBadRequest, errors.New("format must be csv or json"))
		return
	}
	pts, err := a.d.Store().Points(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, errors.New("no such brew"))
		return
	}
	if err != nil {
		slog.Warn("api: brew points", "err", err)
		writeErr(w, http.StatusInternalServerError, errors.New("store error"))
		return
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"t_ms", "temp", "setpoint", "pressure", "flow", "heat"})
		for _, p := range pts {
			_ = cw.Write([]string{
				strconv.FormatInt(p.TMs, 10), cell(p.Temp), cell(p.Setpoint),
				cell(p.Pressure), cell(p.Flow), cell(p.Heat),
			})
		}
		cw.Flush()
		return
	}
	out := make([]pointJSON, len(pts))
	for i, p := range pts {
		out[i] = pointJSON{p.TMs, p.Temp, p.Setpoint, p.Pressure, p.Flow, p.Heat}
	}
	writeJSON(w, http.StatusOK, out)
}

// cell renders a nullable value for CSV; null is an empty cell.
func cell(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func (a *api) backup(w http.ResponseWriter, r *http.Request, dump func(context.Context) (Dump, error)) {
	dp, err := dump(r.Context())
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Medewi-Firmware", dp.Firmware)
	w.Header().Set("X-Medewi-Lines", strconv.Itoa(len(dp.Lines)))
	body := strings.Join(dp.Lines, "\n")
	if len(dp.Lines) > 0 {
		body += "\n" // so dumps concatenated by medewictl keep their last line
	}
	_, _ = w.Write([]byte(body))
}
