// Package ui is the page: plain HTML and ES modules, no bundler. The Wails
// window and virt-mm serve show the same files (docs/adr/0003).
package ui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// Assets is the page's files, index.html at the root.
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
