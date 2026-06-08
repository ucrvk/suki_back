package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
)

func main() {
	var cfg Config
	cfg.FCMCredentialFile = defaultFCMCredentialFile
	flag.StringVar(&cfg.SupabaseURL, "url", defaultSupabaseURL, "Supabase REST base URL")
	flag.StringVar(&cfg.SupabaseKey, "apikey", defaultSupabaseKey, "Supabase anon/public API key")
	flag.StringVar(&cfg.OutputDir, "out", defaultOutputDir, "output directory for AVIF files")
	flag.StringVar(&cfg.DBPath, "db", defaultDBPath, "sqlite database path")
	flag.StringVar(&cfg.ListenAddr, "listen", defaultListenAddr, "gin server listen address")
	flag.BoolVar(&cfg.UpdateNow, "u", false, "run one maid image sync on startup")
	flag.IntVar(&cfg.Quality, "quality", defaultQuality, "AVIF quality 0-100")
	flag.IntVar(&cfg.Speed, "speed", defaultSpeed, "AVIF speed 0-10")
	flag.Parse()

	applyEnvOverrides(&cfg)

	if cfg.SupabaseKey == "" {
		log.Fatal("missing apikey")
	}
	if cfg.SupabaseURL == "" {
		log.Fatal("missing supabase url")
	}

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		log.Fatalf("create db dir: %v", err)
	}

	app, err := newApp(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer app.Close()

	if cfg.UpdateNow {
		if err := app.SyncMaidImages(); err != nil {
			log.Fatalf("one-shot sync failed: %v", err)
		}
		log.Printf("one-shot sync completed")
		return
	}

	if err := app.Run(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
