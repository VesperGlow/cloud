package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/VesperGlow/cloud/internal/auth"
	"github.com/VesperGlow/cloud/internal/config"
	"github.com/VesperGlow/cloud/internal/ids"
	"github.com/VesperGlow/cloud/internal/storage"
	"github.com/VesperGlow/cloud/internal/webui"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const RootID = "00000000-0000-0000-0000-000000000000"
const maxJSONBody = 7 << 20
const maxDocumentBytes = 1 << 20
const maxAvatarBytes = 2 << 20
const avatarObjectKey = "profile/avatar"
const maxBlocksPerRequest = 1000
const maxCompleteBody = 256 << 20

type Server struct {
	db      *sql.DB
	storage storage.Storage
	auth    *auth.Service
	cfg     config.Config
	log     *slog.Logger
	limiter *loginLimiter
}

type File struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Size      int64   `json:"size"`
	MimeType  string  `json:"mime_type,omitempty"`
	ETag      string  `json:"etag,omitempty"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	objectKey string
}

func New(db *sql.DB, store storage.Storage, a *auth.Service, cfg config.Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{db: db, storage: store, auth: a, cfg: cfg, log: logger, limiter: newLoginLimiter()}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, s.securityHeaders, s.originGuard)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", s.ready)
	r.Get("/s/{token}", s.publicShare)
	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.logout)
			r.Get("/auth/me", s.me)
			r.Patch("/auth/credentials", s.changeCredentials)
			r.Get("/profile/avatar", s.getAvatar)
			r.Put("/profile/avatar", s.updateAvatar)
			r.Delete("/profile/avatar", s.deleteAvatar)
			r.Get("/storage/stats", s.storageStats)
			r.Get("/files/{id}", s.getFile)
			r.Get("/files/{id}/children", s.children)
			r.Get("/files/{id}/download", s.download)
			r.Get("/files/{id}/preview", s.preview)
			r.Get("/files/{id}/content", s.getDocument)
			r.Put("/files/{id}/content", s.updateDocument)
			r.Get("/files/{id}/book", s.bookInfo)
			r.Get("/files/{id}/book/content", s.bookContent)
			r.Get("/files/{id}/book/assets/{index}", s.bookAsset)
			r.Get("/files/{id}/book/cover", s.bookCover)
			r.Get("/files/{id}/book/progress", s.bookProgress)
			r.Put("/files/{id}/book/progress", s.saveBookProgress)
			r.Get("/files/{id}/share", s.getShare)
			r.Post("/files/{id}/share", s.createShare)
			r.Delete("/files/{id}/share", s.revokeShare)
			r.Post("/directories", s.createDirectory)
			r.Post("/documents", s.createDocument)
			r.Patch("/files/{id}", s.patchFile)
			r.Delete("/files/{id}", s.deleteFile)
			r.Post("/uploads", s.createUpload)
			r.Post("/uploads/{id}/blocks", s.uploadBlocks)
			r.Post("/uploads/{id}/complete", s.completeUpload)
			r.Delete("/uploads/{id}", s.abortUpload)
		})
	})
	r.Handle("/*", webui.Handler())
	return r
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		problem(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https: http:; media-src 'self' blob: https: http:; style-src 'self' 'unsafe-inline'; connect-src 'self' https: http:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				base, _ := url.Parse(s.cfg.BaseURL)
				got, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(base.Scheme, got.Scheme) || !strings.EqualFold(base.Host, got.Host) {
					problem(w, http.StatusForbidden, "origin not allowed")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if !s.limiter.allow(ip) {
		problem(w, http.StatusTooManyRequests, "too many login attempts; try again later")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if len(in.Username) > 128 || len(in.Password) > 1024 {
		s.limiter.fail(ip)
		problem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, expires, err := s.auth.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		s.limiter.fail(ip)
		problem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.limiter.success(ip)
	http.SetCookie(w, &http.Cookie{Name: "cloud_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	s.log.Info("user logged in", "user", in.Username)
	writeJSON(w, http.StatusOK, map[string]any{"username": in.Username, "has_avatar": s.hasAvatar(r.Context())})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("cloud_session"); err == nil {
		s.auth.Logout(r.Context(), c.Value)
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"username": r.Context().Value(userKey{}).(string), "has_avatar": s.hasAvatar(r.Context())})
}

func (s *Server) hasAvatar(ctx context.Context) bool {
	var value string
	return s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='avatar_mime'`).Scan(&value) == nil && value != ""
}

