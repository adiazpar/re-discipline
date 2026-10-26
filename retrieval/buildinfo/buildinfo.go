// Package buildinfo describes the exact plugin payload, independently of its
// release number. Values are stamped by scripts/build.go, never from a server.
package buildinfo

import (
	"strconv"
	"strings"
)

var Version = "dev"
var ID = "dev"
var Schema = "dev"

const Protocol = 1

type Info struct {
	Version  string `json:"version"`
	Build    string `json:"build"`
	Schema   string `json:"schema"`
	Protocol int    `json:"protocol"`
}

func Current() Info { return Info{Version, ID, Schema, Protocol} }

// Compare accepts released x.y.z versions only; dev builds have no ordering.
func Compare(a, b string) int {
	parse := func(s string) ([3]int, bool) {
		var out [3]int
		parts := strings.Split(s, ".")
		if len(parts) != 3 {
			return out, false
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	x, ok := parse(a)
	if !ok {
		return 0
	}
	y, ok := parse(b)
	if !ok {
		return 0
	}
	for i := range x {
		if x[i] > y[i] {
			return 1
		}
		if x[i] < y[i] {
			return -1
		}
	}
	return 0
}
