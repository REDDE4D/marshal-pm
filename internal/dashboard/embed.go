package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// staticFS returns the embedded SPA build rooted at the dist directory.
func staticFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail
	}
	return sub
}