func (s *Server) getAvatar(w http.ResponseWriter, r *http.Request) {
	var contentType string
	if err := s.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='avatar_mime'`).Scan(&contentType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, "avatar not found")
		} else {
			problem(w, http.StatusInternalServerError, "database error")
		}
		return
	}
	data, err := s.storage.GetObject(r.Context(), avatarObjectKey, maxAvatarBytes)
	if err != nil {
		s.log.Error("avatar read failed", "error", err)
		problem(w, http.StatusBadGateway, "could not read avatar")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) updateAvatar(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DataURL string `json:"data_url"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	comma := strings.IndexByte(in.DataURL, ',')
	if comma < 0 || !strings.HasPrefix(in.DataURL, "data:image/") {
		problem(w, http.StatusBadRequest, "avatar must be a data URL")
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.DataURL[comma+1:])
	if err != nil || len(data) == 0 {
		problem(w, http.StatusBadRequest, "avatar data is invalid")
		return
	}
	if len(data) > maxAvatarBytes {
		problem(w, http.StatusRequestEntityTooLarge, "avatar must not exceed 2 MiB")
		return
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		problem(w, http.StatusUnsupportedMediaType, "avatar must be JPEG, PNG, GIF, or WebP")
		return
	}
	if _, err := s.storage.PutObject(r.Context(), avatarObjectKey, contentType, data); err != nil {
		s.log.Error("avatar write failed", "error", err)
		problem(w, http.StatusBadGateway, "could not save avatar")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO settings(key,value,updated_at) VALUES('avatar_mime',?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, contentType, now); err != nil {
		s.log.Error("avatar metadata write failed", "error", err)
		problem(w, http.StatusInternalServerError, "could not save avatar")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.DeleteObject(r.Context(), avatarObjectKey); err != nil {
		s.log.Error("avatar delete failed", "error", err)
		problem(w, http.StatusBadGateway, "could not delete avatar")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM settings WHERE key='avatar_mime'`); err != nil {
		problem(w, http.StatusInternalServerError, "could not delete avatar")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changeCredentials(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Username        string `json:"username"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.Username == "" || len(in.Username) > 128 || len(in.Password) < 12 || len(in.Password) > 1024 || len(in.CurrentPassword) > 1024 {
		problem(w, http.StatusBadRequest, "username is required and password must be between 12 and 1024 characters")
		return
	}
	currentUsername := r.Context().Value(userKey{}).(string)
	if err := s.auth.ChangeCredentials(r.Context(), currentUsername, in.CurrentPassword, in.Username, in.Password); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			problem(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		s.log.Error("credential change failed", "error", err)
		problem(w, http.StatusInternalServerError, "could not update credentials")
		return
	}
	s.clearSessionCookie(w)
	s.log.Info("administrator credentials changed", "previous_user", currentUsername, "user", in.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "cloud_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

type userKey struct{}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cloud_session")
		if err != nil {
			problem(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, err := s.auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			problem(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func scanFile(row interface{ Scan(...any) error }) (File, error) {
	var f File
	var parent, mime, etag sql.NullString
	err := row.Scan(&f.ID, &parent, &f.Name, &f.Kind, &f.objectKey, &f.Size, &mime, &etag, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if parent.Valid {
		f.ParentID = &parent.String
	}
	f.MimeType = mime.String
	f.ETag = etag.String
	return f, err
}

const fileColumns = `id,parent_id,name,kind,COALESCE(object_key,''),size,mime_type,etag,status,created_at,updated_at`

func (s *Server) file(ctx context.Context, id string) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM files WHERE id=?`, id))
}
func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "file not found")
		return
	}
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	crumbs, err := s.breadcrumbs(r.Context(), f.ID)
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"file": f, "breadcrumbs": crumbs})
}
func (s *Server) breadcrumbs(ctx context.Context, id string) ([]File, error) {
	const qualified = `f.id,f.parent_id,f.name,f.kind,COALESCE(f.object_key,''),f.size,f.mime_type,f.etag,f.status,f.created_at,f.updated_at`
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE p(id,parent_id,name,kind,object_key,size,mime_type,etag,status,created_at,updated_at,depth) AS (SELECT `+fileColumns+`,0 FROM files WHERE id=? UNION ALL SELECT `+qualified+`,p.depth+1 FROM files f JOIN p ON f.id=p.parent_id) SELECT `+fileColumns+` FROM p ORDER BY depth DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, e := scanFile(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Server) children(w http.ResponseWriter, r *http.Request) {
	parent, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || parent.Kind != "directory" {
		problem(w, 404, "directory not found")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT `+fileColumns+` FROM files WHERE parent_id=? ORDER BY kind DESC, name COLLATE NOCASE`, parent.ID)
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, e := scanFile(rows)
		if e != nil {
			problem(w, 500, "database error")
			return
		}
		out = append(out, f)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) storageStats(w http.ResponseWriter, r *http.Request) {
	var totalBytes, fileCount int64
	if err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0),COUNT(*) FROM files WHERE kind='file' AND status='ready'`).Scan(&totalBytes, &fileCount); err != nil {
		problem(w, http.StatusInternalServerError, "could not calculate storage usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"total_bytes": totalBytes, "file_count": fileCount})
}

func (s *Server) createDirectory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if decodeJSON(w, r, &in) != nil {
		return
	}
	if err := validateName(in.Name); err != nil {
		problem(w, 400, err.Error())
		return
	}
	parent, err := s.file(r.Context(), in.ParentID)
	if err != nil || parent.Kind != "directory" || parent.Status != "ready" {
		problem(w, 400, "parent directory is invalid")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	f := File{ID: ids.New(), ParentID: &in.ParentID, Name: in.Name, Kind: "directory", Status: "ready", CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO files(id,parent_id,name,kind,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, f.ID, in.ParentID, f.Name, f.Kind, f.Status, now, now)
	if isConflict(err) {
		problem(w, 409, "an item with that name already exists")
		return
	}
	if err != nil {
		problem(w, 500, "could not create directory")
		return
	}
	writeJSON(w, 201, f)
}

type documentInput struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	ETag     string `json:"etag"`
}

func editableDocumentName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt", ".yaml", ".yml", ".json", ".toml", ".ini", ".conf", ".log", ".csv":
		return true
	default:
		return false
	}
}

func documentMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".toml":
		return "application/toml; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func validateDocument(name, content string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !editableDocumentName(name) {
		return errors.New("this file type cannot be edited as text")
	}
	if !utf8.ValidString(content) {
		return errors.New("document must contain valid UTF-8 text")
	}
	if len([]byte(content)) > maxDocumentBytes {
		return errors.New("editable documents cannot exceed 1 MiB")
	}
	return nil
}

func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	var in documentInput
	if decodeJSON(w, r, &in) != nil {
		return
	}
	if err := validateDocument(in.Name, in.Content); err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	parent, err := s.file(r.Context(), in.ParentID)
	if err != nil || parent.Kind != "directory" || parent.Status != "ready" {
		problem(w, http.StatusBadRequest, "parent directory is invalid")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	key, manifest, err := s.storage.Store(r.Context(), bytes.NewReader([]byte(in.Content)))
	if err != nil {
		problem(w, http.StatusBadGateway, "object storage write failed")
		return
	}
	f := File{ID: ids.New(), ParentID: &in.ParentID, Name: in.Name, Kind: "file", Size: manifest.Size, MimeType: documentMime(in.Name), ETag: manifest.ID(), Status: "ready", CreatedAt: now, UpdatedAt: now, objectKey: key}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO files(id,parent_id,name,kind,object_key,size,mime_type,etag,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, f.ID, in.ParentID, f.Name, f.Kind, f.objectKey, f.Size, f.MimeType, f.ETag, f.Status, now, now)
	if isConflict(err) {
		problem(w, http.StatusConflict, "an item with that name already exists")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "could not create document")
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, http.StatusNotFound, "ready file not found")
		return
	}
	if !editableDocumentName(f.Name) {
		problem(w, http.StatusUnsupportedMediaType, "this file type cannot be edited as text")
		return
	}
	if f.Size > maxDocumentBytes {
		problem(w, http.StatusRequestEntityTooLarge, "editable documents cannot exceed 1 MiB")
		return
	}
	data, err := s.readContent(r.Context(), f)
	if errors.Is(err, storage.ErrObjectTooLarge) {
		problem(w, http.StatusRequestEntityTooLarge, "editable documents cannot exceed 1 MiB")
		return
	}
	if err != nil {
		problem(w, http.StatusBadGateway, "object storage read failed")
		return
	}
	if !utf8.Valid(data) {
		problem(w, http.StatusUnsupportedMediaType, "file is not valid UTF-8 text")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": string(data), "etag": f.ETag, "updated_at": f.UpdatedAt})
}

func (s *Server) updateDocument(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, http.StatusNotFound, "ready file not found")
		return
	}
	var in documentInput
	if decodeJSON(w, r, &in) != nil {
		return
	}
	if err := validateDocument(f.Name, in.Content); err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.ETag != "" && f.ETag != "" && in.ETag != f.ETag {
		problem(w, http.StatusConflict, "document changed elsewhere; reopen it before saving")
		return
	}
	key, manifest, err := s.storage.Store(r.Context(), bytes.NewReader([]byte(in.Content)))
	if err != nil {
		problem(w, http.StatusBadGateway, "object storage write failed")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = s.db.ExecContext(r.Context(), `UPDATE files SET object_key=?,size=?,mime_type=?,etag=?,updated_at=? WHERE id=?`, key, manifest.Size, documentMime(f.Name), manifest.ID(), now, f.ID); err != nil {
		problem(w, http.StatusInternalServerError, "document content changed but metadata update failed")
		return
	}
	updated, _ := s.file(r.Context(), f.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) patchFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == RootID {
		problem(w, 400, "root cannot be modified")
		return
	}
	var in struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if decodeJSON(w, r, &in) != nil {
		return
	}
	if in.Name == nil && in.ParentID == nil {
		problem(w, 400, "name or parent_id is required")
		return
	}
	f, err := s.file(r.Context(), id)
	if err != nil {
		problem(w, 404, "file not found")
		return
	}
	name := f.Name
	if in.Name != nil {
		name = *in.Name
		if err := validateName(name); err != nil {
			problem(w, 400, err.Error())
			return
		}
	}
	parent := *f.ParentID
	if in.ParentID != nil {
		parent = *in.ParentID
		p, err := s.file(r.Context(), parent)
		if err != nil || p.Kind != "directory" || p.Status != "ready" {
			problem(w, 400, "target directory is invalid")
			return
		}
		if f.Kind == "directory" {
			var cyclic int
			err = s.db.QueryRowContext(r.Context(), `WITH RECURSIVE d(id) AS (SELECT id FROM files WHERE id=? UNION ALL SELECT f.id FROM files f JOIN d ON f.parent_id=d.id) SELECT EXISTS(SELECT 1 FROM d WHERE id=?)`, id, parent).Scan(&cyclic)
			if err != nil {
				problem(w, 500, "database error")
				return
			}
			if cyclic == 1 {
				problem(w, 400, "a directory cannot be moved into itself or its descendants")
				return
			}
		}
	}
	_, err = s.db.ExecContext(r.Context(), `UPDATE files SET name=?,parent_id=?,updated_at=? WHERE id=?`, name, parent, time.Now().UTC().Format(time.RFC3339Nano), id)
	if isConflict(err) {
		problem(w, 409, "an item with that name already exists")
		return
	}
	if err != nil {
		problem(w, 500, "could not update item")
		return
	}
	updated, _ := s.file(r.Context(), id)
	writeJSON(w, 200, updated)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == RootID {
		problem(w, 400, "root cannot be deleted")
		return
	}
	f, err := s.file(r.Context(), id)
	if err != nil {
		problem(w, 404, "file not found")
		return
	}
	if f.Kind == "directory" {
		var n int
		if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM files WHERE parent_id=?`, id).Scan(&n); err != nil {
			problem(w, 500, "database error")
			return
		}
		if n > 0 {
			problem(w, 409, "directory must be empty before deletion")
			return
		}
		if _, err = s.db.ExecContext(r.Context(), `DELETE FROM files WHERE id=?`, id); err != nil {
			problem(w, 500, "could not delete directory")
			return
		}
		w.WriteHeader(204)
		return
	}
	// 任何状态的文件行都可以删除：pending 行是失败/中断的上传遗留，
	// 级联清理 uploads 记录后，孤儿块由垃圾回收器兜底。
	if _, err = s.db.ExecContext(r.Context(), `DELETE FROM files WHERE id=?`, id); err != nil {
		problem(w, 500, "could not delete file")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) { s.streamFile(w, r, false) }
func (s *Server) preview(w http.ResponseWriter, r *http.Request)  { s.streamFile(w, r, true) }
func (s *Server) streamFile(w http.ResponseWriter, r *http.Request, inline bool) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, 404, "ready file not found")
		return
	}
	if inline && !isPreviewable(f) {
		problem(w, 415, "preview is not available for this file type")
		return
	}
	s.serveFileContent(w, r, f, inline)
}

// serveFileContent delivers one logical file. Single-block files (the
// common case) still redirect to a presigned URL so content flows
// browser<->S3 directly; multi-block files stream through the server with
// Range support, and legacy whole-object keys keep working until the
// startup migration re-stores them as blocks.
func (s *Server) serveFileContent(w http.ResponseWriter, r *http.Request, f File, inline bool) {
	if !storage.IsManifestKey(f.objectKey) {
		u, err := s.storage.PresignGetObject(r.Context(), f.objectKey, f.Name, responseMime(f), inline, s.cfg.PresignExpires)
		if err != nil {
			problem(w, 502, "could not create download URL")
			return
		}
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	m, err := s.storage.GetManifest(r.Context(), f.objectKey)
	if err != nil {
		s.log.Error("manifest read failed", "file", f.ID, "key", f.objectKey, "error", err)
		problem(w, 502, "object storage read failed")
		return
	}
	if len(m.Blocks) == 1 {
		u, err := s.storage.PresignGetObject(r.Context(), storage.BlockKey(m.Blocks[0].ID), f.Name, responseMime(f), inline, s.cfg.PresignExpires)
		if err != nil {
			problem(w, 502, "could not create download URL")
			return
		}
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	rc, err := s.storage.Open(r.Context(), f.objectKey)
	if err != nil {
		s.log.Error("file open failed", "file", f.ID, "error", err)
		problem(w, 502, "object storage read failed")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", responseMime(f))
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+strings.ReplaceAll(url.PathEscape(f.Name), "+", "%20"))
	var modtime time.Time
	if t, err := time.Parse(time.RFC3339Nano, f.UpdatedAt); err == nil {
		modtime = t
	}
	http.ServeContent(w, r, f.Name, modtime, rc)
}

// readContent reads a whole file with a size guard, transparently handling
// legacy whole-object keys that predate block storage.
func (s *Server) readContent(ctx context.Context, f File) ([]byte, error) {
	if storage.IsManifestKey(f.objectKey) {
		return s.storage.ReadFile(ctx, f.objectKey, maxDocumentBytes)
	}
	return s.storage.GetObject(ctx, f.objectKey, maxDocumentBytes)
}

func responseMime(f File) string {
	if f.MimeType != "" && f.MimeType != "application/octet-stream" {
		return f.MimeType
	}
	switch strings.ToLower(filepath.Ext(f.Name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mov":
		return "video/quicktime"
	case ".m4v":
		return "video/x-m4v"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".toml":
		return "application/toml; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".txt", ".conf", ".ini", ".log":
		return "text/plain; charset=utf-8"
	default:
		return f.MimeType
	}
}

func newShareToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) getShare(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var token, created string
	err := s.db.QueryRowContext(r.Context(), `SELECT s.token,s.created_at FROM shares s JOIN files f ON f.id=s.file_id WHERE s.file_id=? AND f.kind='file' AND f.status='ready'`, id).Scan(&token, &created)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "could not read share")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": true, "url": s.cfg.BaseURL + "/s/" + token, "created_at": created})
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := s.file(r.Context(), id)
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, http.StatusNotFound, "ready file not found")
		return
	}
	token, err := newShareToken()
	if err != nil {
		problem(w, http.StatusInternalServerError, "could not generate share link")
		return
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO shares(file_id,token,created_at) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET token=excluded.token,created_at=excluded.created_at`, id, token, created)
	if err != nil {
		problem(w, http.StatusInternalServerError, "could not create share link")
		return
	}
	s.log.Info("file share created", "file_id", id)
	writeJSON(w, http.StatusCreated, map[string]any{"active": true, "url": s.cfg.BaseURL + "/s/" + token, "created_at": created})
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM shares WHERE file_id=?`, id); err != nil {
		problem(w, http.StatusInternalServerError, "could not revoke share link")
		return
	}
	s.log.Info("file share revoked", "file_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 || len(token) > 128 {
		problem(w, http.StatusNotFound, "share not found")
		return
	}
	var f File
	var parent, mime, etag sql.NullString
	err := s.db.QueryRowContext(r.Context(), `SELECT f.id,f.parent_id,f.name,f.kind,COALESCE(f.object_key,''),f.size,f.mime_type,f.etag,f.status,f.created_at,f.updated_at FROM shares s JOIN files f ON f.id=s.file_id WHERE s.token=? AND f.kind='file' AND f.status='ready'`, token).Scan(&f.ID, &parent, &f.Name, &f.Kind, &f.objectKey, &f.Size, &mime, &etag, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusNotFound, "share not found")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "could not open share")
		return
	}
	f.MimeType = mime.String
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	s.serveFileContent(w, r, f, true)
}

