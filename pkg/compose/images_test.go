package compose

import (
	"strings"
	"testing"
)

func TestUpdateImages(t *testing.T) {
	updated, err := UpdateImages("services:\n  web:\n    image: old:v1\n", map[string]string{"web": "new:v2"})
	if err != nil || !strings.Contains(updated, "image: new:v2") {
		t.Fatalf("UpdateImages() = %q, %v", updated, err)
	}
	if _, err := UpdateImages(updated, map[string]string{"missing": "new:v2"}); err == nil {
		t.Fatal("UpdateImages accepted an unknown service")
	}
}
