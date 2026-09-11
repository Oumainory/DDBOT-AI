package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Sora233/MiraiGo-Template/config"
	"github.com/cnxysoft/DDBOT-WSa/adapter"
	"github.com/cnxysoft/DDBOT-WSa/internal/adminapi"
	"github.com/cnxysoft/DDBOT-WSa/internal/auth"
	"github.com/cnxysoft/DDBOT-WSa/internal/buildinfo"
	"github.com/cnxysoft/DDBOT-WSa/internal/idempotency"
	"github.com/cnxysoft/DDBOT-WSa/internal/observation"
	"github.com/cnxysoft/DDBOT-WSa/internal/origin"
	"github.com/cnxysoft/DDBOT-WSa/internal/platformdb"
	"github.com/cnxysoft/DDBOT-WSa/internal/runtimeconfig"
	"github.com/cnxysoft/DDBOT-WSa/internal/secretstore"
	"github.com/cnxysoft/DDBOT-WSa/internal/session"
	"github.com/cnxysoft/DDBOT-WSa/internal/webui"
	"github.com/cnxysoft/DDBOT-WSa/lsp/concern"
	"github.com/cnxysoft/DDBOT-WSa/lsp/concern_type"
	"github.com/cnxysoft/DDBOT-WSa/lsp/mmsg"
	"github.com/cnxysoft/DDBOT-WSa/lsp/subscription"
	"github.com/ghodss/yaml"
	"github.com/sirupsen/logrus"
	"go.uber.org/atomic"
)

// 请求和响应结构体

type mockMsgCtx struct {
	groupCode int64
}

func (m *mockMsgCtx) TextSend(text string) interface{}  { return nil }
func (m *mockMsgCtx) TextReply(text string) interface{} { return nil }
func (m *mockMsgCtx) Reply(msg *mmsg.MSG) interface{}   { return nil }
func (m *mockMsgCtx) Send(msg *mmsg.MSG) interface{}    { return nil }
func (m *mockMsgCtx) NoPermissionReply() interface{}    { return nil }
func (m *mockMsgCtx) GetLog() *logrus.Entry             { return logrus.NewEntry(logrus.StandardLogger()) }
func (m *mockMsgCtx) GetTarget() mmsg.Target            { return mmsg.NewGroupTarget(m.groupCode) }
func (m *mockMsgCtx) GetSender() *adapter.SenderInfo {
	return &adapter.SenderInfo{UserID: 10000, Uin: 10000}
}

type AddSubRequest struct {
	Site      string      `json:"site"`
	ID        interface{} `json:"id"`
	Type      string      `json:"type"`
	GroupCode int64       `json:"groupCode"`
}