type createUploadInput struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var in createUploadInput
	if decodeJSON(w, r, &in) != nil {
		return
	}
	if err := validateName(in.Name); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if in.Size < 0 || in.Size > 5*1024*1024*1024*1024 {
		problem(w, 400, "invalid file size")
		return
	}
	if len(in.MimeType) > 255 {
		problem(w, 400, "mime type is too long")
		return
	}
	if in.MimeType == "" {
		in.MimeType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(in.MimeType); err != nil {
		problem(w, 400, "mime type is invalid")
		return
	}
	p, err := s.file(r.Context(), in.ParentID)
	if err != nil || p.Kind != "directory" || p.Status != "ready" {
		problem(w, 400, "parent directory is invalid")
		return
	}
	// Every file is a block upload now: the browser splits the content
	// into fixed-size blocks and PUTs them straight to S3 under their
	// SHA-256 content addresses; the manifest is only written on complete.
	fileID, uploadID := ids.New(), ids.New()
	now := time.Now().UTC()
	expires := now.Add(s.cfg.UploadExpires)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO files(id,parent_id,name,kind,size,mime_type,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, fileID, in.ParentID, in.Name, "file", in.Size, in.MimeType, "pending", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO uploads(id,file_id,block_size,expected_size,status,created_at,expires_at) VALUES(?,?,?,?,'pending',?,?)`, uploadID, fileID, s.cfg.BlockSize, in.Size, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano))
	}
	if err != nil {
		tx.Rollback()
		if isConflict(err) {
			problem(w, 409, "an item with that name already exists")
		} else {
			problem(w, 500, "could not create upload")
		}
		return
	}
	if err = tx.Commit(); err != nil {
		problem(w, 500, "could not create upload")
		return
	}
	s.log.Info("upload created", "file", in.Name, "size", in.Size, "block_size", s.cfg.BlockSize)
	writeJSON(w, 201, map[string]any{
		"upload_id":   uploadID,
		"file_id":     fileID,
		"mode":        "blocks",
		"block_size":  s.cfg.BlockSize,
		"block_count": blockCount(in.Size, s.cfg.BlockSize),
		"expires_at":  expires.Format(time.RFC3339Nano),
	})
}

type uploadRecord struct {
	ID, FileID, ObjectKey, Status string
	BlockSize, ExpectedSize       int64
}

func (s *Server) upload(ctx context.Context, id string) (uploadRecord, error) {
	var u uploadRecord
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.file_id,COALESCE(f.object_key,''),u.block_size,u.expected_size,u.status FROM uploads u JOIN files f ON f.id=u.file_id WHERE u.id=?`, id).Scan(&u.ID, &u.FileID, &u.ObjectKey, &u.BlockSize, &u.ExpectedSize, &u.Status)
	return u, err
}

