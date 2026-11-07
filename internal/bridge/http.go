package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type HTTPOptions struct {
	// MaxSnapshotAge is how old the last successful refresh may be before the
	// readiness endpoint starts returning 503. A single failed refresh does
	// not make the service unready while a recent snapshot is still available.
	// Zero restores the strict "last refresh must have succeeded" behaviour.
	MaxSnapshotAge time.Duration
}

func NewHTTPHandler(service *Service) http.Handler {
	return NewHTTPHandlerWithOptions(service, HTTPOptions{})
}

func NewHTTPHandlerWithOptions(service *Service, options HTTPOptions) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /health-check", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.Snapshot(r.Context())
		ready := err == nil && !snapshot.RefreshedAt.IsZero()
		if ready && options.MaxSnapshotAge > 0 && time.Since(snapshot.RefreshedAt) > options.MaxSnapshotAge {
			ready = false
		}
		if ready && options.MaxSnapshotAge == 0 && snapshot.RefreshError != "" {
			ready = false
		}
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		response := map[string]any{
			"ok":          ready,
			"refreshedAt": snapshot.RefreshedAt,
			"error":       snapshot.RefreshError,
		}
		if err != nil {
			response["error"] = err.Error()
		}
		writeJSON(w, status, response)
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		handleForcedRefresh(w, r, service)
	})
	mux.HandleFunc("GET /v1/devices/force", func(w http.ResponseWriter, r *http.Request) {
		handleForcedRefresh(w, r, service)
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.Snapshot(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
	mux.HandleFunc("GET /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		device, ok, err := service.Device(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "device was not found in the cache")
			return
		}
		writeJSON(w, http.StatusOK, device)
	})
	mux.HandleFunc("POST /v1/devices/{id}/switch", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid form data")
			return
		}
		handleSwitch(w, r, service, r.PostForm.Get("state"))
	})
	return mux
}

func handleForcedRefresh(w http.ResponseWriter, r *http.Request, service *Service) {
	if err := service.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	snapshot, err := service.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func handleSwitch(w http.ResponseWriter, r *http.Request, service *Service, state string) {
	device, err := service.Switch(r.Context(), r.PathValue("id"), state)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "was not found") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