func parseIdToString(id interface{}) string {
	switch v := id.(type) {
	case json.Number:
		return v.String()
	case float64:
		return fmt.Sprintf("%.0f", v)
	case int, int32, int64:
		return fmt.Sprintf("%d", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

type RemoveSubRequest struct {
	Site      string      `json:"site"`
	ID        interface{} `json:"id"`
	Type      string      `json:"type"`
	GroupCode int64       `json:"groupCode"`
}

type GetSubConfigRequest struct {
	Site      string      `json:"site"`
	ID        interface{} `json:"id"`
	Type      string      `json:"type"`
	GroupCode int64       `json:"groupCode"`
}

type SetSubConfigRequest struct {
	Site      string                      `json:"site"`
	ID        interface{}                 `json:"id"`
	Type      string                      `json:"type"`
	GroupCode int64                       `json:"groupCode"`
	Config    *concern.GroupConcernConfig `json:"config"`
}

type ConfigUpdateRequest struct {
	Config map[string]interface{} `json:"config"`
}

type ConfigKeyUpdateRequest struct {
	Value interface{} `json:"value"`
}

type LogsRequest struct {
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Level  string `json:"level"`
}

type SendNotificationRequest struct {
	Target  string `json:"target"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

type SubInfo struct {
	ID        interface{} `json:"id"`
	Type      string      `json:"type"`
	Site      string      `json:"site"`
	GroupCode int64       `json:"groupCode"`
	Name      string      `json:"name"`
}

type ConfigInfo struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

type LogInfo struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type SystemStatus struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Disk   float64 `json:"disk"`
}

type NotificationInfo struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Target  string    `json:"target"`
	Message string    `json:"message"`
	Success bool      `json:"success"`
}

type Server struct {
	addr  string
	token string

	startedAt           time.Time
	online              *atomic.Bool
	httpServer          *http.Server
	platformStore       *platformdb.Store
	observationRecorder *observation.Recorder
	restoreObservation  func()
}

// PlatformConfig controls the opt-in Phase 1 platform foundation mounted next to
// the legacy /api/v1 server. Zero values deliberately resolve from the safe
// native defaults and environment variables, so legacy callers can keep using
// Start without changing their behavior.
type PlatformConfig struct {
	DatabasePath  string
	MasterKeyFile string
	RuntimeMode   runtimeconfig.RuntimeMode
	Origin        origin.Policy
	RequireOrigin bool
	Cookie        session.CookiePolicy
}

type SubSummary struct {
	Total  int            `json:"total"`
	BySite map[string]int `json:"bySite"`
}

type OneBotStatus struct {
	Online bool `json:"online"`
	Good   bool `json:"good"`
}

type Health struct {
	Ok        bool   `json:"ok"`
	UptimeSec int64  `json:"uptimeSec"`
	Version   string `json:"version"`
}

func Start(online *atomic.Bool, alive *atomic.Bool) (*Server, error) {
	return start(online, alive, nil)
}

// StartWithPlatform starts the legacy admin server and the narrow Phase 1 auth /
// secret-store / health surface. Platform initialization is intentionally fail-open for the
// Legacy Core: an unavailable platform database is reported by health/readiness
// and leaves /api/v1 running.
func StartWithPlatform(online *atomic.Bool, alive *atomic.Bool, platform PlatformConfig) (*Server, error) {
	return start(online, alive, &platform)
}

func start(online *atomic.Bool, alive *atomic.Bool, platform *PlatformConfig) (*Server, error) {
	enable := config.GlobalConfig.GetBool("admin.enable")
	if !enable {
		return nil, nil
	}
	if platform != nil {
		resolved := resolvePlatformConfig(*platform)
		platform = &resolved
	}
	addrConfig := config.GlobalConfig.GetString("admin.addr")
	if envAddr := strings.TrimSpace(os.Getenv("DDBOT_AI_HTTP_LISTEN")); envAddr != "" {
		addrConfig = envAddr
	}
	runtimeMode := runtimeconfig.ModeNative
	if platform != nil {
		runtimeMode = platform.RuntimeMode
		if runtimeMode == "" {
			runtimeMode = runtimeconfig.ModeNative
		}
		// The container image supplies DDBOT_AI_HTTP_LISTEN explicitly. When a
		// caller opts into Docker mode without that override, use the container
		// default instead of accidentally binding the host-loopback config value.
		if runtimeMode == runtimeconfig.ModeDocker && strings.TrimSpace(os.Getenv("DDBOT_AI_HTTP_LISTEN")) == "" {
			addrConfig = ""
		}
	}
	addr, err := runtimeconfig.HTTPListen(runtimeMode, addrConfig)
	if err != nil {
		return nil, err
	}
	token := config.GlobalConfig.GetString("admin.token")

	s := &Server{addr: addr, token: token, startedAt: time.Now(), online: online}

	mux := http.NewServeMux()
	if platform != nil {
		platformHTTP, store, err := newPlatformHTTP(*platform, online)
		if err != nil {
			return nil, err
		}
		s.platformStore = store
		s.observationRecorder = platformHTTP.observationRecorder
		s.restoreObservation = platformHTTP.restoreObservation
		mux.Handle("/api/v2/", platformHTTP.handler)
		mux.Handle("/healthz", platformHTTP.probe.Healthz())
		mux.Handle("/readyz", platformHTTP.probe.Readyz())
		// SPA fallback is registered last and is narrower than all legacy/API
		// routes. The webui handler itself also refuses API/probe paths.
		mux.Handle("/", webui.Handler())
		if platformHTTP.bootstrap.Created {
			// This is the only raw setup-token output boundary. Do not route it
			// through logrus, API responses, health, or error envelopes.
			fmt.Printf("DDBOT-AI setup token: %s (expires %s)\n", platformHTTP.bootstrap.Token, platformHTTP.bootstrap.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if platformHTTP.bootstrapErr != nil {
			logrus.WithError(platformHTTP.bootstrapErr).Warn("DDBOT-AI admin bootstrap is unavailable")
		}
	}
	// 基础 API
	mux.HandleFunc("/api/v1/health", s.withAuth(s.handleHealth))
	mux.HandleFunc("/api/v1/onebot/status", s.withAuth(s.handleOneBotStatus))
	mux.HandleFunc("/api/v1/subs/summary", s.withAuth(s.handleSubsSummary))

	// 订阅管理 API
	mux.HandleFunc("/api/v1/subs/list", s.withAuth(s.handleSubsList))
	mux.HandleFunc("/api/v1/subs/add", s.withAuth(s.handleAddSub))
	mux.HandleFunc("/api/v1/subs/remove", s.withAuth(s.handleRemoveSub))
	mux.HandleFunc("/api/v1/subs/detail/{id}", s.withAuth(s.handleSubDetail))
	mux.HandleFunc("/api/admin/sub/config", s.withAuth(s.handleSubConfig))

	// 配置管理 API
	mux.HandleFunc("/api/v1/config", s.withAuth(s.handleConfig))
	mux.HandleFunc("/api/v1/config/{key}", s.withAuth(s.handleConfigKey))
	mux.HandleFunc("/api/v1/config/reload", s.withAuth(s.handleConfigReload))

	// 日志管理 API
	mux.HandleFunc("/api/v1/logs", s.withAuth(s.handleLogs))
	mux.HandleFunc("/api/v1/logs/{level}", s.withAuth(s.handleLogsByLevel))
	mux.HandleFunc("/api/v1/logs/clear", s.withAuth(s.handleClearLogs))

	// 状态管理 API
	mux.HandleFunc("/api/v1/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("/api/v1/status/concerns", s.withAuth(s.handleConcernsStatus))
	mux.HandleFunc("/api/v1/status/system", s.withAuth(s.handleSystemStatus))

	// 通知管理 API
	mux.HandleFunc("/api/v1/notifications", s.withAuth(s.handleNotifications))
	mux.HandleFunc("/api/v1/notifications/send", s.withAuth(s.handleSendNotification))
	mux.HandleFunc("/api/v1/notifications/stats", s.withAuth(s.handleNotificationStats))

	// API 调试界面
	mux.HandleFunc("/api/debug", s.serveApiDebugger)
	mux.HandleFunc("/api/debug/", s.serveApiDebuggerAssets)

	server := &http.Server{Addr: addr, Handler: mux}
	s.httpServer = server
	go func() {
		_ = server.ListenAndServe()
	}()
	return s, nil
}

type platformHTTP struct {
	handler             http.Handler
	probe               platformdb.Probe
	secretStore         *secretstore.Service
	bootstrap           auth.BootstrapResult
	bootstrapErr        error
	observationRecorder *observation.Recorder
	restoreObservation  func()
	domainRepository    *platformdb.DomainRepository
}

func newPlatformHTTP(platform PlatformConfig, online *atomic.Bool) (platformHTTP, *platformdb.Store, error) {
	databasePath := strings.TrimSpace(platform.DatabasePath)
	if databasePath == "" {
		databasePath = strings.TrimSpace(os.Getenv("DDBOT_AI_PLATFORM_DB"))
	}
	if databasePath == "" {
		databasePath = "ddbot-ai.sqlite"
	}
	store, openErr := platformdb.Open(context.Background(), platformdb.Config{Path: databasePath})
	var secretRepository *platformdb.SecretRepository
	if store != nil {
		secretRepository = platformdb.NewSecretRepository(store)
	}
	secretService := secretstore.New(context.Background(), secretRepository, secretstore.Config{MasterKeyFile: platform.MasterKeyFile})
	probe := platformdb.NewProbeWithAuthAndSecret(store, openErr, secretService)
	var repository *platformdb.AuthRepository
	if store != nil {
		repository = platformdb.NewAuthRepository(store)
	}
	var observationRepository *platformdb.ObservationRepository
	var recorder *observation.Recorder
	var domainRepository *platformdb.DomainRepository
	if store != nil {
		observationRepository = platformdb.NewObservationRepository(store)
		recorder = observation.NewRecorder(observationRepository, observation.Config{})
		domainRepository = platformdb.NewDomainRepository(store)
	}
	legacySubscriptions := subscription.NewService()
	authService := auth.NewService(repository, auth.Config{})
	apiServer, err := adminapi.NewServer(adminapi.Config{
		Auth:                  authService,
		Probe:                 &probe,
		Origin:                platform.Origin,
		RequireOrigin:         platform.RequireOrigin,
		Cookie:                platform.Cookie,
		LegacyOnline:          online,
		Build:                 buildinfo.Current(),
		ObservationRepository: observationRepository,
		ObservationRecorder:   recorder,
		DomainRepository:      domainRepository,
		LegacySubscriptions:   legacySubscriptions,
		Idempotency:           idempotency.NewMemoryStore(idempotency.DefaultRetention),
	})
	if err != nil {
		if recorder != nil {
			_ = recorder.Close(context.Background())
		}
		if store != nil {
			_ = store.Close()
		}
		return platformHTTP{}, nil, err
	}
	result, bootstrapErr := authService.Bootstrap(context.Background())
	if openErr != nil {
		// Keep the detailed owner error in the process log boundary while the
		// probe and HTTP error envelope expose only stable non-sensitive codes.
		bootstrapErr = openErr
	} else if bootstrapErr != nil {
		// Bootstrap errors are represented by the stable auth error in the
		// endpoint layer; detailed database state remains out of health JSON.
		bootstrapErr = auth.ErrUnavailable
	}
	var restoreObservation func()
	if recorder != nil {
		restoreObservation = observation.Install(recorder)
	}
	if domainRepository != nil {
		// Projection reconciliation is best-effort and asynchronous. It never
		// blocks Legacy startup or changes BuntDB authority.
		go func() {
			records, snapshotErr := legacySubscriptions.Snapshot(context.Background())
			if snapshotErr != nil {
				logrus.WithError(snapshotErr).Warn("DDBOT-AI domain projection snapshot unavailable")
				return
			}
			if rebuildErr := domainRepository.RebuildProjection(context.Background(), records); rebuildErr != nil {
				logrus.WithError(rebuildErr).Warn("DDBOT-AI domain projection reconciliation degraded")
			}
		}()
	}
	return platformHTTP{
		handler:             apiServer.Handler(),
		probe:               probe,
		secretStore:         secretService,
		bootstrap:           result,
		bootstrapErr:        bootstrapErr,
		observationRecorder: recorder,
		restoreObservation:  restoreObservation,
		domainRepository:    domainRepository,
	}, store, nil
}

func resolvePlatformConfig(platform PlatformConfig) PlatformConfig {
	if platform.RuntimeMode == "" {
		if configured := strings.TrimSpace(os.Getenv("DDBOT_AI_RUNTIME_MODE")); configured != "" {
			platform.RuntimeMode = runtimeconfig.RuntimeMode(configured)
		}
	}
	if strings.TrimSpace(platform.Origin.AllowedOrigin) == "" {
		platform.Origin.AllowedOrigin = strings.TrimSpace(os.Getenv("DDBOT_AI_ALLOWED_ORIGIN"))
	}
	if !platform.Origin.TrustForwarded {
		platform.Origin.TrustForwarded = envBool("DDBOT_AI_TRUST_FORWARDED")
	}
	if !platform.RequireOrigin {
		platform.RequireOrigin = envBool("DDBOT_AI_REQUIRE_ORIGIN")
	}
	if !platform.Cookie.Secure {
		platform.Cookie.Secure = envBool("DDBOT_AI_COOKIE_SECURE")
	}
	if strings.TrimSpace(platform.MasterKeyFile) == "" {
		platform.MasterKeyFile = strings.TrimSpace(os.Getenv("DDBOT_AI_MASTER_KEY_FILE"))
	}
	return platform
}

func envBool(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

// Close is primarily useful to embedders and tests. It never changes the
// legacy subscription store; it only shuts down this HTTP listener and the
// optional platform owner created by StartWithPlatform.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.httpServer != nil {
		if err := s.httpServer.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			firstErr = err
		}
	}
	if s.observationRecorder != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := s.observationRecorder.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		cancel()
	}
	if s.restoreObservation != nil {
		s.restoreObservation()
		s.restoreObservation = nil
	}
	if s.platformStore != nil {
		if err := s.platformStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 添加 CORS 头
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if s.token != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) != s.token {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	uptime := time.Since(s.startedAt).Seconds()
	_ = json.NewEncoder(w).Encode(Health{Ok: true, UptimeSec: int64(uptime), Version: ""})
}

func (s *Server) handleOneBotStatus(w http.ResponseWriter, _ *http.Request) {
	online := false
	if s.online != nil {
		online = s.online.Load()
	}
	_ = json.NewEncoder(w).Encode(OneBotStatus{Online: online, Good: online})
}

func (s *Server) handleSubsSummary(w http.ResponseWriter, _ *http.Request) {
	bySite := map[string]int{}
	total := 0
	for _, c := range concern.ListConcern() {
		sm := c.GetStateManager()
		_, ids, ctypes, err := sm.ListConcernState(func(groupCode int64, id interface{}, p concern_type.Type) bool { return true })
		if err != nil {
			continue
		}
		ids, ctypes, _ = sm.GroupTypeById(ids, ctypes)
		cnt := len(ids)
		bySite[c.Site()] = cnt
		total += cnt
	}
	_ = json.NewEncoder(w).Encode(SubSummary{Total: total, BySite: bySite})
}

// 订阅管理 API 处理函数

func (s *Server) handleSubsList(w http.ResponseWriter, r *http.Request) {
	var subs []SubInfo
	for _, c := range concern.ListConcern() {
		sm := c.GetStateManager()
		groupCodes, ids, ctypes, err := sm.ListConcernState(func(groupCode int64, id interface{}, p concern_type.Type) bool { return true })
		if err != nil {
			continue
		}
		for i := range ids {
			if i < len(groupCodes) && i < len(ctypes) {
				name := ""
				if info, err := c.Get(ids[i]); err == nil && info != nil {
					name = info.GetName()
				}
				subs = append(subs, SubInfo{
					ID:        ids[i],
					Type:      ctypes[i].String(),
					Site:      c.Site(),
					GroupCode: groupCodes[i],
					Name:      name,
				})
			}
		}
	}
	_ = json.NewEncoder(w).Encode(subs)
}

func (s *Server) handleAddSub(w http.ResponseWriter, r *http.Request) {
	var req AddSubRequest
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	ctx := &mockMsgCtx{groupCode: req.GroupCode}
	_, err := subscription.NewService().SubscribeWithMessageContext(ctx, subscription.Request{
		Site: req.Site, ID: parseIdToString(req.ID), Type: req.Type, GroupCode: req.GroupCode,
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (s *Server) handleRemoveSub(w http.ResponseWriter, r *http.Request) {
	var req RemoveSubRequest
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	ctx := &mockMsgCtx{groupCode: req.GroupCode}
	_, err := subscription.NewService().UnsubscribeWithMessageContext(ctx, subscription.Request{
		Site: req.Site, ID: parseIdToString(req.ID), Type: req.Type, GroupCode: req.GroupCode,
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (s *Server) handleSubDetail(w http.ResponseWriter, r *http.Request) {
	// 从 URL 中获取 ID
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/subs/detail/")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid ID"})
		return
	}

	// 查找订阅
	for _, c := range concern.ListConcern() {
		sm := c.GetStateManager()
		groupCodes, ids, ctypes, err := sm.ListConcernState(func(groupCode int64, id interface{}, p concern_type.Type) bool { return true })
		if err != nil {
			continue
		}
		for i := range ids {
			if fmt.Sprintf("%v", ids[i]) == id {
				if i < len(groupCodes) && i < len(ctypes) {
					_ = json.NewEncoder(w).Encode(SubInfo{
						ID:        ids[i],
						Type:      ctypes[i].String(),
						Site:      c.Site(),
						GroupCode: groupCodes[i],
					})
					return
				}
			}
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "Subscription not found"})
}

func (s *Server) handleSubConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		site := r.URL.Query().Get("site")
		id := r.URL.Query().Get("id")
		groupCodeStr := r.URL.Query().Get("groupCode")

		var targetConcern concern.Concern
		for _, c := range concern.ListConcern() {
			if c.Site() == site {
				targetConcern = c
				break
			}
		}

		if targetConcern == nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid site"})
			return
		}

		parsedId, err := targetConcern.ParseId(id)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid ID format: " + err.Error()})
			return
		}

		var groupCode int64
		fmt.Sscanf(groupCodeStr, "%d", &groupCode)

		sm := targetConcern.GetStateManager()
		cfg := sm.GetGroupConcernConfig(groupCode, parsedId)

		_ = json.NewEncoder(w).Encode(cfg)
		return
	}

	if r.Method == "POST" {
		var req SetSubConfigRequest
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request: " + err.Error()})
			return
		}

		var targetConcern concern.Concern
		for _, c := range concern.ListConcern() {
			if c.Site() == req.Site {
				targetConcern = c
				break
			}
		}

		if targetConcern == nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid site"})
			return
		}

		parsedId, err := targetConcern.ParseId(parseIdToString(req.ID))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid ID format: " + err.Error()})
			return
		}

		sm := targetConcern.GetStateManager()
		err = sm.OperateGroupConcernConfig(req.GroupCode, parsedId, req.Config, func(IConfig concern.IConfig) bool {
			return true
		})

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
}

// 配置管理 API 处理函数

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 获取完整配置
		data, err := os.ReadFile("application.yaml")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read config file"})
			return
		}
		var config map[string]interface{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse config file"})
			return
		}
		_ = json.NewEncoder(w).Encode(config)

	case http.MethodPost:
		// 更新配置
		var req ConfigUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
			return
		}

		data, err := yaml.Marshal(req.Config)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to marshal config"})
			return
		}

		if err := os.WriteFile("application.yaml", data, 0644); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write config file"})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleConfigKey(w http.ResponseWriter, r *http.Request) {
	// 从 URL 中获取配置键
	key := strings.TrimPrefix(r.URL.Path, "/api/v1/config/")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid config key"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		// 获取特定配置项
		value := config.GlobalConfig.Get(key)
		_ = json.NewEncoder(w).Encode(ConfigInfo{Key: key, Value: value})

	case http.MethodPost:
		// 更新特定配置项
		var req ConfigKeyUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
			return
		}

		// 读取当前配置
		data, err := os.ReadFile("application.yaml")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read config file"})
			return
		}

		var config map[string]interface{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse config file"})
			return
		}

		// 更新配置项
		// 处理嵌套键，如 "admin.enable"
		keys := strings.Split(key, ".")
		current := config
		for i, k := range keys {
			if i == len(keys)-1 {
				current[k] = req.Value
			} else {
				if _, ok := current[k]; !ok {
					current[k] = make(map[string]interface{})
				}
				if nested, ok := current[k].(map[string]interface{}); ok {
					current = nested
				} else {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid config key path"})
					return
				}
			}
		}

		// 写回配置文件
		data, err = yaml.Marshal(config)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to marshal config"})
			return
		}

		if err := os.WriteFile("application.yaml", data, 0644); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write config file"})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	// 重新加载配置
	// 注意：这里需要根据实际的配置加载机制来实现
	// 由于 config.GlobalConfig 是全局的，我们可以尝试重新读取配置文件

	data, err := os.ReadFile("application.yaml")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read config file"})
		return
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse config file"})
		return
	}

	// 这里可以添加重新加载配置的逻辑
	// 由于具体的配置加载机制可能不同，这里只是返回成功
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// 日志管理 API 处理函数

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	// 获取日志目录
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		logDir = "."
	}

	// 查找最新的日志文件
	var latestLogFile string
	var latestModTime time.Time

	entries, err := os.ReadDir(logDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read log directory"})
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			fileInfo, err := entry.Info()
			if err != nil {
				continue
			}
			if fileInfo.ModTime().After(latestModTime) {
				latestModTime = fileInfo.ModTime()
				latestLogFile = filepath.Join(logDir, entry.Name())
			}
		}
	}

	if latestLogFile == "" {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "No log files found"})
		return
	}

	// 读取日志文件
	data, err := os.ReadFile(latestLogFile)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read log file"})
		return
	}

	// 解析日志
	lines := strings.Split(string(data), "\n")
	var logs []LogInfo

	// 简单的日志解析，假设日志格式为：时间 级别 消息
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) >= 3 {
			logs = append(logs, LogInfo{
				Time:    parts[0],
				Level:   parts[1],
				Message: parts[2],
			})
		} else {
			logs = append(logs, LogInfo{
				Time:    time.Now().Format("2006-01-02 15:04:05"),
				Level:   "INFO",
				Message: line,
			})
		}
	}

	// 应用分页
	limit := 100
	offset := 0

	if r.URL.Query().Get("limit") != "" {
		fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &limit)
	}

	if r.URL.Query().Get("offset") != "" {
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
	}

	if offset < 0 {
		offset = 0
	}

	if limit <= 0 {
		limit = 100
	}

	end := offset + limit
	if end > len(logs) {
		end = len(logs)
	}

	if offset >= len(logs) {
		_ = json.NewEncoder(w).Encode([]LogInfo{})
		return
	}

	_ = json.NewEncoder(w).Encode(logs[offset:end])
}

func (s *Server) handleLogsByLevel(w http.ResponseWriter, r *http.Request) {
	// 从 URL 中获取日志级别
	level := strings.TrimPrefix(r.URL.Path, "/api/v1/logs/")
	if level == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid log level"})
		return
	}

	// 获取日志目录
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		logDir = "."
	}

	// 查找最新的日志文件
	var latestLogFile string
	var latestModTime time.Time

	entries, err := os.ReadDir(logDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read log directory"})
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			fileInfo, err := entry.Info()
			if err != nil {
				continue
			}
			if fileInfo.ModTime().After(latestModTime) {
				latestModTime = fileInfo.ModTime()
				latestLogFile = filepath.Join(logDir, entry.Name())
			}
		}
	}

	if latestLogFile == "" {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "No log files found"})
		return
	}

	// 读取日志文件
	data, err := os.ReadFile(latestLogFile)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read log file"})
		return
	}

	// 解析日志
	lines := strings.Split(string(data), "\n")
	var logs []LogInfo

	// 简单的日志解析，假设日志格式为：时间 级别 消息
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) >= 3 && strings.EqualFold(parts[1], level) {
			logs = append(logs, LogInfo{
				Time:    parts[0],
				Level:   parts[1],
				Message: parts[2],
			})
		}
	}

	_ = json.NewEncoder(w).Encode(logs)
}

func (s *Server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	// 获取日志目录
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		logDir = "."
	}

	// 清理日志文件
	entries, err := os.ReadDir(logDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read log directory"})
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			filePath := filepath.Join(logDir, entry.Name())
			if err := os.Truncate(filePath, 0); err != nil {
				// 忽略错误，继续清理其他日志文件
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// 状态管理 API 处理函数

type DetailedStatus struct {
	Online              bool            `json:"online"`
	Uptime              time.Duration   `json:"uptime"`
	Version             string          `json:"version"`
	Subscriptions       int             `json:"subscriptions"`
	SubscriptionsBySite map[string]int  `json:"subscriptionsBySite"`
	ConcernsStatus      map[string]bool `json:"concernsStatus"`
	System              SystemStatus    `json:"system"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// 计算运行时间
	uptime := time.Since(s.startedAt)

	// 获取订阅信息
	subscriptions := 0
	subscriptionsBySite := make(map[string]int)
	for _, c := range concern.ListConcern() {
		sm := c.GetStateManager()
		_, ids, ctypes, err := sm.ListConcernState(func(groupCode int64, id interface{}, p concern_type.Type) bool { return true })
		if err != nil {
			continue
		}
		ids, ctypes, _ = sm.GroupTypeById(ids, ctypes)
		cnt := len(ids)
		subscriptionsBySite[c.Site()] = cnt
		subscriptions += cnt
	}

	// 获取各站点关注状态
	concernsStatus := make(map[string]bool)
	for _, c := range concern.ListConcern() {
		concernsStatus[c.Site()] = true // 简单起见，假设所有站点都正常
	}

	// 获取系统状态
	systemStatus := s.getSystemStatus()

	// 构建详细状态
	status := DetailedStatus{
		Online:              s.online.Load(),
		Uptime:              uptime,
		Version:             "", // 版本信息需要从其他地方获取
		Subscriptions:       subscriptions,
		SubscriptionsBySite: subscriptionsBySite,
		ConcernsStatus:      concernsStatus,
		System:              systemStatus,
	}

	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleConcernsStatus(w http.ResponseWriter, r *http.Request) {
	// 获取各站点关注状态
	concernsStatus := make(map[string]map[string]interface{})

	for _, c := range concern.ListConcern() {
		sm := c.GetStateManager()
		_, ids, ctypes, err := sm.ListConcernState(func(groupCode int64, id interface{}, p concern_type.Type) bool { return true })
		if err != nil {
			concernsStatus[c.Site()] = map[string]interface{}{
				"online": false,
				"error":  err.Error(),
			}
			continue
		}

		// 统计各类型的订阅数量
		typeCount := make(map[string]int)
		for _, t := range ctypes {
			typeCount[t.String()]++
		}

		concernsStatus[c.Site()] = map[string]interface{}{
			"online":        true,
			"subscriptions": len(ids),
			"types":         typeCount,
		}
	}

	_ = json.NewEncoder(w).Encode(concernsStatus)
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	// 获取系统状态
	systemStatus := s.getSystemStatus()
	_ = json.NewEncoder(w).Encode(systemStatus)
}

func (s *Server) getSystemStatus() SystemStatus {
	// 简单实现，返回模拟数据
	// 实际项目中可以使用系统监控库获取真实数据
	return SystemStatus{
		CPU:    25.5, // 25.5%
		Memory: 45.2, // 45.2%
		Disk:   60.7, // 60.7%
	}
}

// 通知管理 API 处理函数

type NotificationStats struct {
	Total          int                `json:"total"`
	Success        int                `json:"success"`
	Failed         int                `json:"failed"`
	ByType         map[string]int     `json:"byType"`
	ByTarget       map[string]int     `json:"byTarget"`
	RecentFailures []NotificationInfo `json:"recentFailures"`
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	// 模拟通知历史
	// 实际项目中，应该从数据库或日志中获取真实的通知历史
	var notifications []NotificationInfo

	// 添加一些模拟数据
	notifications = append(notifications, NotificationInfo{
		ID:      "1",
		Time:    time.Now().Add(-time.Hour),
		Type:    "live",
		Target:  "group:123456",
		Message: "测试直播通知",
		Success: true,
	})

	notifications = append(notifications, NotificationInfo{
		ID:      "2",
		Time:    time.Now().Add(-2 * time.Hour),
		Type:    "news",
		Target:  "group:123456",
		Message: "测试新闻通知",
		Success: true,
	})

	notifications = append(notifications, NotificationInfo{
		ID:      "3",
		Time:    time.Now().Add(-3 * time.Hour),
		Type:    "live",
		Target:  "group:789012",
		Message: "测试直播通知 2",
		Success: false,
	})

	_ = json.NewEncoder(w).Encode(notifications)
}

func (s *Server) handleSendNotification(w http.ResponseWriter, r *http.Request) {
	var req SendNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	// 模拟发送通知
	// 实际项目中，应该调用真实的通知发送函数
	fmt.Printf("发送测试通知: Target=%s, Type=%s, Message=%s\n", req.Target, req.Type, req.Message)

	// 模拟发送成功
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "测试通知发送成功"})
}

func (s *Server) handleNotificationStats(w http.ResponseWriter, r *http.Request) {
	// 模拟通知统计信息
	// 实际项目中，应该从数据库或日志中获取真实的统计信息
	stats := NotificationStats{
		Total:   100,
		Success: 95,
		Failed:  5,
		ByType: map[string]int{
			"live":  60,
			"news":  30,
			"other": 10,
		},
		ByTarget: map[string]int{
			"group:123456": 70,
			"group:789012": 30,
		},
		RecentFailures: []NotificationInfo{
			{
				ID:      "3",
				Time:    time.Now().Add(-3 * time.Hour),
				Type:    "live",
				Target:  "group:789012",
				Message: "测试直播通知 2",
				Success: false,
			},
		},
	}

	_ = json.NewEncoder(w).Encode(stats)
}

// API 调试界面处理函数

func (s *Server) serveApiDebugger(w http.ResponseWriter, r *http.Request) {
	// 设置CORS头以便调试
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 返回API调试界面的HTML
	html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>DDBOT-WSa API 调试工具</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
            color: #333;
        }
        .container { 
            max-width: 1200px; 
            margin: 0 auto; 
            background: white; 
            border-radius: 12px; 
            box-shadow: 0 10px 30px rgba(0,0,0,0.2);
            overflow: hidden;
        }
        .header { 
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white; 
            padding: 30px; 
            text-align: center; 
        }
        .header h1 { font-size: 28px; margin-bottom: 8px; }
        .header p { opacity: 0.9; font-size: 16px; }
        
        .main-content { 
            display: grid; 
            grid-template-columns: 1fr 1fr; 
            gap: 20px; 
            padding: 30px; 
        }
        
        .panel { 
            background: #f8f9fa; 
            border-radius: 10px; 
            padding: 25px; 
            box-shadow: 0 2px 10px rgba(0,0,0,0.05);
        }
        .panel h2 { 
            color: #495057; 
            margin-bottom: 20px; 
            padding-bottom: 10px; 
            border-bottom: 2px solid #dee2e6; 
        }
        
        .form-group { margin-bottom: 20px; }
        .form-group label { 
            display: block; 
            margin-bottom: 8px; 
            font-weight: 600; 
            color: #495057; 
        }
        .form-control { 
            width: 100%; 
            padding: 12px; 
            border: 2px solid #e9ecef; 
            border-radius: 6px; 
            font-size: 14px; 
            transition: border-color 0.2s; 
        }
        .form-control:focus { 
            outline: none; 
            border-color: #667eea; 
            box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.1); 
        }
        
        .form-row { 
            display: grid; 
            grid-template-columns: 1fr 1fr; 
            gap: 15px; 
        }
        
        select.form-control { 
            appearance: none; 
            background-image: url("data:image/svg+xml;charset=UTF-8,%3csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3e%3cpolyline points='6 9 12 15 18 9'%3e%3c/polyline%3e%3c/svg%3e");
            background-repeat: no-repeat;
            background-position: right 12px center;
            background-size: 16px;
            padding-right: 40px;
        }
        
        textarea.form-control { 
            min-height: 150px; 
            resize: vertical; 
            font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace; 
        }
        
        .btn { 
            width: 100%; 
            padding: 14px; 
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white; 
            border: none; 
            border-radius: 6px; 
            font-size: 16px; 
            font-weight: 600; 
            cursor: pointer; 
            transition: all 0.2s; 
        }
        .btn:hover:not(:disabled) { 
            transform: translateY(-2px); 
            box-shadow: 0 5px 15px rgba(102, 126, 234, 0.4); 
        }
        .btn:disabled { 
            opacity: 0.6; 
            cursor: not-allowed; 
        }
        
        .response-info { 
            display: flex; 
            justify-content: space-between; 
            margin-bottom: 20px; 
            padding: 15px; 
            background: white; 
            border-radius: 6px; 
            border-left: 4px solid #667eea; 
        }
        
        .status-badge { 
            padding: 6px 12px; 
            border-radius: 20px; 
            font-size: 14px; 
            font-weight: 600; 
        }
        .status-success { background: #d4edda; color: #155724; }
        .status-error { background: #f8d7da; color: #721c24; }
        
        .tabs { 
            display: flex; 
            margin-bottom: 20px; 
            border-bottom: 1px solid #dee2e6; 
        }
        
        .tab-btn { 
            padding: 12px 20px; 
            background: none; 
            border: none; 
            color: #6c757d; 
            cursor: pointer; 
            border-bottom: 3px solid transparent; 
            transition: all 0.2s; 
            font-weight: 500; 
        }
        .tab-btn.active { 
            color: #667eea; 
            border-bottom-color: #667eea; 
        }
        
        .response-content { 
            background: white; 
            border-radius: 6px; 
            padding: 20px; 
            min-height: 250px; 
            max-height: 400px; 
            overflow-y: auto; 
            font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace; 
            font-size: 13px; 
            line-height: 1.5; 
        }
        
        .response-content pre { 
            margin: 0; 
            white-space: pre-wrap; 
            word-break: break-all; 
        }
        
        .empty-state { 
            text-align: center; 
            padding: 40px; 
            color: #6c757d; 
        }
        
        .api-presets { 
            margin-top: 15px; 
        }
        .preset-btn { 
            display: inline-block;
            margin: 5px 5px 5px 0;
            padding: 8px 12px;
            background: #e9ecef;
            border: none;
            border-radius: 4px;
            font-size: 12px;
            cursor: pointer;
            transition: all 0.2s;
        }
        .preset-btn:hover { 
            background: #667eea; 
            color: white; 
        }
        
        @media (max-width: 768px) {
            .main-content { 
                grid-template-columns: 1fr; 
            }
            .form-row { 
                grid-template-columns: 1fr; 
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🤖 DDBOT-WSa API 调试工具</h1>
            <p>轻松测试和调试你的机器人API接口</p>
        </div>
        
        <div class="main-content">
            <div class="panel">
                <h2>📤 请求配置</h2>
                
                <div class="form-group">
                    <label>基础URL:</label>
                    <input type="text" id="baseUrl" class="form-control" value="http://` + strings.TrimPrefix(s.addr, "http://") + `" placeholder="http://localhost:15631">
                </div>
                
                <div class="form-row">
                    <div class="form-group">
                        <label>方法:</label>
                        <select id="method" class="form-control">
                            <option value="GET">GET</option>
                            <option value="POST">POST</option>
                            <option value="PUT">PUT</option>
                            <option value="DELETE">DELETE</option>
                        </select>
                    </div>
                    
                    <div class="form-group">
                        <label>Token:</label>
                        <input type="password" id="token" class="form-control" placeholder="Bearer token">
                    </div>
                </div>
                
                <div class="form-group">
                    <label>API路径:</label>
                    <input type="text" id="apiPath" class="form-control" value="/api/v1/health" placeholder="/api/v1/...">
                    <div class="api-presets">
                        <small>常用API:</small><br>
                        <button class="preset-btn" onclick="setApi('/api/v1/health')">健康检查</button>
                        <button class="preset-btn" onclick="setApi('/api/v1/onebot/status')">OneBot状态</button>
                        <button class="preset-btn" onclick="setApi('/api/v1/subs/summary')">订阅汇总</button>
                        <button class="preset-btn" onclick="setApi('/api/v1/subs/list')">订阅列表</button>
                        <button class="preset-btn" onclick="setApi('/api/v1/status')">系统状态</button>
                        <button class="preset-btn" onclick="setApi('/api/v1/config')">完整配置</button>
                    </div>
                </div>
                
                <div class="form-group" id="requestBodyGroup" style="display: none;">
                    <label>请求体 (JSON):</label>
                    <textarea id="requestBody" class="form-control" placeholder='{\n  "site": "bilibili",\n  "id": "123456",\n  "type": "live",\n  "groupCode": 123456789\n}'></textarea>
                </div>
                
                <button id="sendBtn" class="btn" onclick="sendRequest()">发送请求</button>
            </div>
            
            <div class="panel">
                <h2>📥 响应结果</h2>
                
                <div class="response-info">
                    <span id="statusBadge" class="status-badge">等待请求...</span>
                    <span id="responseTime">-</span>
                </div>
                
                <div class="tabs">
                    <button class="tab-btn active" onclick="switchTab('headers')">响应头</button>
                    <button class="tab-btn" onclick="switchTab('body')">响应体</button>
                </div>
                
                <div id="headersTab" class="response-content">
                    <div class="empty-state">暂无响应头信息</div>
                </div>
                
                <div id="bodyTab" class="response-content" style="display: none;">
                    <div class="empty-state">暂无响应内容</div>
                </div>
            </div>
        </div>
    </div>

    <script>
        // 全局变量
        let currentTab = 'body';
        let loading = false;
        
        // 方法改变时更新UI
        document.getElementById('method').addEventListener('change', function() {
            const requestBodyGroup = document.getElementById('requestBodyGroup');
            requestBodyGroup.style.display = this.value === 'GET' ? 'none' : 'block';
        });
        
        // 设置API路径
        function setApi(path) {
            document.getElementById('apiPath').value = path;
            // 根据API路径预设请求体
            updateRequestBody(path);
        }
        
        // 更新请求体模板
        function updateRequestBody(path) {
            const requestBody = document.getElementById('requestBody');
            switch(path) {
                case '/api/v1/subs/add':
                case '/api/v1/subs/remove':
                    requestBody.value = JSON.stringify({
                        "site": "bilibili",
                        "id": "123456",
                        "type": "live",
                        "groupCode": 123456789
                    }, null, 2);
                    break;
                case '/api/v1/config/admin.enable':
                    requestBody.value = JSON.stringify({
                        "value": true
                    }, null, 2);
                    break;
                case '/api/v1/notifications/send':
                    requestBody.value = JSON.stringify({
                        "target": "group:123456",
                        "message": "测试消息",
                        "type": "live"
                    }, null, 2);
                    break;
                default:
                    requestBody.value = '';
            }
            
            // 显示请求体输入框
            if (requestBody.value) {
                document.getElementById('requestBodyGroup').style.display = 'block';
                document.getElementById('method').value = 'POST';
            }
        }
        
        // 切换标签页
        function switchTab(tab) {
            currentTab = tab;
            
            // 更新按钮状态
            document.querySelectorAll('.tab-btn').forEach(btn => {
                btn.classList.remove('active');
            });
            event.target.classList.add('active');
            
            // 显示对应内容
            document.getElementById('headersTab').style.display = tab === 'headers' ? 'block' : 'none';
            document.getElementById('bodyTab').style.display = tab === 'body' ? 'block' : 'none';
        }
        
        // 发送请求
        async function sendRequest() {
            if (loading) return;
            
            const baseUrl = document.getElementById('baseUrl').value;
            const method = document.getElementById('method').value;
            const token = document.getElementById('token').value;
            const apiPath = document.getElementById('apiPath').value;
            const requestBody = document.getElementById('requestBody').value;
            
            if (!apiPath) {
                alert('请输入API路径');
                return;
            }
            
            loading = true;
            const sendBtn = document.getElementById('sendBtn');
            sendBtn.textContent = '发送中...';
            sendBtn.disabled = true;
            
            const startTime = Date.now();
            
            try {
                // 智能URL拼接，避免重复的/api/
                let url = baseUrl;
                if (!baseUrl.endsWith('/') && !apiPath.startsWith('/')) {
                    url += '/';
                }
                // 如果apiPath已经包含/api/前缀，则直接使用
                if (apiPath.startsWith('/api/')) {
                    url = baseUrl.replace(/\/api\/.*$/, '') + apiPath;
                } else {
                    url += apiPath;
                }
                const headers = {
                    'Content-Type': 'application/json'
                };
                
                if (token) {
                    headers['Authorization'] = token.startsWith('Bearer ') ? token : 'Bearer ' + token;
                }
                
                const options = {
                    method: method,
                    headers: headers
                };
                
                // 添加请求体
                if (method !== 'GET' && requestBody.trim()) {
                    try {
                        JSON.parse(requestBody);
                        options.body = requestBody;
                    } catch (e) {
                        throw new Error('请求体不是有效的JSON格式');
                    }
                }
                
                const response = await fetch(url, options);
                const endTime = Date.now();
                
                // 更新状态显示
                const statusBadge = document.getElementById('statusBadge');
                const responseTime = document.getElementById('responseTime');
                
                statusBadge.textContent = response.status + ' ' + response.statusText;
                statusBadge.className = 'status-badge ' + (response.status < 400 ? 'status-success' : 'status-error');
                responseTime.textContent = '耗时: ' + (endTime - startTime) + 'ms';
                
                // 处理响应头
                let headersText = '';
                for (let [key, value] of response.headers.entries()) {
                    headersText += key + ': ' + value + '\n';
                }
                
                document.getElementById('headersTab').innerHTML = 
                    headersText ? '<pre>' + headersText + '</pre>' : '<div class="empty-state">暂无响应头信息</div>';
                
                // 处理响应体
                const contentType = response.headers.get('content-type');
                let bodyText = '';
                
                if (contentType && contentType.includes('application/json')) {
                    const data = await response.json();
                    bodyText = JSON.stringify(data, null, 2);
                } else {
                    bodyText = await response.text();
                }
                
                document.getElementById('bodyTab').innerHTML = 
                    bodyText ? '<pre>' + bodyText + '</pre>' : '<div class="empty-state">暂无响应内容</div>';
                
                // 默认显示响应体
                if (currentTab !== 'body') {
                    switchTab('body');
                }
                
            } catch (error) {
                const statusBadge = document.getElementById('statusBadge');
                const responseTime = document.getElementById('responseTime');
                
                statusBadge.textContent = '请求失败';
                statusBadge.className = 'status-badge status-error';
                responseTime.textContent = '耗时: ' + (Date.now() - startTime) + 'ms';
                
                document.getElementById('bodyTab').innerHTML = 
                    '<pre>{\n  "error": "' + error.message + '"\n}</pre>';
            } finally {
                loading = false;
                sendBtn.textContent = '发送请求';
                sendBtn.disabled = false;
            }
        }
        
        // 回车发送请求
        document.addEventListener('keypress', function(e) {
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                sendRequest();
            }
        });
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func (s *Server) serveApiDebuggerAssets(w http.ResponseWriter, r *http.Request) {
	// 简单的静态文件服务，主要用于未来扩展
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("API调试器资源文件"))
}