type uploadBlockRequest struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}
type uploadBlockResponse struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	Exists bool   `json:"exists"`
	URL    string `json:"url,omitempty"`
}

// uploadBlocks issues conditional presigned PUT URLs for blocks that do
// not exist yet. The endpoint is stateless: completeUpload independently
// verifies every block, so registration is purely a client convenience.
func (s *Server) uploadBlocks(w http.ResponseWriter, r *http.Request) {
	u, err := s.upload(r.Context(), chi.URLParam(r, "id"))
	if err != nil || u.Status != "pending" {
		problem(w, 404, "pending upload not found")
		return
	}
	var body struct {
		Blocks []uploadBlockRequest `json:"blocks"`
	}
	if decodeJSON(w, r, &body) != nil {
		return
	}
	if len(body.Blocks) > maxBlocksPerRequest {
		problem(w, 400, "too many blocks in one request")
		return
	}
	for _, b := range body.Blocks {
		if !storage.ValidBlockID(b.ID) {
			problem(w, 400, "invalid block id")
			return
		}
		if b.Size < 1 || b.Size > u.BlockSize {
			problem(w, 400, "invalid block size")
			return
		}
	}
	results := make([]uploadBlockResponse, len(body.Blocks))
	indices := make([]int, len(body.Blocks))
	for i := range indices {
		indices[i] = i
	}
	if err := parallel(indices, 16, func(i int) error {
		b := body.Blocks[i]
		if _, e := s.storage.HeadBlock(r.Context(), b.ID); e == nil {
			results[i] = uploadBlockResponse{ID: b.ID, Size: b.Size, Exists: true}
			return nil
		} else if !storage.IsNotFound(e) {
			return e
		}
		uurl, e := s.storage.PresignBlockPut(r.Context(), b.ID, s.cfg.PresignExpires)
		if e != nil {
			return e
		}
		results[i] = uploadBlockResponse{ID: b.ID, Size: b.Size, URL: uurl}
		return nil
	}); err != nil {
		s.log.Error("block registration failed", "upload", u.ID, "error", err)
		problem(w, 502, "object storage could not prepare block uploads")
		return
	}
	writeJSON(w, 200, map[string]any{"blocks": results})
}
func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.upload(r.Context(), chi.URLParam(r, "id"))
	if err != nil || u.Status != "pending" {
		problem(w, 404, "pending upload not found")
		return
	}
	var body struct {
		Blocks []storage.Block `json:"blocks"`
	}
	if decodeJSONLimit(w, r, &body, maxCompleteBody) != nil {
		return
	}
	total := blockCount(u.ExpectedSize, u.BlockSize)
	if int64(len(body.Blocks)) != total {
		problem(w, 400, "complete block list is invalid")
		return
	}
	for i, b := range body.Blocks {
		if !storage.ValidBlockID(b.ID) {
			problem(w, 400, "complete block list is invalid")
			return
		}
		if b.Size != expectedBlockSize(u.ExpectedSize, u.BlockSize, int64(i)) {
			problem(w, 400, "complete block list is invalid")
			return
		}
	}
	// Verify that every block actually exists. Blocks are immutable
	// (conditional PUTs), so a verified list is a durable list. A crash
	// after PutManifest but before the DB commit makes this request safely
	// retryable: the conditional manifest PUT dedups to the same object.
	var mu sync.Mutex
	var missing, mismatched []string
	indices := make([]int, len(body.Blocks))
	for i := range indices {
		indices[i] = i
	}
	if err := parallel(indices, 32, func(i int) error {
		b := body.Blocks[i]
		got, e := s.storage.HeadBlock(r.Context(), b.ID)
		if e != nil {
			if storage.IsNotFound(e) {
				mu.Lock()
				missing = append(missing, b.ID)
				mu.Unlock()
				return nil
			}
			return e
		}
		if got.Size != b.Size {
			mu.Lock()
			mismatched = append(mismatched, b.ID)
			mu.Unlock()
		}
		return nil
	}); err != nil {
		s.log.Error("upload verification failed", "upload", u.ID, "error", err)
		problem(w, 502, "object storage could not verify the upload")
		return
	}
	if len(mismatched) > 0 {
		problem(w, 400, "one or more blocks were stored with an unexpected size")
		return
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		writeJSON(w, 409, map[string]any{"error": map[string]any{"status": 409, "message": "some blocks are missing from object storage", "missing_blocks": missing}})
		return
	}
	manifest := storage.Manifest{Version: 1, Size: u.ExpectedSize, Blocks: body.Blocks}
	key, err := s.storage.PutManifest(r.Context(), manifest)
	if err != nil {
		s.log.Error("manifest write failed", "upload", u.ID, "error", err)
		problem(w, 502, "object storage could not write the manifest")
		return
	}
	if err := s.finalizeUpload(r.Context(), u, key, manifest.ID()); err != nil {
		problem(w, 500, "object stored but metadata finalization failed")
		return
	}
	f, _ := s.file(r.Context(), u.FileID)
	s.log.Info("upload completed", "file", f.Name, "blocks", len(manifest.Blocks), "size", manifest.Size)
	writeJSON(w, 200, f)
}

