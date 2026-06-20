package lib

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/disintegration/imaging"
	"github.com/gen2brain/avif"
	"github.com/gin-gonic/gin"
	"github.com/supabase-community/supabase-go"
	_ "golang.org/x/image/webp"
	"google.golang.org/api/option"
	_ "modernc.org/sqlite"
)

const (
	DefaultSupabaseURL       = "https://uzlzkjuijruqanetagxh.supabase.co"
	DefaultSupabaseKey       = "sb_publishable_6_fvEEW8e1DNGvtVhXPzxw_h2i04w7b"
	DefaultOutputDir         = "./data/images"
	DefaultDBPath            = "./data/maid_pic.db"
	DefaultFCMCredentialFile = "./data/fcm_token.json"
	supabaseUserAgent        = "sukiApp_backend/nightly (contact:admin@wenwen12305.top)"
	DefaultListenAddr        = ":6988"
	DefaultQuality           = 85
	DefaultSpeed             = 8
	outputWidth              = 800
	outputHeight             = 450
	imageDownloadTimeout     = 180 * time.Second
	outputFilePermission     = 0o644
	defaultWorkers           = 8
	supabaseTableName        = "suki_booking"
)

type Config struct {
	SupabaseURL       string
	SupabaseKey       string
	OutputDir         string
	DBPath            string
	ListenAddr        string
	FCMCredentialFile string
	FCMProxy          string
	Quality           int
	Speed             int
	UpdateNow         bool
}

type App struct {
	cfg         Config
	db          *sql.DB
	supabase    *supabase.Client
	fcmClient   *messaging.Client
	imageClient *http.Client
}

type BookingRow struct {
	Maids     []Maid   `json:"maids"`
	TimeSlots []string `json:"time_slots"`
}

type Maid struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	VRCID    string   `json:"vrcid"`
	Tags     []string `json:"tags"`
	Disabled bool     `json:"disabled"`
}

type BookingEnabledRow struct {
	BookingEnabled bool `json:"booking_enabled"`
}

type TimeSlotsRow struct {
	TimeSlots []string `json:"time_slots"`
}

type MaidRecord struct {
	MaidID     string
	VRCID      string
	Name       string
	ImageURL   string
	ImageHash  string
	OutputFile string
	UpdatedAt  time.Time
}

