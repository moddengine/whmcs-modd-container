package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/config"
	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/service"
)

const maxBody = 1 << 20

type API struct {
	Manager   *service.Manager
	Config    config.Config
	Token     string
	Logger    *slog.Logger
	Version   string
	Commit    string
	BuildDate string
	DockerAPI string
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.health)
	mux.HandleFunc("GET /v1/info", a.info)
	mux.HandleFunc("GET /v1/image/versions", a.versions)
	mux.HandleFunc("GET /v1/log", a.log)
	mux.HandleFunc("GET /v1/services", a.list)
	mux.HandleFunc("PUT /v1/services/{id}", a.provision)
	mux.HandleFunc("GET /v1/services/{id}", a.get)
	mux.HandleFunc("POST /v1/services/{id}/suspend", a.suspend)
	mux.HandleFunc("POST /v1/services/{id}/resume", a.resume)
	mux.HandleFunc("POST /v1/services/{id}/terminate", a.terminate)
	mux.HandleFunc("DELETE /v1/services/{id}", a.delete)
	mux.HandleFunc("POST /v1/services/{id}/upgrade", a.upgrade)
	return a.requestMiddleware(a.authMiddleware(mux))
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) info(w http.ResponseWriter, r *http.Request) {
	tombstones, err := a.Manager.Repo.TombstoneCount()
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	services, err := a.Manager.Repo.List()
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	counts := map[string]int{"active": 0, "suspended": 0, "terminated": 0, "deleted": tombstones}
	for _, item := range services {
		counts[item.State]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": a.Version, "commit": a.Commit, "build_date": a.BuildDate,
		"docker_api_version": a.DockerAPI, "zfs_prefix": a.Config.ZFS.DatasetPrefix,
		"services_dir": a.Config.State.ServicesDir, "caddy_service_config_dir": a.Config.Caddy.ServiceConfigDir,
		"traffic_drain": a.Config.Deployment.TrafficDrain.String(), "metrics_provider": "mock",
		"service_counts": counts,
	})
}

func (a *API) versions(w http.ResponseWriter, r *http.Request) {
	versions, err := a.Manager.Docker.Versions(r.Context())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (a *API) log(w http.ResponseWriter, r *http.Request) {
	lines, err := Tail(a.Config.Logging.Path, 250, 1<<20)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "limit": 250})
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	services, err := a.Manager.List(r.Context())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	status, err := a.Manager.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) provision(w http.ResponseWriter, r *http.Request) {
	var request model.ProvisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	status, created, err := a.Manager.Provision(r.Context(), r.PathValue("id"), request, requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, status)
}

func (a *API) suspend(w http.ResponseWriter, r *http.Request) {
	a.action(w, r, a.Manager.Suspend)
}

func (a *API) resume(w http.ResponseWriter, r *http.Request) {
	a.action(w, r, a.Manager.Resume)
}

func (a *API) terminate(w http.ResponseWriter, r *http.Request) {
	a.action(w, r, a.Manager.Terminate)
}

func (a *API) action(w http.ResponseWriter, r *http.Request, fn func(context.Context, string, string) (*model.Status, error)) {
	status, err := fn(r.Context(), r.PathValue("id"), requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	if err := a.Manager.Delete(r.Context(), r.PathValue("id"), requestID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": model.Deleted})
}

func (a *API) upgrade(w http.ResponseWriter, r *http.Request) {
	var request model.UpgradeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	status, err := a.Manager.Upgrade(r.Context(), r.PathValue("id"), request, requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		valid := strings.HasPrefix(header, prefix) &&
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), []byte(a.Token)) == 1
		if !valid {
			writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized", "authentication required", requestID(r)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
		a.Logger.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *service.Error
	if !errors.As(err, &apiErr) {
		apiErr = &service.Error{Code: "internal_error", Status: 500, Err: err}
	}
	message := apiErr.Error()
	if apiErr.Status >= 500 {
		a.Logger.Error("request failed", "request_id", requestID(r), "error", err)
		message = "controller request failed"
	}
	writeJSON(w, apiErr.Status, errorBody(apiErr.Code, message, requestID(r)))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func errorBody(code, message, requestID string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": requestID}}
}

func requestID(r *http.Request) string { return r.Header.Get("X-Request-ID") }

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(bytes[:])
}

func Tail(path string, lineLimit, byteLimit int) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := max(int64(0), info.Size()-int64(byteLimit))
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(io.LimitReader(file, int64(byteLimit)))
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	return lines, nil
}
