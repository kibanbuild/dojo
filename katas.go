// Package dojo embeds the kata catalog shipped with the CLI.
package dojo

import (
	"embed"
	"io/fs"
)

// Only what the CLI runs is embedded: solutions and wrong fixes never reach the binary.
//
//go:embed katas/*/*/kata.yaml katas/*/*/setup.sh katas/*/*/check.sh
var embedded embed.FS

// Katas returns the embedded catalog rooted at the katas directory.
func Katas() fs.FS {
	sub, err := fs.Sub(embedded, "katas")
	if err != nil {
		panic(err)
	}
	return sub
}
