// Package server implements the flyer HTTP API and page serving.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CNife/flyer/internal/api"
	"github.com/CNife/flyer/internal/auth"
	"github.com/CNife/flyer/internal/config"
	"github.com/CNife/flyer/internal/store"
)

var errUnauthorized = errors.New("unauthorized")

type identity struct {
	isAdmin bool
	keyID   *int64
	name    string
}

func (i *identity) rateKey() string {
	if i.isAdmin {
		return "admin"
	}
	return fmt.Sprintf("key:%d", *i.keyID)
}

type Server struct {
	cfg             config.Config
	store           *store.Store
	signer          *auth.Signer
	logger          *log.Logger
	mux             *http.ServeMux
	uploadLimiter   *rateLimiter
	passwordLimiter *rateLimiter
	passwordForm    *template.Template
	now             func() time.Time
}

func New(cfg config.Config, st *store.Store, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{
		cfg:             cfg,
		store:           st,
		signer:          auth.NewSigner(cfg.SessionSecret),
		logger:          logger,
		mux:             http.NewServeMux(),
		uploadLimiter:   newRateLimiter(cfg.RateLimitPerHour, time.Hour),
		passwordLimiter: newRateLimiter(10, time.Minute),
		passwordForm:    template.Must(template.New("password").Parse(passwordPage)),
		now:             time.Now,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	s.mux.HandleFunc("POST /api/pages", s.handlePublish)
	s.mux.HandleFunc("GET /api/pages", s.handleListPages)
	s.mux.HandleFunc("POST /api/pages/{slug}/expire", s.handleExpirePage)
	s.mux.HandleFunc("DELETE /api/pages/{slug}", s.handleDeletePage)
	s.mux.HandleFunc("GET /api/admin/stats", s.handleStats)
	s.mux.HandleFunc("POST /api/admin/cleanup", s.handleCleanup)
	s.mux.HandleFunc("GET /api/admin/keys", s.handleListKeys)
	s.mux.HandleFunc("POST /api/admin/keys", s.handleCreateKey)
	s.mux.HandleFunc("DELETE /api/admin/keys/{id}", s.handleRevokeKey)
	s.mux.HandleFunc("GET /{slug}", s.handlePage)
	s.mux.HandleFunc("POST /{slug}", s.handlePasswordSubmit)
}

func (s *Server) Handler() http.Handler {
	return s.recoverPanics(s.securityHeaders(s.mux))
}

func (s *Server) StartCleanup(ctx context.Context, interval time.Duration) {
	s.cleanup(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanup(ctx)
			}
		}
	}()
}

func (s *Server) cleanup(ctx context.Context) {
	n, err := s.store.Cleanup(ctx, s.now(), s.cfg.GracePeriod.Duration)
	if err != nil {
		s.logger.Printf("cleanup: %v", err)
		return
	}
	if n > 0 {
		s.logger.Printf("cleanup: removed %d expired page(s)", n)
	}
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(r *http.Request) (*identity, error) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errUnauthorized
	}
	token := parts[1]
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminKey)) == 1 {
		return &identity{isAdmin: true, name: "admin"}, nil
	}
	key, err := s.store.GetKeyByHash(r.Context(), auth.HashKey(token))
	if err != nil || key.RevokedAt != nil {
		return nil, errUnauthorized
	}
	id := key.ID
	return &identity{keyID: &id, name: key.Name}, nil
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	id, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return false
	}
	if !id.isAdmin {
		writeError(w, http.StatusForbidden, "admin key required")
		return false
	}
	return true
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	now := s.now()
	if ok, retry := s.uploadLimiter.allow(id.rateKey(), now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+64*1024)
	var req api.PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "page is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.HTML) == "" {
		writeError(w, http.StatusBadRequest, "html must not be empty")
		return
	}
	if int64(len(req.HTML)) > s.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "page is too large")
		return
	}
	if req.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds must not be negative")
		return
	}

	var expires *time.Time
	if !req.Permanent {
		ttl := s.cfg.DefaultTTL.Duration
		if req.TTLSeconds > 0 {
			ttl = time.Duration(req.TTLSeconds) * time.Second
		}
		if ttl > s.cfg.MaxTTL.Duration {
			writeError(w, http.StatusBadRequest, "ttl exceeds server maximum")
			return
		}
		e := now.Add(ttl)
		expires = &e
	}

	var passwordHash string
	if req.Password != "" {
		passwordHash, err = auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var slug string
	for range 8 {
		candidate, err := auth.NewSlug()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate slug")
			return
		}
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = candidate
		}
		err = s.store.CreatePage(r.Context(), store.Page{
			Slug:         candidate,
			Title:        title,
			FileName:     candidate + ".html",
			PasswordHash: passwordHash,
			OwnerKeyID:   id.keyID,
			Size:         int64(len(req.HTML)),
			CreatedAt:    now,
			ExpiresAt:    expires,
		}, []byte(req.HTML))
		if errors.Is(err, store.ErrSlugExists) {
			continue
		}
		if err != nil {
			s.logger.Printf("create page: %v", err)
			writeError(w, http.StatusInternalServerError, "could not store page")
			return
		}
		slug = candidate
		break
	}
	if slug == "" {
		writeError(w, http.StatusInternalServerError, "could not allocate a slug")
		return
	}

	writeJSON(w, http.StatusCreated, api.PublishResponse{
		Slug:      slug,
		URL:       strings.TrimRight(s.cfg.BaseURL, "/") + "/" + slug,
		ExpiresAt: expires,
		Protected: passwordHash != "",
	})
}

