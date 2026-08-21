package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedAssets(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/favicon.svg", "/logo.png"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rr.Code)
		}
	}
	// FileServer 把 /index.html 规范重定向到 /（标准行为）
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("/index.html status=%d, want 301", rr.Code)
	}
}

func TestHandlerCachesHashedAssetsImmutably(t *testing.T) {
	// 用真实的构建产物文件名（带内容哈希）
	matches, err := fs.Glob(assets, "dist/assets/*.js")
	if err != nil || len(matches) == 0 {
		t.Fatal("no built assets found; run npm run build first")
	}
	path := "/assets/" + filepath.Base(matches[0])
	h := Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("asset %s status=%d", path, rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("assets cache-control=%q, want immutable", cc)
	}
	// 非 assets 路径不加 immutable
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	h.ServeHTTP(rr2, req2)
	if cc := rr2.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Fatalf("favicon cache-control=%q must not be immutable", cc)
	}
}

func TestHandlerSPAFallback(t *testing.T) {
	h := Handler()
	// 前端路由（/f/xxx、/read/xxx）等未知路径回退到 index.html
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/f/abc-123", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("spa fallback status=%d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("fallback content-type=%q", ct)
	}
}

func TestHandlerRejectsTraversal(t *testing.T) {
	h := Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/../go.mod", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		// 不返回错误即可；绝不能返回 go.mod 内容
		return
	}
	if strings.Contains(rr.Body.String(), "module github.com/VesperGlow/revaro") {
		t.Fatal("path traversal leaked file contents")
	}
}
