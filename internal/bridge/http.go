package bridge

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewHTTPHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /health-check", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.Snapshot(r.Context())
		status := http.StatusOK
		if err != nil || snapshot.RefreshedAt.IsZero() || snapshot.RefreshError != "" {
			status = http.StatusServiceUnavailable
		}
		response := map[string]any{
			"ok":          status == http.StatusOK,
			"refreshedAt": snapshot.RefreshedAt,
			"error":       snapshot.RefreshError,
		}
		if err != nil {
			response["error"] = err.Error()
		}
		writeJSON(w, status, response)
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
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
