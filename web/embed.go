// Package webassets exposes the production ACR Console bundle to the Go server.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var bundle embed.FS

func Dist() fs.FS {
	files, err := fs.Sub(bundle, "dist")
	if err != nil {
		panic(err)
	}
	return files
}
