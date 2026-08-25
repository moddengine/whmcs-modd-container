package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moddengine/whmcs-container-controller/internal/config"
	dockeradapter "github.com/moddengine/whmcs-container-controller/internal/docker"
	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/service"
	"github.com/moddengine/whmcs-container-controller/internal/state"
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
	pullMu    sync.RWMutex
	pull      imagePullStatus
}

type imagePullStatus struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.health)
	mux.HandleFunc("GET /v1/info", a.info)
	mux.HandleFunc("GET /v1/image/versions", a.versions)
	mux.HandleFunc("GET /v1/image/pull", a.pullImageStatus)
	mux.HandleFunc("POST /v1/image/pull", a.pullImage)
	mux.HandleFunc("GET /v1/services", a.list)
	mux.HandleFunc("PUT /v1/services/{id}", a.provision)
	mux.HandleFunc("GET /v1/services/{id}", a.get)
	mux.HandleFunc("POST /v1/services/{id}/suspend", a.suspend)
	mux.HandleFunc("POST /v1/services/{id}/resume", a.resume)
	mux.HandleFunc("POST /v1/services/{id}/terminate", a.terminate)
	mux.HandleFunc("POST /v1/services/{id}/dns/reconnect", a.reconnectDNS)
	mux.HandleFunc("DELETE /v1/services/{id}", a.delete)
	mux.HandleFunc("POST /v1/services/{id}/upgrade", a.upgrade)
	mux.HandleFunc("POST /v1/services/{id}/monitor-token", a.monitorToken)
	mux.HandleFunc("GET /v1/services/{id}/status/ws", a.statusWebSocket)
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

func (a *API) pullImage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	version := strings.TrimSpace(request.Version)
	if version != "" {
		if err := dockeradapter.ValidateVersion(version); err != nil {
			a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
			return
		}
	}
	queued := version
	if queued == "" {
		queued = "latest v*"
	}
	a.pullMu.Lock()
	if a.pull.Status == "pending" {
		a.pullMu.Unlock()
		a.writeError(w, r, &service.Error{Code: "image_pull_pending", Status: 409, Err: errors.New("an image pull is already pending")})
		return
	}
	a.pull = imagePullStatus{Status: "pending", Version: queued}
	a.pullMu.Unlock()
	requestID := requestID(r)
	go func() {
		pulled, err := a.Manager.Docker.Pull(context.Background(), version)
		if err != nil {
			a.setImagePullStatus(imagePullStatus{Status: "failed", Version: queued, Error: err.Error()})
			a.Logger.Error("image pull failed", "request_id", requestID, "version", version, "error", err)
			return
		}
		a.setImagePullStatus(imagePullStatus{Status: "completed", Version: pulled.Version})
		a.Logger.Info("image pull completed", "request_id", requestID, "version", pulled.Version)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "version": queued})
}

func (a *API) pullImageStatus(w http.ResponseWriter, _ *http.Request) {
	a.pullMu.RLock()
	status := a.pull
	a.pullMu.RUnlock()
	if status.Status == "" {
		status.Status = "idle"
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) setImagePullStatus(status imagePullStatus) {
	a.pullMu.Lock()
	a.pull = status
	a.pullMu.Unlock()
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
	status, accepted, err := a.Manager.Provision(r.Context(), r.PathValue("id"), request, requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, acceptedCode(accepted), map[string]any{"service": status})
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

func (a *API) reconnectDNS(w http.ResponseWriter, r *http.Request) {
	if err := a.Manager.ReconnectDNS(r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (a *API) action(w http.ResponseWriter, r *http.Request, fn func(context.Context, string, string) (*model.Status, bool, error)) {
	status, accepted, err := fn(r.Context(), r.PathValue("id"), requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, acceptedCode(accepted), map[string]any{"service": status})
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	tombstone, accepted, err := a.Manager.Delete(r.Context(), r.PathValue("id"), requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, acceptedCode(accepted), map[string]any{"service": tombstone})
}

func (a *API) upgrade(w http.ResponseWriter, r *http.Request) {
	var request model.UpgradeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	status, accepted, err := a.Manager.Upgrade(r.Context(), r.PathValue("id"), request, requestID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, acceptedCode(accepted), map[string]any{"service": status})
}

type monitorClaims struct {
	ServiceID string `json:"service_id"`
	Origin    string `json:"origin"`
	Expires   int64  `json:"exp"`
}

func (a *API) monitorToken(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Origin string `json:"origin"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	origin, err := validOrigin(request.Origin)
	if err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	id := r.PathValue("id")
	if err := service.ValidateServiceID(id); err != nil {
		a.writeError(w, r, &service.Error{Code: "invalid_request", Status: 400, Err: err})
		return
	}
	if _, err := a.Manager.Repo.Get(id); err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			a.writeError(w, r, err)
			return
		}
		if _, tombErr := a.Manager.Repo.GetTombstone(id); tombErr != nil {
			if !errors.Is(tombErr, state.ErrNotFound) {
				a.writeError(w, r, tombErr)
				return
			}
			a.writeError(w, r, &service.Error{Code: "not_found", Status: 404, Err: errors.New("service not found")})
			return
		}
	}
	expires := time.Now().UTC().Add(time.Hour)
	token, err := signMonitorToken(monitorClaims{ServiceID: id, Origin: origin, Expires: expires.Unix()}, a.Token)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires})
}

func (a *API) statusWebSocket(w http.ResponseWriter, r *http.Request) {
	protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
	if len(protocols) != 2 || strings.TrimSpace(protocols[0]) != "modd-monitor" {
		writeJSON(w, http.StatusUnauthorized, errorBody("invalid_monitor_token", "monitor token required", requestID(r)))
		return
	}
	claims, err := verifyMonitorToken(strings.TrimSpace(protocols[1]), a.Token)
	if err != nil || claims.ServiceID != r.PathValue("id") || claims.Origin != r.Header.Get("Origin") {
		writeJSON(w, http.StatusUnauthorized, errorBody("invalid_monitor_token", "monitor token is invalid or expired", requestID(r)))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"modd-monitor"}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(context.Background())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous []byte
	for {
		if time.Now().Unix() >= claims.Expires {
			_ = conn.Close(websocket.StatusPolicyViolation, "monitor token expired")
			return
		}
		snapshot, deleted, err := a.Manager.MonitorSnapshot(ctx, claims.ServiceID)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "status unavailable")
			return
		}
		current, _ := json.Marshal(snapshot.Service)
		if !hmac.Equal(previous, current) {
			if err := wsjson.Write(ctx, conn, snapshot); err != nil {
				return
			}
			previous = current
		}
		if deleted {
			_ = conn.Close(websocket.StatusNormalClosure, "deleted")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func signMonitorToken(claims monitorClaims, secret string) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyMonitorToken(token, secret string) (monitorClaims, error) {
	var claims monitorClaims
	body, signature, ok := strings.Cut(token, ".")
	if !ok {
		return claims, errors.New("malformed token")
	}
	want := hmac.New(sha256.New, []byte(secret))
	_, _ = want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return claims, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return claims, err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	if claims.ServiceID == "" || claims.Origin == "" || time.Now().Unix() >= claims.Expires {
		return claims, errors.New("expired token")
	}
	return claims, nil
}

func validOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("origin must be an HTTPS origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func acceptedCode(accepted bool) int {
	if accepted {
		return http.StatusAccepted
	}
	return http.StatusOK
}

func (a *API) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status/ws") {
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