func NewApp(cfg Config) (*App, error) {
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA synchronous = NORMAL`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	supa, err := supabase.NewClient(cfg.SupabaseURL, cfg.SupabaseKey, &supabase.ClientOptions{
		Schema: "public",
		Headers: map[string]string{
			"User-Agent": supabaseUserAgent,
		},
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	ctx := context.Background()
	fcmOptions := []option.ClientOption{option.WithCredentialsFile(cfg.FCMCredentialFile)}
	if strings.TrimSpace(cfg.FCMProxy) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(cfg.FCMProxy))
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("parse FCM proxy: %w", err)
		}
		proxyClient := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
		fcmOptions = append(fcmOptions, option.WithHTTPClient(proxyClient))
	}
	firebaseApp, err := firebase.NewApp(ctx, nil, fcmOptions...)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	fcmClient, err := firebaseApp.Messaging(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &App{
		cfg:         cfg,
		db:          db,
		supabase:    supa,
		fcmClient:   fcmClient,
		imageClient: &http.Client{Timeout: imageDownloadTimeout},
	}, nil
}

func (a *App) Close() error {
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func (a *App) Run() error {
	go a.startBookingPoller()
	go a.startDailyRefreshTokenPoller()
	return a.runServer()
}

func (a *App) SyncMaidImages() error {
	rows, err := a.fetchBookingRows()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("no booking rows returned")
	}

	pending := make([]Maid, 0, len(rows[0].Maids))
	for _, maid := range rows[0].Maids {
		if strings.TrimSpace(maid.VRCID) == "" {
			log.Printf("skip maid without vrcid: name=%q image=%q", maid.Name, maid.Image)
			continue
		}
		if strings.TrimSpace(maid.Image) == "" {
			log.Printf("skip maid without image: vrcid=%s name=%q", maid.VRCID, maid.Name)
			continue
		}

		record, err := a.getMaidRecord(maid.VRCID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && record.ImageURL == maid.Image {
			continue
		}
		pending = append(pending, maid)
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > defaultWorkers {
		workers = defaultWorkers
	}
	if workers > len(pending) {
		workers = len(pending)
	}

	type processResult struct {
		record MaidRecord
		err    error
	}

	if len(pending) == 0 {
		log.Printf("updated 0 maid(s)")
		return nil
	}

	jobs := make(chan Maid, len(pending))
	results := make(chan processResult, len(pending))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for maid := range jobs {
				log.Printf("image changed: vrcid=%s name=%q", maid.VRCID, maid.Name)
				record, err := a.processMaid(maid)
				results <- processResult{record: record, err: err}
			}
		}()
	}

	for _, maid := range pending {
		jobs <- maid
	}
	close(jobs)

	wg.Wait()
	close(results)

	changed := 0
	for res := range results {
		if res.err != nil {
			log.Printf("process failed: vrcid=%s err=%v", res.record.VRCID, res.err)
			continue
		}
		if err := a.upsertMaidRecord(res.record); err != nil {
			log.Printf("store failed: vrcid=%s err=%v", res.record.VRCID, err)
			continue
		}
		changed++
	}

	log.Printf("updated %d maid(s)", changed)
	return nil
}

func (a *App) fetchBookingRows() ([]BookingRow, error) {
	var rows []BookingRow
	_, err := a.supabase.From(supabaseTableName).Select("*", "", false).Limit(1, "").ExecuteTo(&rows)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (a *App) fetchBookingEnabled() (bool, bool, error) {
	var rows []BookingEnabledRow
	_, err := a.supabase.From(supabaseTableName).Select("booking_enabled", "", false).Limit(1, "").ExecuteTo(&rows)
	if err != nil {
		return false, false, err
	}
	if len(rows) == 0 {
		return false, false, nil
	}
	return rows[0].BookingEnabled, true, nil
}

func (a *App) fetchTimeSlots() ([]string, bool, error) {
	var rows []TimeSlotsRow
	_, err := a.supabase.From(supabaseTableName).Select("time_slots", "", false).Limit(1, "").ExecuteTo(&rows)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0].TimeSlots, true, nil
}

func (a *App) processMaid(maid Maid) (MaidRecord, error) {
	req, err := http.NewRequest(http.MethodGet, maid.Image, nil)
	if err != nil {
		return MaidRecord{}, err
	}
	resp, err := a.imageClient.Do(req)
	if err != nil {
		return MaidRecord{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return MaidRecord{}, fmt.Errorf("download status %s", resp.Status)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return MaidRecord{}, err
	}

	processed := imaging.Fill(img, outputWidth, outputHeight, imaging.Center, imaging.Lanczos)
	encodeInput := forceOpaqueRGBA(processed)

	encoded, err := encodeAVIF(encodeInput, a.cfg.Quality, a.cfg.Speed)
	if err != nil {
		return MaidRecord{}, err
	}
	if len(encoded) == 0 {
		return MaidRecord{}, errors.New("avif encoding produced empty output")
	}

	hash := sha1.Sum(encoded)
	shortHash := hex.EncodeToString(hash[:])[:8]
	baseName := fmt.Sprintf("%s-%s", sanitizeFileComponent(maid.VRCID), shortHash)
	fileName := baseName + ".avif"
	outPath := filepath.Join(a.cfg.OutputDir, fileName)

	if err := writeFileAtomic(outPath, encoded, outputFilePermission); err != nil {
		return MaidRecord{}, err
	}

	return MaidRecord{
		MaidID:     maid.ID,
		VRCID:      maid.VRCID,
		Name:       maid.Name,
		ImageURL:   maid.Image,
		ImageHash:  shortHash,
		OutputFile: fileName,
		UpdatedAt:  time.Now().UTC(),
	}, nil
}

func encodeAVIF(img image.Image, quality int, speed int) ([]byte, error) {
	var buf bytes.Buffer
	if err := avif.Encode(&buf, img, avif.Options{
		Quality:           quality,
		QualityAlpha:      quality,
		Speed:             speed,
		ChromaSubsampling: image.YCbCrSubsampleRatio420,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func forceOpaqueRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(bl >> 8),
				A: 255,
			})
		}
	}
	return dst
}

func initSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS maid_images (
			vrcid TEXT PRIMARY KEY,
			maid_id TEXT NOT NULL,
			name TEXT NOT NULL,
			image_url TEXT NOT NULL,
			image_hash TEXT NOT NULL,
			output_file TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS app_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sysbooking_sessions (
			user_id TEXT PRIMARY KEY,
			sb_refreshtoken TEXT NOT NULL,
			sb_token TEXT NOT NULL,
			fcm_token TEXT,
			notification_enabled INTEGER NOT NULL DEFAULT 0 CHECK(notification_enabled IN (0, 1)),
			token_valid INTEGER NOT NULL DEFAULT 1 CHECK(token_valid IN (0, 1)),
			token TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sysbooking_bookings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			booking_id TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			maid_id TEXT NOT NULL,
			timeslot INTEGER NOT NULL CHECK(timeslot IN (21, 22)),
			autoqueue INTEGER NOT NULL CHECK(autoqueue IN (0, 1)),
			with_friend INTEGER NOT NULL DEFAULT 0 CHECK(with_friend IN (0, 1)),
			friend_vrcid TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sysbooking_bookings_queue
		 ON sysbooking_bookings (maid_id, status, created_at, id);`,
		`CREATE INDEX IF NOT EXISTS idx_sysbooking_bookings_user
		 ON sysbooking_bookings (user_id, status);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) getMaidRecord(vrcid string) (MaidRecord, error) {
	row := a.db.QueryRow(
		`SELECT maid_id, vrcid, name, image_url, image_hash, output_file, updated_at
		 FROM maid_images WHERE vrcid = ?`,
		vrcid,
	)
	var record MaidRecord
	var updatedAt string
	if err := row.Scan(
		&record.MaidID,
		&record.VRCID,
		&record.Name,
		&record.ImageURL,
		&record.ImageHash,
		&record.OutputFile,
		&updatedAt,
	); err != nil {
		return MaidRecord{}, err
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	return record, nil
}

func (a *App) upsertMaidRecord(record MaidRecord) error {
	_, err := a.db.Exec(
		`INSERT INTO maid_images (vrcid, maid_id, name, image_url, image_hash, output_file, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(vrcid) DO UPDATE SET
		   maid_id=excluded.maid_id,
		   name=excluded.name,
		   image_url=excluded.image_url,
		   image_hash=excluded.image_hash,
		   output_file=excluded.output_file,
		   updated_at=excluded.updated_at`,
		record.VRCID,
		record.MaidID,
		record.Name,
		record.ImageURL,
		record.ImageHash,
		record.OutputFile,
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (a *App) setAppBool(key string, value bool) error {
	_, err := a.db.Exec(
		`INSERT INTO app_state (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key,
		boolToString(value),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (a *App) getAppBool(key string) (bool, bool, error) {
	row := a.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	return value == "1", true, nil
}

func boolToString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (a *App) manifestMap() (map[string]string, error) {
	rows, err := a.db.Query(`SELECT vrcid, image_hash FROM maid_images ORDER BY vrcid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	manifest := make(map[string]string)
	for rows.Next() {
		var vrcid, hash string
		if err := rows.Scan(&vrcid, &hash); err != nil {
			return nil, err
		}
		if strings.TrimSpace(vrcid) == "" || strings.TrimSpace(hash) == "" {
			continue
		}
		manifest[vrcid] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (a *App) serveManifest(c *gin.Context) {
	manifest, err := a.manifestMap()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, manifest)
}

func (a *App) serveImage(c *gin.Context) {
	name := filepath.Base(c.Param("file"))
	if !strings.HasSuffix(strings.ToLower(name), ".avif") {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, s-maxage=31536000, immutable")
	c.File(filepath.Join(a.cfg.OutputDir, name))
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sanitizeFileComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func init() {
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("jpg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("png", "\x89PNG\r\n\x1a\n", png.Decode, png.DecodeConfig)
	image.RegisterFormat("gif", "GIF8", gif.Decode, gif.DecodeConfig)
}
