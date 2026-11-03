package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gitlabvars/internal/gitlab"
	"gitlabvars/internal/web"
)

type Backend interface {
	CurrentUser(context.Context, string) (gitlab.User, error)
	Projects(context.Context, string, string, int) ([]gitlab.Project, int, error)
	Variables(context.Context, string, int64) ([]gitlab.Variable, error)
	Variable(context.Context, string, int64, string, string) (gitlab.Variable, error)
	UpdateValue(context.Context, string, int64, string, string, string) (gitlab.Variable, error)
}

type Server struct {
	cfg        Config
	live       Backend
	demo       Backend
	sessions   sessionStore
	locks      [64]sync.Mutex
	signingKey [32]byte
	tracking   *trackingStore
}

type variableView struct {
	Key              string  `json:"key"`
	Value            *string `json:"value,omitempty"`
	Masked           bool    `json:"masked"`
	Hidden           bool    `json:"hidden"`
	Secret           bool    `json:"secret"`
	EnvironmentScope string  `json:"environment_scope"`
	VariableType     string  `json:"variable_type"`
	Protected        bool    `json:"protected"`
	Raw              bool    `json:"raw"`
	Description      *string `json:"description"`
	Version          string  `json:"version"`
}

func New(cfg Config, live, demo Backend) *Server {
	s := &Server{cfg: cfg, live: live, demo: demo, tracking: newTrackingStore(), sessions: sessionStore{items: make(map[[32]byte]session), attempts: make(map[string]loginAttempt), ttl: cfg.SessionTTL}}
	if _, err := rand.Read(s.signingKey[:]); err != nil {
		panic("secure random source unavailable")
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("POST /api/demo-session", s.demoLogin)
	mux.HandleFunc("GET /api/session", s.auth(s.currentSession))
	mux.HandleFunc("DELETE /api/session", s.auth(s.logout))
	mux.HandleFunc("GET /api/projects", s.auth(s.projects))
	mux.HandleFunc("GET /api/projects/{id}", s.auth(s.project))
	mux.HandleFunc("GET /api/tracking", s.auth(s.getTracking))
	mux.HandleFunc("PUT /api/tracking", s.auth(s.putTracking))
	mux.HandleFunc("GET /api/groups", s.auth(s.groups))
	mux.HandleFunc("GET /api/groups/{id}/tree", s.auth(s.groupTree))
	mux.HandleFunc("GET /api/{kind}/{id}/variables", s.auth(s.resourceVariables))
	mux.HandleFunc("POST /api/{kind}/{id}/variables", s.auth(s.createVariable))
	mux.HandleFunc("PUT /api/{kind}/{id}/variables/{key}", s.auth(s.resourceUpdate))
	mux.HandleFunc("DELETE /api/{kind}/{id}/variables/{key}", s.auth(s.deleteVariable))
	mux.HandleFunc("POST /api/{kind}/{id}/variables/{key}/reveal", s.auth(s.revealVariable))
	mux.HandleFunc("POST /api/variables/bulk", s.auth(s.bulkCreate))
	mux.HandleFunc("POST /api/infrastructure/generate", s.auth(s.generateInfrastructure))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 404, "not_found", "Маршрут API не найден.")
	})
	mux.HandleFunc("/", s.asset)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(s.cfg.Origin, "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != s.cfg.Origin {
			fail(w, 403, "origin", "Источник запроса не разрешён. Откройте приложение по настроенному адресу.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"gitlab_url": s.cfg.GitLabURL, "demo": s.cfg.Demo && s.demo != nil})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sessions.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limit", "Слишком много попыток входа. Подождите минуту.")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.Token) == 0 || len(input.Token) > 4096 || strings.ContainsAny(input.Token, "\r\n\t ") {
		fail(w, 400, "token", "Введите корректный токен GitLab.")
		return
	}
	user, err := s.live.CurrentUser(r.Context(), input.Token)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	s.startSession(w, r, input.Token, user, false)
}

