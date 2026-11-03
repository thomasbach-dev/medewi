package daemon

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
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
// nil, is mounted at /metrics.
func NewHandler(d *Daemon, metrics http.Handler) http.Handler {
	a := &api{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("POST /api/power", a.power)
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
	return mux
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
	writeJSON(w, http.StatusOK, struct {
		Connected bool       `json:"connected"`
		SpecOK    bool       `json:"spec_ok"`
		Ready     bool       `json:"ready"`
		BrewOpen  bool       `json:"brew_open"`
		Power     PowerState `json:"power"`
		Firmware  string     `json:"firmware"`
		Temp      *float64   `json:"temp"`
		Setpoint  *float64   `json:"setpoint"`
		LastBrew  *brewJSON  `json:"last_brew"`
	}{s.Connected, s.SpecOK, s.Ready, s.BrewOpen, s.Power, s.Firmware, s.Temp, s.Setpoint, last})
}

func (a *api) power(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State PowerState `json:"state"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
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

// statusFor maps a daemon refusal or failure to its HTTP status.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrInvalidPower):
		return http.StatusBadRequest
	case errors.Is(err, ErrBrewOpen), errors.Is(err, ErrSpecMismatch):
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
	_, _ = w.Write([]byte(strings.Join(dp.Lines, "\n")))
}
