package web

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var staticFS embed.FS

// StaticFS returns the embedded console assets rooted at the static directory.
// The returned filesystem is read-only and can be served directly by an
// http.FileServer.
func StaticFS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}