func (s *Server) finalizeUpload(ctx context.Context, u uploadRecord, manifestKey, manifestID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE files SET object_key=?,status='ready',size=?,etag=?,updated_at=? WHERE id=? AND status='pending'`, manifestKey, u.ExpectedSize, manifestID, time.Now().UTC().Format(time.RFC3339Nano), u.FileID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE uploads SET status='completed' WHERE id=? AND status='pending'`, u.ID)
	}
	if err != nil {
		if tx != nil {
			tx.Rollback()
		}
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}
func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.upload(r.Context(), chi.URLParam(r, "id"))
	if err != nil || u.Status != "pending" {
		problem(w, 404, "pending upload not found")
		return
	}
	// Blocks uploaded so far are content-addressed garbage without a
	// manifest; the garbage collector reclaims them after the grace period.
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE uploads SET status='aborted' WHERE id=?`, u.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM files WHERE id=? AND status='pending'`, u.FileID)
	}
	if err != nil {
		if tx != nil {
			tx.Rollback()
		}
		problem(w, 500, "could not clean upload metadata")
		return
	}
	if err = tx.Commit(); err != nil {
		problem(w, 500, "could not clean upload metadata")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) failUpload(ctx context.Context, uploadID, fileID string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE uploads SET status='failed' WHERE id=?`, uploadID)
	_, _ = s.db.ExecContext(ctx, `UPDATE files SET status='failed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), fileID)
}

