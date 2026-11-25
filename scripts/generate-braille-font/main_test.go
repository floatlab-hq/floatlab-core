package main

import (
	"bytes"
	"os"
	"testing"
)

func TestFontMatchesInstallerAsset(t *testing.T) {
	want, err := os.ReadFile("../../floatlab-appliance-image/fonts/FloatLabBraille-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if got := font(); !bytes.Equal(got, want) {
		t.Fatal("generated font differs from the checked-in installer asset")
	}
}
