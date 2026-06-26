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
	"github.com/gen2brain/avif"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	DefaultReversePicCacheDir = "./data/reverse-pic-cache"
	reversePicMaxEdge         = 1000
	reversePicMaxEntries      = 1000
)

type reversePicCache struct {
	dir string
	mu  sync.Mutex
	grp singleflight.Group
}

type reversePicCacheEntry struct {
	path    string
	size    int64
	modTime time.Time
}

func newReversePicCache(dir string) (*reversePicCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &reversePicCache{dir: dir}, nil
}

func (c *reversePicCache) pathForKey(key string) string {
	return filepath.Join(c.dir, key+".avif")
}

func (c *reversePicCache) loadIfExists(key string) (string, bool, error) {
	path := c.pathForKey(key)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	_ = os.Chtimes(path, time.Now(), time.Now())
	return path, true, nil
}

func (c *reversePicCache) store(key string, data []byte) (string, error) {
	path := c.pathForKey(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		return "", err
	}
	if err := c.pruneLocked(); err != nil {
		return "", err
	}
	return path, nil
}

func (c *reversePicCache) pruneLocked() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	files := make([]reversePicCacheEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, reversePicCacheEntry{
			path:    filepath.Join(c.dir, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	if len(files) <= reversePicMaxEntries {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	for i := 0; i < len(files)-reversePicMaxEntries; i++ {
		_ = os.Remove(files[i].path)
	}
	return nil
}

func normalizeReversePicTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("missing target")
	}
	raw = strings.TrimPrefix(raw, "/")

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid target url: %w", err)
	}
	if !u.IsAbs() || !strings.EqualFold(u.Scheme, "https") || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("target must be an absolute https url")
	}

	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String(), nil
}

func reversePicCacheKey(normalized string) string {
	h := sha1.Sum([]byte(normalized))
	return hex.EncodeToString(h[:])
}

func isReversePicOriginAllowed(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}

	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return false
	}

	host := strings.ToLower(u.Hostname())
	switch {
	case strings.EqualFold(u.Scheme, "https") && host == "suki.wenwen12305.top":
		return u.Port() == "" || u.Port() == "443"
	case strings.EqualFold(u.Scheme, "http") && (host == "localhost" || host == "127.0.0.1" || host == "::1"):
		return true
	default:
		return false
	}
}

func isReversePicPath(path string) bool {
	return strings.Contains(path, "/reverse-pic/") || strings.HasSuffix(path, "/reverse-pic")
}

func reversePicCORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		path := c.Request.URL.Path

		if origin == "" {
			c.Next()
			return
		}

		if isReversePicPath(path) {
			if !isReversePicOriginAllowed(origin) {
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
			h.Set("Access-Control-Allow-Headers", "Content-Type, User-Agent, Accept, Range, Origin")
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

func (a *App) handleReversePic(c *gin.Context) {
	major, ok := parseReversePicMajorVersion(c.GetHeader("sukiApp-version"))
	if !ok || major <= reversePicMinMajorVersion {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid sukiApp-version"})
		return
	}

	normalized, err := normalizeReversePicTarget(c.Param("target"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key := reversePicCacheKey(normalized)
	if path, ok, err := a.reversePicCache.loadIfExists(key); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} else if ok {
		serveReversePicFile(c, path)
		return
	}

	result, err, _ := a.reversePicCache.grp.Do(key, func() (any, error) {
		if path, ok, err := a.reversePicCache.loadIfExists(key); err != nil {
			return nil, err
		} else if ok {
			return path, nil
		}

		data, err := a.buildReversePic(normalized)
		if err != nil {
			return nil, err
		}
		path, err := a.reversePicCache.store(key, data)
		if err != nil {
			return nil, err
		}
		return path, nil
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errReversePicUnsupportedMediaType) {
			status = http.StatusUnsupportedMediaType
		} else if errors.Is(err, errReversePicBadTarget) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	path, _ := result.(string)
	if path == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reverse pic cache path missing"})
		return
	}
	serveReversePicFile(c, path)
}

var (
	errReversePicBadTarget            = errors.New("invalid reverse pic target")
	errReversePicUnsupportedMediaType = errors.New("unsupported remote image content-type")
	reversePicMinMajorVersion         = 1
)

func (a *App) buildReversePic(normalized string) ([]byte, error) {
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
		return nil, errReversePicBadTarget
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, errReversePicUnsupportedMediaType
	}
	if !isReversePicSupportedMediaType(mediaType) {
		return nil, errReversePicUnsupportedMediaType
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
	if width > reversePicMaxEdge || height > reversePicMaxEdge {
		newWidth, newHeight := scaledDimensions(width, height, reversePicMaxEdge)
		processed = imaging.Resize(img, newWidth, newHeight, imaging.Lanczos)
	}

	var out bytes.Buffer
	if err := avif.Encode(&out, processed, avif.Options{
		Quality:           a.cfg.Quality,
		QualityAlpha:      a.cfg.Quality,
		Speed:             a.cfg.Speed,
		ChromaSubsampling: image.YCbCrSubsampleRatio420,
	}); err != nil {
		return nil, err
	}
	encoded := out.Bytes()
	if len(encoded) == 0 {
		return nil, errors.New("avif encoding produced empty output")
	}
	return encoded, nil
}

func parseReversePicMajorVersion(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	major := 0
	seenDigit := false
	for _, r := range raw {
		if r < '0' || r > '9' {
			break
		}
		seenDigit = true
		major = major*10 + int(r-'0')
	}
	return major, seenDigit
}

func isReversePicSupportedMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp":
		return true
	default:
		return false
	}
}

func scaledDimensions(width, height, maxEdge int) (int, int) {
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

func serveReversePicFile(c *gin.Context, path string) {
	if info, err := os.Stat(path); err == nil {
		c.Header("Content-Type", "image/avif")
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("Content-Length", fmt.Sprintf("%d", info.Size()))
		c.File(path)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "cache miss"})
}