func (s *Server) handleListPages(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	limit, offset, err := pageListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var owner *int64
	if !id.isAdmin {
		owner = id.keyID
	}
	pages, total, err := s.store.ListPages(r.Context(), owner, limit, offset)
	if err != nil {
		s.logger.Printf("list pages: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list pages")
		return
	}
	baseURL := strings.TrimRight(s.cfg.BaseURL, "/")
	out := make([]api.Page, 0, len(pages))
	for _, p := range pages {
		out = append(out, api.Page{
			Slug:       p.Slug,
			Title:      p.Title,
			URL:        baseURL + "/" + p.Slug,
			Size:       p.Size,
			Protected:  p.PasswordHash != "",
			OwnerKeyID: p.OwnerKeyID,
			CreatedAt:  p.CreatedAt,
			ExpiresAt:  p.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, api.PageListResponse{
		Pages: out, Total: total, Limit: limit, Offset: offset,
	})
}

func pageListParams(r *http.Request) (int, int, error) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return 0, 0, errors.New("limit must be an integer between 1 and 500")
		}
		limit = n
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

func (s *Server) handleExpirePage(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	slug := r.PathValue("slug")
	if !auth.ValidSlug(slug) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	page, err := s.store.GetPage(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read page")
		return
	}
	if !canManage(id, page) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	now := s.now()
	if err := s.store.ExpirePage(r.Context(), slug, now); err != nil {
		switch {
		case errors.Is(err, store.ErrExpired):
			writeError(w, http.StatusConflict, "page already expired")
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "page not found")
		default:
			s.logger.Printf("expire page %s: %v", slug, err)
			writeError(w, http.StatusInternalServerError, "could not expire page")
		}
		return
	}
	writeJSON(w, http.StatusOK, api.ExpirePageResponse{
		Expired: true, ExpiresAt: time.Unix(now.Unix(), 0),
	})
}

func canManage(id *identity, page *store.Page) bool {
	if id.isAdmin {
		return true
	}
	return page.OwnerKeyID != nil && id.keyID != nil && *page.OwnerKeyID == *id.keyID
}

func (s *Server) handleDeletePage(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	slug := r.PathValue("slug")
	if !auth.ValidSlug(slug) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	page, err := s.store.GetPage(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read page")
		return
	}
	if !canManage(id, page) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err := s.store.DeletePage(r.Context(), slug); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "page not found")
			return
		}
		s.logger.Printf("delete page %s: %v", slug, err)
		writeError(w, http.StatusInternalServerError, "could not delete page")
		return
	}
	writeJSON(w, http.StatusOK, api.DeletePageResponse{Deleted: true})
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !auth.ValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	page, ok := s.loadPage(w, r, slug)
	if !ok {
		return
	}
	if page.PasswordHash != "" {
		cookie, err := r.Cookie(auth.SessionCookie)
		if err != nil || !s.signer.Verify(cookie.Value, slug, s.now().Unix()) {
			s.renderPassword(w, page, "", http.StatusUnauthorized)
			return
		}
	}
	s.servePage(w, r, page)
}

