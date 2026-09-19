//go:build !windows

package semantic

import "os"

func APIKey() string { return os.Getenv("TYPESAFE_API_KEY") }
