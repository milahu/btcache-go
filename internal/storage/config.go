package storage

type Config struct {
	CacheDBPath     string
	MaxCacheBytes   int64
	BackendBaseURL  string
	BackendUser     string
	BackendPass     string
	QBURL           string
	QBUser          string
	QBPass          string
}

func LoadConfig(path string) (*Config, error) {
	// minimal: load defaults or read from yaml/env
	return &Config{
		CacheDBPath:    "./cache.db",
		MaxCacheBytes:  1 << 40, // 1TB
		BackendBaseURL: "https://user:pass@backend-server",
		QBURL:          "http://localhost:8080",
		QBUser:         "admin",
		QBPass:         "admin",
	}, nil
}