func (s *Server) handlePasswordSubmit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !auth.ValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	page, ok := s.loadPage(w, r, slug)
	if !ok {
		return
	}
	if page.PasswordHash == "" {
		//nolint:gosec // slug 已通过 auth.ValidSlug 校验，不构成开放重定向
		http.Redirect(w, r, "/"+slug, http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	now := s.now()
	if ok, retry := s.passwordLimiter.allow(clientIP(r)+"|"+slug, now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.renderPassword(w, page, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	valid, err := auth.VerifyPassword(page.PasswordHash, r.PostFormValue("password"))
	if err != nil {
		s.logger.Printf("verify password for %s: %v", slug, err)
		s.renderPassword(w, page, "This page could not be unlocked.", http.StatusInternalServerError)
		return
	}
	if !valid {
		s.renderPassword(w, page, "Wrong password.", http.StatusUnauthorized)
		return
	}
	expires := auth.CookieExpiry(now, page.ExpiresAt)
	if !expires.After(now) {
		s.renderExpired(w)
		return
	}
	//nolint:gosec // Secure 跟随 BaseURL：本地 http 部署必须允许非 Secure cookie
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    s.signer.Sign(slug, expires.Unix()),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(expires.Sub(now).Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.cfg.BaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	//nolint:gosec // slug 已通过 auth.ValidSlug 校验，不构成开放重定向
	http.Redirect(w, r, "/"+slug, http.StatusSeeOther)
}

func (s *Server) loadPage(w http.ResponseWriter, r *http.Request, slug string) (*store.Page, bool) {
	page, err := s.store.GetPage(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.logger.Printf("get page %s: %v", slug, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, false
	}
	now := s.now()
	if page.ExpiresAt != nil && !now.Before(*page.ExpiresAt) {
		if now.After(page.ExpiresAt.Add(s.cfg.GracePeriod.Duration)) {
			http.NotFound(w, r)
		} else {
			s.renderExpired(w)
		}
		return nil, false
	}
	return page, true
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request, page *store.Page) {
	f, err := s.store.OpenPage(page.FileName)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Printf("open page %s: %v", page.Slug, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, page.FileName, page.CreatedAt, f)
}

func (s *Server) renderExpired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><meta name=\"robots\" content=\"noindex\"><title>Expired</title><p>This page has expired.</p>"))
}

func (s *Server) renderPassword(w http.ResponseWriter, page *store.Page, errMsg string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = s.passwordForm.Execute(w, struct {
		Title string
		Error string
		Slug  string
	}{Title: page.Title, Error: errMsg, Slug: page.Slug})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	st, err := s.store.Stats(r.Context(), s.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read stats")
		return
	}
	writeJSON(w, http.StatusOK, api.StatsResponse{
		Pages: st.Pages, Active: st.Active, Expired: st.Expired,
		Permanent: st.Permanent, Bytes: st.Bytes, Keys: st.Keys,
	})
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	n, err := s.store.Cleanup(r.Context(), s.now(), s.cfg.GracePeriod.Duration)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cleanup failed")
		return
	}
	writeJSON(w, http.StatusOK, api.CleanupResponse{Deleted: n})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list keys")
		return
	}
	out := make([]api.Key, 0, len(keys))
	for _, k := range keys {
		out = append(out, api.Key{
			ID: k.ID, Name: k.Name, Role: k.Role,
			CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt,
		})
	}
	writeJSON(w, http.StatusOK, api.KeyListResponse{Keys: out})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req api.CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
		return
	}
	if req.Role == "" {
		req.Role = "publisher"
	}
	if req.Role != "publisher" {
		writeError(w, http.StatusBadRequest, "role must be publisher")
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate key")
		return
	}
	now := s.now()
	k, err := s.store.CreateKey(r.Context(), req.Name, req.Role, auth.HashKey(key), now)
	if err != nil {
		s.logger.Printf("create key: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create key")
		return
	}
	writeJSON(w, http.StatusCreated, api.CreateKeyResponse{
		ID: k.ID, Name: k.Name, Role: k.Role, Key: key, CreatedAt: k.CreatedAt,
	})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := s.store.RevokeKey(r.Context(), id, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke key")
		return
	}
	writeJSON(w, http.StatusOK, api.RevokeKeyResponse{Revoked: true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: make(map[string][]time.Time)}
}

func (l *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	list := l.hits[key]
	i := 0
	for i < len(list) && !list[i].After(cutoff) {
		i++
	}
	list = list[i:]
	if len(list) >= l.limit {
		retry := list[0].Add(l.window).Sub(now)
		if retry < 0 {
			retry = 0
		}
		l.hits[key] = list
		return false, retry
	}
	l.hits[key] = append(list, now)
	return true, 0
}

//nolint:gosec // 登录页 HTML 模板，不是硬编码凭据
const passwordPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow, noarchive">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:#f4f4f1;color:#1c1c1a}
main{width:min(92vw,24rem);padding:2rem;background:#fff;border:1px solid #d8d8d2;border-radius:14px;box-shadow:0 12px 40px rgba(0,0,0,.08)}
h1{font-size:1.15rem;margin:0 0 1rem;overflow-wrap:anywhere}
input,button{box-sizing:border-box;width:100%;font:inherit;padding:.7rem .8rem;border-radius:8px}
input{border:1px solid #bdbdb6;background:#fff}
button{margin-top:.6rem;border:0;background:#1c1c1a;color:#fff;cursor:pointer}
.error{color:#b42318;margin:0 0 .8rem}
@media (prefers-color-scheme:dark){body{background:#111;color:#eee}main{background:#1b1b1a;border-color:#333}input{background:#111;color:#eee;border-color:#444}button{background:#eee;color:#111}}
</style>
</head>
<body><main>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post" action="/{{.Slug}}">
<input type="password" name="password" placeholder="Password" autocomplete="current-password" autofocus required>
<button type="submit">View page</button>
</form>
</main></body></html>`
