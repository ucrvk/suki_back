package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"suki_back/lib"
)

func main() {
	var cfg lib.Config
	cfg.FCMCredentialFile = lib.DefaultFCMCredentialFile
	flag.StringVar(&cfg.SupabaseURL, "url", lib.DefaultSupabaseURL, "Supabase REST base URL")
	flag.StringVar(&cfg.SupabaseKey, "apikey", lib.DefaultSupabaseKey, "Supabase anon/public API key")
	flag.StringVar(&cfg.OutputDir, "out", lib.DefaultOutputDir, "output directory for AVIF files")
	flag.StringVar(&cfg.DBPath, "db", lib.DefaultDBPath, "sqlite database path")
	flag.StringVar(&cfg.ListenAddr, "listen", lib.DefaultListenAddr, "gin server listen address")
	flag.BoolVar(&cfg.UpdateNow, "u", false, "run one maid image sync on startup")
	flag.IntVar(&cfg.Quality, "quality", lib.DefaultQuality, "AVIF quality 0-100")
	flag.IntVar(&cfg.Speed, "speed", lib.DefaultSpeed, "AVIF speed 0-10")
	flag.Parse()

	lib.ApplyEnvOverrides(&cfg)

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

	app, err := lib.NewApp(cfg)
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
