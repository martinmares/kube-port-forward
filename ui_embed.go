package main

import (
	"embed"
	"io/fs"
)

var (
	//go:embed ui/index.html
	indexHTML []byte

	//go:embed ui/static
	embeddedUI embed.FS

	uiStaticFS = mustSubFS(embeddedUI, "ui/static")
)

func mustSubFS(root fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
