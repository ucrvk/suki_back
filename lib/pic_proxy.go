package lib

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	DefaultPicProxyCacheDir = "./data/pic-proxy-cache"
	picProxyMaxEntries      = 1000
	picProxyMaxEdge         = 900
)

var (
	errPicProxyBadTarget            = errors.New("target must be an absolute https url")
	errPicProxyUnsupportedMediaType = errors.New("unsupported remote image content-type")
)

type picProxyCache struct {
	dir string
	mu  sync.Mutex
	grp singleflight.Group
}

type picProxyCacheEntry struct {
	path    string
	modTime time.Time
}

func newPicProxyCache(dir string) (*picProxyCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &picProxyCache{dir: dir}, nil
}

func (c *picProxyCache) pathForKey(key string) string {
	return filepath.Join(c.dir, key+".avif")
}

func (c *picProxyCache) loadIfExists(key string) (string, bool, error) {
	path := c.pathForKey(key)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return path, true, nil
}

func (c *picProxyCache) store(key string, data []byte) (string, error) {
	path := c.pathForKey(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := writeFileAtomic(path, data, outputFilePermission); err != nil {
		return "", err
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return "", err
	}
	if err := c.pruneLocked(); err != nil {
		return "", err
	}
	return path, nil
}

func (c *picProxyCache) pruneLocked() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}

	files := make([]picProxyCacheEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, picProxyCacheEntry{
			path:    filepath.Join(c.dir, entry.Name()),
			modTime: info.ModTime(),
		})
	}

	if len(files) <= picProxyMaxEntries {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	for i := 0; i < len(files)-picProxyMaxEntries; i++ {
		_ = os.Remove(files[i].path)
	}
	return nil
}

func picProxyCacheKey(normalized string) string {
	sum := sha1.Sum([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func normalizePicProxyTarget(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	if raw == "" {
		return "", errors.New("missing target")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid target url: %w", err)
	}
	if !u.IsAbs() || !strings.EqualFold(u.Scheme, "https") || strings.TrimSpace(u.Host) == "" {
		return "", errPicProxyBadTarget
	}

	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String(), nil
}

func isPicProxyPath(path string) bool {
	return strings.Contains(path, "/pic-proxy/") || strings.HasSuffix(path, "/pic-proxy")
}

func isPicProxyOriginAllowed(origin string) bool {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Hostname() == "" {
		return false
	}

	host := strings.ToLower(u.Hostname())
	switch {
	case strings.EqualFold(u.Scheme, "https") && host == "suki.wenwen12305.top":
		return u.Port() == "" || u.Port() == "443"
	case strings.EqualFold(u.Scheme, "http") && (host == "localhost" || host == "127.0.0.1"):
		return true
	default:
		return false
	}
}

func appCORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		path := c.Request.URL.Path

		if isPicProxyPath(path) && origin != "" {
			if !isPicProxyOriginAllowed(origin) {
				if c.Request.Method == http.MethodOptions {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
				c.Next()
				return
			}

			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, User-Agent, sukiApp-version, Accept, Range, Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Type")
			h.Set("Vary", "Origin")
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Expose-Headers", "*")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (a *App) handlePicProxy(c *gin.Context) {
	normalized, err := normalizePicProxyTarget(c.Param("target"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key := picProxyCacheKey(normalized)
	if path, ok, err := a.picProxyCache.loadIfExists(key); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} else if ok {
		servePicProxyFile(c, path)
		return
	}

	result, err, _ := a.picProxyCache.grp.Do(key, func() (any, error) {
		if path, ok, err := a.picProxyCache.loadIfExists(key); err != nil {
			return nil, err
		} else if ok {
			return path, nil
		}

		data, err := a.buildPicProxy(normalized)
		if err != nil {
			return nil, err
		}
		return a.picProxyCache.store(key, data)
	})
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errPicProxyBadTarget):
			status = http.StatusBadRequest
		case errors.Is(err, errPicProxyUnsupportedMediaType):
			status = http.StatusUnsupportedMediaType
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	path, _ := result.(string)
	if path == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pic proxy cache path missing"})
		return
	}
	servePicProxyFile(c, path)
}

func (a *App) buildPicProxy(normalized string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, normalized, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.imageClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("download status %s", resp.Status)
	}
	if resp.Request != nil && resp.Request.URL != nil && !strings.EqualFold(resp.Request.URL.Scheme, "https") {
		return nil, errPicProxyBadTarget
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, errPicProxyUnsupportedMediaType
	}
	if !isPicProxySupportedMediaType(mediaType, normalized) {
		return nil, errPicProxyUnsupportedMediaType
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, errors.New("invalid image dimensions")
	}

	processed := img
	if width > picProxyMaxEdge || height > picProxyMaxEdge {
		newWidth, newHeight := scaleToMaxEdge(width, height, picProxyMaxEdge)
		processed = imaging.Resize(img, newWidth, newHeight, imaging.Lanczos)
	}

	encoded, err := encodeAVIF(forceOpaqueRGBA(processed), a.cfg.Quality, a.cfg.Speed)
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 {
		return nil, errors.New("avif encoding produced empty output")
	}
	return bytes.Clone(encoded), nil
}

func isPicProxySupportedMediaType(mediaType string, normalized string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp":
		return true
	case "application/octet-stream":
		return picProxyHasAllowedExtension(normalized)
	default:
		return false
	}
}

func picProxyHasAllowedExtension(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".webp")
}

func scaleToMaxEdge(width, height, maxEdge int) (int, int) {
	if width <= 0 || height <= 0 || maxEdge <= 0 {
		return width, height
	}
	if width >= height {
		newWidth := maxEdge
		newHeight := int((float64(height) * float64(maxEdge)) / float64(width))
		if newHeight < 1 {
			newHeight = 1
		}
		return newWidth, newHeight
	}

	newHeight := maxEdge
	newWidth := int((float64(width) * float64(maxEdge)) / float64(height))
	if newWidth < 1 {
		newWidth = 1
	}
	return newWidth, newHeight
}

func servePicProxyFile(c *gin.Context, path string) {
	info, err := os.Stat(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "cache miss"})
		return
	}
	c.Header("Content-Type", "image/avif")
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Length", fmt.Sprintf("%d", info.Size()))
	c.File(path)
}
