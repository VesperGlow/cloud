package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/VesperGlow/cloud/internal/auth"
	"github.com/VesperGlow/cloud/internal/config"
	"github.com/VesperGlow/cloud/internal/database"
	"github.com/VesperGlow/cloud/internal/storage"
)

type mockStorage struct {
	objects   map[string]storage.ObjectInfo
	contents  map[string][]byte
	deleteErr error
}

func (m *mockStorage) Ping(context.Context) error { return nil }
func (m *mockStorage) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	m.objects[key] = storage.ObjectInfo{}
	return "https://s3.example/put", nil
}
func (m *mockStorage) PresignGet(context.Context, string, string, string, bool, time.Duration) (string, error) {
	return "https://s3.example/get", nil
}
func (m *mockStorage) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	v, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{}, errors.New("not found")
	}
	return v, nil
}
func (m *mockStorage) Read(_ context.Context, key string, limit int64) ([]byte, error) {
	data, ok := m.contents[key]
	if !ok {
		return nil, errors.New("not found")
	}
	if int64(len(data)) > limit {
		return nil, storage.ErrObjectTooLarge
	}
	return append([]byte(nil), data...), nil
}
func (m *mockStorage) Write(_ context.Context, key, _ string, data []byte) (storage.ObjectInfo, error) {
	m.contents[key] = append([]byte(nil), data...)
	info := storage.ObjectInfo{Size: int64(len(data)), ETag: `"mock-etag"`}
	m.objects[key] = info
	return info, nil
}
func (m *mockStorage) Delete(_ context.Context, key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.objects, key)
	delete(m.contents, key)
	return nil
}
func (m *mockStorage) CreateMultipart(context.Context, string, string) (string, error) {
	return "s3-upload", nil
}
func (m *mockStorage) PresignPart(context.Context, string, string, int32, time.Duration) (string, error) {
	return "https://s3.example/part", nil
}
func (m *mockStorage) CompleteMultipart(context.Context, string, string, []storage.CompletedPart) error {
	return nil
}
func (m *mockStorage) AbortMultipart(context.Context, string, string) error { return nil }

type testApp struct {
	t       *testing.T
	db      *sql.DB
	store   *mockStorage
	handler http.Handler
	cookie  *http.Cookie
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	db, err := database.Open(t.TempDir() + "/cloud.db")
	if err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{DB: db, Params: auth.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}}
	if _, err := a.Initialize(context.Background(), "admin", "a-secure-test-password"); err != nil {
		t.Fatal(err)
	}
	store := &mockStorage{objects: map[string]storage.ObjectInfo{}, contents: map[string][]byte{}}
	cfg := config.Config{BaseURL: "http://example.test", MultipartThreshold: 100, PartSize: 5 * 1024 * 1024, PresignExpires: time.Minute, UploadExpires: time.Hour}
	app := &testApp{t: t, db: db, store: store, handler: New(db, store, a, cfg, nil).Handler()}
	resp := app.request("POST", "/api/auth/login", map[string]any{"username": "admin", "password": "a-secure-test-password"}, false)
	if resp.Code != 200 {
		t.Fatalf("login status %d: %s", resp.Code, resp.Body.String())
	}
	app.cookie = resp.Result().Cookies()[0]
	t.Cleanup(func() { db.Close() })
	return app
}

func TestChangeCredentialsRequiresCurrentPasswordAndRevokesSession(t *testing.T) {
	a := newTestApp(t)
	wrong := a.request("PATCH", "/api/auth/credentials", map[string]any{"current_password": "wrong-password", "username": "owner", "password": "a-new-secure-password"}, true)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password status=%d: %s", wrong.Code, wrong.Body.String())
	}
	changed := a.request("PATCH", "/api/auth/credentials", map[string]any{"current_password": "a-secure-test-password", "username": "owner", "password": "a-new-secure-password"}, true)
	if changed.Code != http.StatusNoContent {
		t.Fatalf("change status=%d: %s", changed.Code, changed.Body.String())
	}
	me := a.request("GET", "/api/auth/me", nil, true)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("old session remains valid: %d", me.Code)
	}
	oldLogin := a.request("POST", "/api/auth/login", map[string]any{"username": "admin", "password": "a-secure-test-password"}, false)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old login status=%d", oldLogin.Code)
	}
	newLogin := a.request("POST", "/api/auth/login", map[string]any{"username": "owner", "password": "a-new-secure-password"}, false)
	if newLogin.Code != http.StatusOK {
		t.Fatalf("new login status=%d: %s", newLogin.Code, newLogin.Body.String())
	}
}
func (a *testApp) request(method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	a.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated && a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	rr := httptest.NewRecorder()
	a.handler.ServeHTTP(rr, req)
	return rr
}
func decode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRootProtectionAndNameConflict(t *testing.T) {
	a := newTestApp(t)
	rr := a.request("PATCH", "/api/files/"+RootID, map[string]any{"name": "changed"}, true)
	if rr.Code != 400 {
		t.Fatalf("root rename status=%d", rr.Code)
	}
	first := a.request("POST", "/api/directories", map[string]any{"parent_id": RootID, "name": "Photos"}, true)
	if first.Code != 201 {
		t.Fatalf("create status=%d: %s", first.Code, first.Body.String())
	}
	second := a.request("POST", "/api/directories", map[string]any{"parent_id": RootID, "name": "Photos"}, true)
	if second.Code != 409 {
		t.Fatalf("duplicate status=%d", second.Code)
	}
}