func (s *Server) CleanupExpiredUploads(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM uploads WHERE status='pending' AND expires_at<=?`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		s.log.Error("scan stale uploads failed", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		u, err := s.upload(ctx, id)
		if err != nil {
			continue
		}
		// Only legacy uploads predating block storage carry a whole-object
		// key to clean up; orphaned blocks are handled by the GC.
		if u.ObjectKey != "" {
			if err = s.storage.DeleteObject(ctx, u.ObjectKey); err != nil {
				s.log.Warn("stale upload object cleanup failed", "upload", id, "error", err)
				continue
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			continue
		}
		_, err = tx.ExecContext(ctx, `UPDATE uploads SET status='aborted' WHERE id=?`, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM files WHERE id=? AND status='pending'`, u.FileID)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err == nil {
			s.log.Info("stale upload cleaned", "upload", id)
		}
	}
}

func blockCount(size, blockSize int64) int64 {
	if size <= 0 {
		return 0
	}
	return (size + blockSize - 1) / blockSize
}

// expectedBlockSize returns the size the client must report for the
// index-th block (0-based): every block is full except the last one.
func expectedBlockSize(size, blockSize, index int64) int64 {
	total := blockCount(size, blockSize)
	if index == total-1 {
		return size - (total-1)*blockSize
	}
	return blockSize
}

