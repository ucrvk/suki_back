package lib

import (
	"os"
	"strconv"
	"strings"
)

const (
	envPrefix = "MAIDPIC_"

	envSupabaseURL = envPrefix + "SUPABASE_URL"
	envSupabaseKey = envPrefix + "SUPABASE_API_KEY"
	envOutputDir   = envPrefix + "OUTPUT_DIR"
	envDBPath      = envPrefix + "DB_PATH"
	envListenAddr  = envPrefix + "LISTEN_ADDR"
	envQuality     = envPrefix + "QUALITY"
	envSpeed       = envPrefix + "SPEED"
	envUpdateNow   = envPrefix + "UPDATE_NOW"
	envFCMProxy    = "FCM_PROXY"
)

func ApplyEnvOverrides(cfg *Config) {
	cfg.SupabaseURL = firstNonEmpty(
		readEnv(envSupabaseURL),
		readEnv("SUPABASE_URL"),
		cfg.SupabaseURL,
	)
	cfg.SupabaseKey = firstNonEmpty(
		readEnv(envSupabaseKey),
		readEnv("SUPABASE_API_KEY"),
		cfg.SupabaseKey,
	)
	cfg.OutputDir = firstNonEmpty(
		readEnv(envOutputDir),
		cfg.OutputDir,
	)
	cfg.DBPath = firstNonEmpty(
		readEnv(envDBPath),
		cfg.DBPath,
	)
	cfg.ListenAddr = firstNonEmpty(
		readEnv(envListenAddr),
		cfg.ListenAddr,
	)
	if v, ok := readEnvInt(envQuality); ok {
		cfg.Quality = v
	}
	if v, ok := readEnvInt(envSpeed); ok {
		cfg.Speed = v
	}
	if v, ok := readEnvBool(envUpdateNow); ok {
		cfg.UpdateNow = v
	}
	if v := readEnv(envFCMProxy); v != "" {
		cfg.FCMProxy = v
	}
}

func readEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func readEnvInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func readEnvBool(key string) (bool, bool) {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "t", "true", "yes", "y", "on":
		return true, true
	case "0", "f", "false", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}