func TestDirectoryCannotMoveIntoDescendant(t *testing.T) {
	a := newTestApp(t)
	parentRR := a.request("POST", "/api/directories", map[string]any{"parent_id": RootID, "name": "Parent"}, true)
	parent := decode[File](t, parentRR)
	childRR := a.request("POST", "/api/directories", map[string]any{"parent_id": parent.ID, "name": "Child"}, true)
	child := decode[File](t, childRR)
	rr := a.request("PATCH", "/api/files/"+parent.ID, map[string]any{"parent_id": child.ID}, true)
	if rr.Code != 400 {
		t.Fatalf("cycle status=%d: %s", rr.Code, rr.Body.String())
	}
}

func TestShareLinkCanBeReadRotatedAndRevoked(t *testing.T) {
	a := newTestApp(t)
	id := "11111111-1111-4111-8111-111111111111"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`INSERT INTO files(id,parent_id,name,kind,object_key,size,mime_type,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, RootID, "profile.yaml", "file", "objects/profile", 42, "application/octet-stream", "ready", now, now); err != nil {
		t.Fatal(err)
	}
	createdRR := a.request("POST", "/api/files/"+id+"/share", nil, true)
	if createdRR.Code != http.StatusCreated {
		t.Fatalf("create share=%d: %s", createdRR.Code, createdRR.Body.String())
	}
	created := decode[struct {
		Active bool   `json:"active"`
		URL    string `json:"url"`
	}](t, createdRR)
	if !created.Active {
		t.Fatal("created share is inactive")
	}
	shareURL, err := url.Parse(created.URL)
	if err != nil {
		t.Fatal(err)
	}
	publicRR := a.request("GET", shareURL.Path, nil, false)
	if publicRR.Code != http.StatusFound || publicRR.Header().Get("Location") != "https://s3.example/get" {
		t.Fatalf("public share=%d location=%q", publicRR.Code, publicRR.Header().Get("Location"))
	}
	statusRR := a.request("GET", "/api/files/"+id+"/share", nil, true)
	status := decode[struct {
		Active bool   `json:"active"`
		URL    string `json:"url"`
	}](t, statusRR)
	if !status.Active || status.URL != created.URL {
		t.Fatalf("share status=%+v", status)
	}
	rotatedRR := a.request("POST", "/api/files/"+id+"/share", nil, true)
	rotated := decode[struct{ URL string `json:"url"` }](t, rotatedRR)
	if rotated.URL == created.URL {
		t.Fatal("rotating share reused token")
	}
	if oldRR := a.request("GET", shareURL.Path, nil, false); oldRR.Code != http.StatusNotFound {
		t.Fatalf("old share remains active: %d", oldRR.Code)
	}
	if revokedRR := a.request("DELETE", "/api/files/"+id+"/share", nil, true); revokedRR.Code != http.StatusNoContent {
		t.Fatalf("revoke share=%d", revokedRR.Code)
	}
	rotatedURL, _ := url.Parse(rotated.URL)
	if publicRR := a.request("GET", rotatedURL.Path, nil, false); publicRR.Code != http.StatusNotFound {
		t.Fatalf("revoked share remains active: %d", publicRR.Code)
	}
}

func TestResponseMimeRecognizesYAML(t *testing.T) {
	got := responseMime(File{Name: "profile.yaml", MimeType: "application/octet-stream"})
	if got != "application/yaml; charset=utf-8" {
		t.Fatalf("yaml content type=%q", got)
	}
}

func TestCreateReadAndUpdateDocument(t *testing.T) {
	a := newTestApp(t)
	createdRR := a.request("POST", "/api/documents", map[string]any{"parent_id": RootID, "name": "notes.md", "content": "# First\n"}, true)
	if createdRR.Code != http.StatusCreated {
		t.Fatalf("create document=%d: %s", createdRR.Code, createdRR.Body.String())
	}
	created := decode[File](t, createdRR)
	if created.Status != "ready" || created.MimeType != "text/markdown; charset=utf-8" {
		t.Fatalf("created document=%+v", created)
	}
	readRR := a.request("GET", "/api/files/"+created.ID+"/content", nil, true)
	if readRR.Code != http.StatusOK {
		t.Fatalf("read document=%d: %s", readRR.Code, readRR.Body.String())
	}
	read := decode[struct {
		Content string `json:"content"`
		ETag    string `json:"etag"`
	}](t, readRR)
	if read.Content != "# First\n" || read.ETag == "" {
		t.Fatalf("document content=%q etag=%q", read.Content, read.ETag)
	}
	conflictRR := a.request("PUT", "/api/files/"+created.ID+"/content", map[string]any{"content": "changed", "etag": "wrong"}, true)
	if conflictRR.Code != http.StatusConflict {
		t.Fatalf("stale edit=%d: %s", conflictRR.Code, conflictRR.Body.String())
	}
	updatedRR := a.request("PUT", "/api/files/"+created.ID+"/content", map[string]any{"content": "# Saved\n", "etag": read.ETag}, true)
	if updatedRR.Code != http.StatusOK {
		t.Fatalf("update document=%d: %s", updatedRR.Code, updatedRR.Body.String())
	}
	reread := a.request("GET", "/api/files/"+created.ID+"/content", nil, true)
	got := decode[struct{ Content string `json:"content"` }](t, reread)
	if got.Content != "# Saved\n" {
		t.Fatalf("updated content=%q", got.Content)
	}
}

func TestSingleUploadRequiresHeadVerification(t *testing.T) {
	a := newTestApp(t)
	createdRR := a.request("POST", "/api/uploads", map[string]any{"parent_id": RootID, "name": "hello.txt", "size": 12, "mime_type": "text/plain"}, true)
	if createdRR.Code != 201 {
		t.Fatalf("create upload=%d: %s", createdRR.Code, createdRR.Body.String())
	}
	created := decode[map[string]any](t, createdRR)
	uploadID := created["upload_id"].(string)
	var objectKey string
	if err := a.db.QueryRow(`SELECT f.object_key FROM files f JOIN uploads u ON u.file_id=f.id WHERE u.id=?`, uploadID).Scan(&objectKey); err != nil {
		t.Fatal(err)
	}
	a.store.objects[objectKey] = storage.ObjectInfo{Size: 12, ETag: "etag"}
	complete := a.request("POST", "/api/uploads/"+uploadID+"/complete", map[string]any{}, true)
	if complete.Code != 200 {
		t.Fatalf("complete=%d: %s", complete.Code, complete.Body.String())
	}
	var status string
	if err := a.db.QueryRow(`SELECT status FROM files WHERE object_key=?`, objectKey).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("status=%s", status)
	}
}

func TestDeleteFailureRetainsDeletingMetadata(t *testing.T) {
	a := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`INSERT INTO files(id,parent_id,name,kind,object_key,size,status,created_at,updated_at) VALUES('file-id',?,'keep.txt','file','objects/key',1,'ready',?,?)`, RootID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	a.store.objects["objects/key"] = storage.ObjectInfo{Size: 1}
	a.store.deleteErr = errors.New("S3 unavailable")
	rr := a.request("DELETE", "/api/files/file-id", nil, true)
	if rr.Code != 502 {
		t.Fatalf("delete=%d", rr.Code)
	}
	var status string
	if err := a.db.QueryRow(`SELECT status FROM files WHERE id='file-id'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "deleting" {
		t.Fatalf("status=%s", status)
	}
	a.store.deleteErr = nil
	retry := a.request("DELETE", "/api/files/file-id", nil, true)
	if retry.Code != http.StatusNoContent {
		t.Fatalf("delete retry=%d: %s", retry.Code, retry.Body.String())
	}
	if err := a.db.QueryRow(`SELECT status FROM files WHERE id='file-id'`).Scan(&status); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("metadata should be removed after retry, got %v", err)
	}
}