func (s *Server) demoLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Demo || s.demo == nil {
		fail(w, 404, "not_found", "Демонстрационный режим выключен.")
		return
	}
	if !s.sessions.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limit", "Слишком много попыток входа. Подождите минуту.")
		return
	}
	var input struct{}
	if !readJSON(w, r, &input) {
		return
	}
	u, err := s.demo.CurrentUser(r.Context(), "demo")
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	s.startSession(w, r, "demo", u, true)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, token string, user gitlab.User, demo bool) {
	id, se, ok := s.sessions.create(token, user, demo)
	if !ok {
		fail(w, 503, "capacity", "Не удалось создать сессию. Повторите позже.")
		return
	}
	s.sessions.remove(r)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: int(s.cfg.SessionTTL.Seconds()), Expires: se.Expires})
	s.sessionJSON(w, se)
}

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		se, ok := s.sessions.get(r)
		if !ok {
			fail(w, 401, "unauthorized", "Сессия завершена. Войдите снова.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(se.CSRF)) != 1 {
			fail(w, 403, "csrf", "Не удалось проверить запрос. Обновите страницу.")
			return
		}
		next(w, r, se)
	}
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request, se session) {
	s.sessionJSON(w, se)
}
func (s *Server) sessionJSON(w http.ResponseWriter, se session) {
	writeJSON(w, 200, map[string]any{"user": se.User, "csrf_token": se.CSRF, "demo": se.Demo})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, se session) {
	s.sessions.remove(r)
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) backend(se session) Backend {
	if se.Demo {
		return s.demo
	}
	return s.live
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request, se session) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 100000 {
			fail(w, 400, "page", "Некорректный номер страницы.")
			return
		}
		page = n
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(search) > 200 {
		fail(w, 400, "search", "Поисковый запрос слишком длинный.")
		return
	}
	projects, next, err := s.backend(se).Projects(r.Context(), se.Token, search, page)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	if projects == nil {
		projects = []gitlab.Project{}
	}
	writeJSON(w, 200, map[string]any{"projects": projects, "next_page": next})
}

func projectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "project", "Некорректный ID проекта.")
		return 0, false
	}
	return id, true
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

func (s *Server) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, gitlab.ErrSensitive) {
		fail(w, 403, "sensitive", "Hidden-переменные нельзя раскрывать. Можно заменить значение или удалить переменную.")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		fail(w, 504, "timeout", "GitLab не ответил вовремя. Повторите запрос.")
		return
	}
	var transport *gitlab.TransportError
	if errors.As(err, &transport) {
		switch transport.Kind {
		case "timeout":
			fail(w, 504, "gitlab_timeout", "Истекло время подключения к GitLab. Проверьте доступ к серверу и повторите вход.")
		case "tls":
			fail(w, 502, "gitlab_tls", "Не удалось проверить TLS-сертификат GitLab. Проверьте сертификат сервера и доверенные центры сертификации.")
		default:
			fail(w, 502, "gitlab_network", "Сервер приложения не может подключиться к GitLab. Проверьте сетевой доступ, DNS и настройки прокси.")
		}
		return
	}
	var upstream *gitlab.Error
	if errors.As(err, &upstream) {
		switch upstream.Status {
		case 401:
			s.sessions.remove(r)
			s.clearCookie(w)
			fail(w, 401, "unauthorized", "GitLab отклонил токен. Проверьте его срок действия и войдите снова.")
		case 403:
			switch r.PathValue("kind") {
			case "groups":
				fail(w, 403, "forbidden", "GitLab запретил доступ к переменным выбранной группы. Нужна роль Owner либо отдельное право управления CI/CD-переменными группы. Область api у токена не повышает роль пользователя. Если нужен одноимённый проект, выберите «Проекты» при добавлении источника.")
			case "projects":
				fail(w, 403, "forbidden", "GitLab запретил доступ к переменным выбранного проекта. Проверьте права на CI/CD-переменные проекта и область api у токена.")
			default:
				fail(w, 403, "forbidden", "GitLab запретил этот запрос. Проверьте права пользователя и область api у токена.")
			}
		case 404:
			fail(w, 404, "not_found", "Проект или переменная не найдены либо недоступны.")
		case 400, 422:
			fail(w, 422, "gitlab_validation", "GitLab отклонил значение переменной. Проверьте ограничения проекта.")
		case 409:
			fail(w, 409, "conflict", "GitLab сообщил о конфликте. Обновите список переменных.")
		case 429:
			w.Header().Set("Retry-After", "60")
			if seconds, e := strconv.Atoi(upstream.RetryAfter); e == nil && seconds > 0 && seconds < 86400 {
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
			}
			fail(w, 429, "rate_limit", "Достигнут лимит запросов GitLab. Повторите позже.")
		default:
			fail(w, 502, "gitlab_unavailable", "GitLab временно недоступен. Повторите позже.")
		}
		return
	}
	fail(w, 502, "gitlab_unavailable", "Не удалось связаться с GitLab. Повторите позже.")
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "content_type", "Ожидается JSON.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		fail(w, 400, "json", "Некорректное тело запроса.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "json", "Ожидается один JSON-объект.")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if name != "index.html" && name != "app.js" && name != "tracking.js" && name != "infrastructure.js" && name != "styles.css" && name != "login.css" && name != "tracking.css" && name != "infrastructure.css" && name != "akron.png" && name != "akron.svg" && name != "login-background.svg" {
		http.NotFound(w, r)
		return
	}
	b, err := web.Files.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch name {
	case "index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case "app.js", "tracking.js", "infrastructure.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case "styles.css", "login.css", "tracking.css", "infrastructure.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "akron.png":
		w.Header().Set("Content-Type", "image/png")
	case "akron.svg", "login-background.svg":
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	}
	_, _ = w.Write(b)
}
