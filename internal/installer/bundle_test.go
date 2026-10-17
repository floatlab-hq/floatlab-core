package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallContainerBundle(t *testing.T) {
	media, system := t.TempDir(), t.TempDir()
	data := []byte("verified image")
	hash := sha256.Sum256(data)
	if err := os.MkdirAll(filepath.Join(media, "images"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "images", "manage.tar"), data, 0644); err != nil {
		t.Fatal(err)
	}
	p := Payload{Files: map[string]string{"images/manage.tar": hex.EncodeToString(hash[:])}}
	if err := installContainerBundle(media, system, p); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(system, "images", "manage.tar")); err != nil || string(got) != string(data) {
		t.Fatalf("%q, %v", got, err)
	}
	p.Files["images/manage.tar"] = "bad"
	if err := installContainerBundle(media, system, p); err == nil {
		t.Fatal("accepted corrupt image")
	}
	p.Files = map[string]string{"etc/appliance.json": "bad"}
	if err := installContainerBundle(media, system, p); err == nil {
		t.Fatal("accepted unexpected file")
	}
}