// parallel runs fn over every index concurrently, bounded by limit
// workers, and returns the first error (other workers finish their
// in-flight item before the function returns).
func parallel(indices []int, limit int, fn func(int) error) error {
	if len(indices) == 0 {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(indices) {
		limit = len(indices)
	}
	queue := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				if err := fn(idx); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, idx := range indices {
		queue <- idx
	}
	close(queue)
	wg.Wait()
	return firstErr
}

func blockIDFromKey(key string) string {
	rest, ok := strings.CutPrefix(key, "blocks/")
	if !ok || len(rest) != 2+1+62 || rest[2] != '/' {
		return ""
	}
	id := rest[:2] + rest[3:]
	if !storage.ValidBlockID(id) {
		return ""
	}
	return id
}

// referencedObjectKeys returns every object key the metadata still points
// at, across all file states.
func (s *Server) referencedObjectKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT object_key FROM files WHERE object_key IS NOT NULL AND object_key <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// CollectGarbage deletes manifests that no metadata references and blocks
// that no surviving manifest references, once they are older than the
// grace period. The grace period must exceed UPLOAD_EXPIRES: blocks of an
// in-flight upload are unreferenced by construction, and any block older
// than the grace period cannot belong to an upload that is still pending.
func (s *Server) CollectGarbage(ctx context.Context) {
	grace := s.cfg.UploadExpires + time.Hour
	cutoff := time.Now().UTC().Add(-grace)
	referenced, err := s.referencedObjectKeys(ctx)
	if err != nil {
		s.log.Error("GC metadata scan failed", "error", err)
		return
	}
	manifests, err := s.storage.ListManifests(ctx)
	if err != nil {
		s.log.Error("GC manifest listing failed", "error", err)
		return
	}
	keepBlocks := map[string]bool{}
	var doomedManifests []string
	for _, ref := range manifests {
		m, err := s.storage.GetManifest(ctx, ref.Key)
		if err != nil {
			s.log.Warn("GC skipped unreadable manifest", "key", ref.Key, "error", err)
			continue
		}
		if !referenced[ref.Key] && ref.LastModified.Before(cutoff) {
			doomedManifests = append(doomedManifests, ref.Key)
			continue
		}
		for _, b := range m.Blocks {
			keepBlocks[b.ID] = true
		}
	}
	var deletedManifests, deletedBlocks, deletedLegacy int
	for _, key := range doomedManifests {
		if err := s.storage.DeleteObject(ctx, key); err != nil {
			s.log.Warn("GC manifest delete failed", "key", key, "error", err)
			continue
		}
		deletedManifests++
	}
	blocks, err := s.storage.ListBlocks(ctx)
	if err != nil {
		s.log.Error("GC block listing failed", "error", err)
		return
	}
	for _, ref := range blocks {
		id := blockIDFromKey(ref.Key)
		if id == "" || keepBlocks[id] {
			continue
		}
		if !ref.LastModified.Before(cutoff) {
			continue // young orphan: possibly an in-flight upload
		}
		if err := s.storage.DeleteObject(ctx, ref.Key); err != nil {
			s.log.Warn("GC block delete failed", "key", ref.Key, "error", err)
			continue
		}
		deletedBlocks++
	}
	// Leftover whole objects from before block storage.
	legacy, err := s.storage.ListPrefix(ctx, "objects/")
	if err != nil {
		s.log.Warn("GC legacy listing failed", "error", err)
	} else {
		for _, ref := range legacy {
			if referenced[ref.Key] || !ref.LastModified.Before(cutoff) {
				continue
			}
			if err := s.storage.DeleteObject(ctx, ref.Key); err != nil {
				s.log.Warn("GC legacy delete failed", "key", ref.Key, "error", err)
				continue
			}
			deletedLegacy++
		}
	}
	if deletedManifests+deletedBlocks+deletedLegacy > 0 {
		s.log.Info("garbage collection finished", "manifests", deletedManifests, "blocks", deletedBlocks, "legacy_objects", deletedLegacy)
	}
}

// MigrateLegacyObjects re-stores whole objects from before block storage
// as content-addressed blocks and manifests. It is idempotent: already
// migrated rows have manifests/ keys and are skipped, and a crash simply
// leaves the same rows for the next start. Rows whose object vanished are
// marked failed so the loss is visible instead of silently broken.
func (s *Server) MigrateLegacyObjects(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,object_key,size FROM files WHERE kind='file' AND status='ready' AND object_key IS NOT NULL AND object_key NOT LIKE 'manifests/%'`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id, key string
		size    int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.key, &it.size); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	migrated := 0
	for _, it := range items {
		body, err := s.storage.OpenRaw(ctx, it.key)
		if err != nil {
			if storage.IsNotFound(err) {
				s.log.Error("legacy object missing; marking file failed", "file", it.id, "key", it.key)
				_, _ = s.db.ExecContext(ctx, `UPDATE files SET status='failed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), it.id)
				continue
			}
			return migrated, fmt.Errorf("open legacy object %s: %w", it.key, err)
		}
		key, m, err := s.storage.Store(ctx, body)
		body.Close()
		if err != nil {
			return migrated, fmt.Errorf("re-store legacy object %s: %w", it.key, err)
		}
		if m.Size != it.size {
			s.log.Warn("legacy object size differs from metadata", "file", it.id, "stored", m.Size, "metadata", it.size)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		res, err := s.db.ExecContext(ctx, `UPDATE files SET object_key=?,size=?,etag=?,updated_at=? WHERE id=? AND object_key=?`, key, m.Size, m.ID(), now, it.id, it.key)
		if err != nil {
			return migrated, fmt.Errorf("update migrated file %s: %w", it.id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // changed under us; leave it alone
		}
		if err := s.storage.DeleteObject(ctx, it.key); err != nil {
			s.log.Warn("legacy object left for GC", "key", it.key, "error", err)
			continue
		}
		migrated++
	}
	return migrated, nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("invalid name")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("name cannot start or end with whitespace")
	}
	if len(name) > 1024 || utf8.RuneCountInString(name) > 255 {
		return errors.New("name is too long")
	}
	if strings.ContainsAny(name, "/\\") {
		return errors.New("name cannot contain path separators")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return errors.New("name contains control characters")
		}
	}
	return nil
}
func isPreviewable(f File) bool {
	mimeType := responseMime(f)
	if strings.HasPrefix(mimeType, "video/") || strings.HasPrefix(mimeType, "audio/") {
		return true
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/avif":
		return true
	default:
		return false
	}
}
func isConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed"))
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONLimit(w, r, dst, maxJSONBody)
}
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		problem(w, 400, "invalid JSON request")
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		problem(w, 400, "request must contain one JSON value")
		return errors.New("multiple JSON values")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

type loginAttempt struct {
	count int
	reset time.Time
}
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{attempts: map[string]loginAttempt{}} }
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if time.Now().After(a.reset) {
		delete(l.attempts, ip)
		return true
	}
	return a.count < 5
}
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if time.Now().After(a.reset) {
		a = loginAttempt{reset: time.Now().Add(15 * time.Minute)}
	}
	a.count++
	l.attempts[ip] = a
}
func (l *loginLimiter) success(ip string) { l.mu.Lock(); delete(l.attempts, ip); l.mu.Unlock() }
