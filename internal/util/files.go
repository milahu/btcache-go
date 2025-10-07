package util

import (
	"path/filepath"
)

func SafeJoin(parts ...string) string {
	return filepath.Join(parts...)
}
