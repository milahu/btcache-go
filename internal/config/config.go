package storage

import (
	"errors"
)

// Config is intentionally small here. Extend as needed.
type Config struct {
	BackendBaseURL string
	BackendUser    string
	BackendPass    string
	QBURL          string
	QBUser         string
	QBPass         string
	CacheDBPath    string
	MaxCacheBytes  int64
}

func LoadConfig(path string) (*Config, error) {
	// For the skeleton we read values from environment or return a default
	// In future: parse YAML/JSON
	// TODO: implement full config parsing. For now return a sample default to allow running small tests.
	return &Config{
		BackendBaseURL: "https://some-username:some-password@backend-server/cas/btih",
		BackendUser:    "some-username",
		BackendPass:    "some-password",
		QBURL:          "http://backend-server:8080",
		QBUser:         "",
		QBPass:         "",
		CacheDBPath:    "cache.db",
		MaxCacheBytes:  1 << 40, // 1TB
	}, nil
}
