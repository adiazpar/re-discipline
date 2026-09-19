package semantic

import (
	"golang.org/x/sys/windows/registry"
	"os"
	"strings"
)

func APIKey() string {
	if key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); key != "" {
		return key
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	key, _, _ := k.GetStringValue("TYPESAFE_API_KEY")
	return strings.TrimSpace(key)
}
