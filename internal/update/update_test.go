package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockExcludesOtherCoordinators(t *testing.T) {
	dir := t.TempDir()
	unlock, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := Acquire(dir); e == nil {
		other()
		t.Fatal("concurrent lock acquired")
	}
	unlock()
	other, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	other()
}
func TestDurablePrivateJournalReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if e := WriteJSON(path, map[string]string{"phase": "restoring"}); e != nil {
		t.Fatal(e)
	}
	if e := WriteJSON(path, map[string]string{"phase": "committed"}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "{\n  \"phase\": \"committed\"\n}\n" {
		t.Fatal(string(b))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
