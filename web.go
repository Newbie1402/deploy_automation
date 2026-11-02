package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

func webHandler() http.Handler {
	files, _ := fs.Sub(webAssets, "web")
	return http.FileServer(http.FS(files))
}
