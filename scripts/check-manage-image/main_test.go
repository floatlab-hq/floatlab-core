package main

import (
	"strings"
	"testing"
)

func TestReadAssets(t *testing.T) {
	page := `<div id="app"></div><script src="/app.js"></script><script src="//example.com/app.js"></script><link href="/app.css" rel="stylesheet">`
	assets, err := readAssets(strings.NewReader(page))
	if err != nil || len(assets) != 2 || assets[0] != (asset{"/app.js", "javascript"}) || assets[1] != (asset{"/app.css", "text/css"}) {
		t.Fatalf("assets = %v, error = %v", assets, err)
	}
	if _, err := readAssets(strings.NewReader(`<div id="app"></div>`)); err == nil {
		t.Fatal("accepted UI without assets")
	}
}
