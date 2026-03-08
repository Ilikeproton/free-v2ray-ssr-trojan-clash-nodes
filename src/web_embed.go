package main

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var embeddedWebFS embed.FS

var embeddedWebSubFS fs.FS
var embeddedIndexHTML []byte

func init() {
	sub, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		panic(err)
	}
	embeddedWebSubFS = sub

	indexHTML, err := fs.ReadFile(embeddedWebSubFS, "index.html")
	if err != nil {
		panic(err)
	}
	embeddedIndexHTML = indexHTML
}
