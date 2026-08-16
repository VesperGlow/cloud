package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VesperGlow/cloud/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// 持久化缩略图：图片与 EPUB 封面由服务端按需生成、视频由前端抽帧后上传，
// 统一存入 S3 的 thumbs/ 前缀（内容寻址、条件写入），前端用带 etag 的
// 不可变 URL 请求，浏览器可长期缓存，刷新/重进目录不再重新加载。

const maxThumbBytes = 512 << 10 // 缩略图对象上限
const maxThumbSource = 64 << 20 // 生成缩略图时允许读取的源文件上限
const thumbMaxDim = 480         // 缩略图最长边

// thumbKey 由清单键（内容哈希）派生，内容不变则缩略图键不变。
func (s *Server) thumbKey(f File) string {
	sum := sha256.Sum256([]byte(f.objectKey + "|thumb"))
	id := hex.EncodeToString(sum[:])
	return "thumbs/" + id[:2] + "/" + id[2:] + ".jpg"
}

func serveThumb(w http.ResponseWriter, r *http.Request, data []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+hexSHA256(data)+`"`)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// thumbnail GET：已有缩略图直接返回（长期缓存）；否则按类型生成一次并落盘。
func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, http.StatusNotFound, "ready file not found")
		return
	}
	key := s.thumbKey(f)
	if data, err := s.storage.GetObject(r.Context(), key, maxThumbBytes); err == nil {
		serveThumb(w, r, data)
		return
	}
	data, ok := s.generateThumb(r.Context(), f)
	if !ok {
		problem(w, http.StatusNotFound, "no thumbnail available")
		return
	}
	_ = s.storage.PutImmutable(r.Context(), key, "image/jpeg", data)
	serveThumb(w, r, data)
}

func (s *Server) generateThumb(ctx context.Context, f File) ([]byte, bool) {
	switch strings.ToLower(filepath.Ext(f.Name)) {
	case ".epub":
		book, err := s.loadBook(ctx, f)
		if err != nil || len(book.Cover) == 0 {
			return nil, false
		}
		if resized, err := resizeToJPEG(book.Cover, thumbMaxDim); err == nil {
			return resized, true
		}
		// 封面解码不了（如 SVG）就直接用原始字节，至少是可缓存的稳定 URL
		return book.Cover, true
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		var raw []byte
		var err error
		if storage.IsManifestKey(f.objectKey) {
			raw, err = s.storage.ReadFile(ctx, f.objectKey, maxThumbSource)
		} else {
			raw, err = s.storage.GetObject(ctx, f.objectKey, maxThumbSource)
		}
		if err != nil || len(raw) == 0 {
			return nil, false
		}
		resized, err := resizeToJPEG(raw, thumbMaxDim)
		if err != nil {
			return nil, false
		}
		return resized, true
	}
	return nil, false
}

// saveThumbnail PUT：接收前端抽帧生成的视频缩略图（小 JPEG），落盘到
// 内容寻址的 thumbs/ 对象，之后的请求直接命中。
func (s *Server) saveThumbnail(w http.ResponseWriter, r *http.Request) {
	f, err := s.file(r.Context(), chi.URLParam(r, "id"))
	if err != nil || f.Kind != "file" || f.Status != "ready" {
		problem(w, http.StatusNotFound, "ready file not found")
		return
	}
	if r.ContentLength > maxThumbBytes {
		problem(w, http.StatusRequestEntityTooLarge, "thumbnail is too large")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxThumbBytes+1))
	if err != nil || len(data) > maxThumbBytes {
		problem(w, http.StatusBadRequest, "thumbnail data is invalid")
		return
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
		problem(w, http.StatusBadRequest, "thumbnail must be a JPEG image")
		return
	}
	if err := s.storage.PutImmutable(r.Context(), s.thumbKey(f), "image/jpeg", data); err != nil {
		s.log.Warn("thumbnail upload failed", "file", f.ID, "error", err)
		problem(w, http.StatusBadGateway, "could not store thumbnail")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resizeToJPEG 解码任意受支持的图片并把最长边缩到 maxDim，输出 JPEG。
func resizeToJPEG(data []byte, maxDim int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, errEmptyImage
	}
	if width <= maxDim && height <= maxDim {
		return encodeJPEG(src)
	}
	longest := width
	if height > longest {
		longest = height
	}
	scale := float64(maxDim) / float64(longest)
	dw := maxInt(1, int(float64(width)*scale))
	dh := maxInt(1, int(float64(height)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Over, nil)
	return encodeJPEG(dst)
}

var errEmptyImage = errors.New("empty image")

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
