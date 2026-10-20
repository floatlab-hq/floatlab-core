package managementupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ownedImage = regexp.MustCompile(`^floatlab-rollback/(floatlab-control|rqlite|victoria-metrics|victoria-logs):[a-f0-9]{64}$`)

// Release only updater-created tags. Registry tags and images referenced by workloads are preserved.
func (l *Linux) releaseImages(ctx context.Context, obsolete *Status) error {
	candidates, e := composeImages(obsolete.PreviousCompose)
	if e != nil {
		return e
	}
	protected := map[string]bool{}
	protect := func(b []byte) error {
		refs, e := composeImages(b)
		if e != nil {
			return e
		}
		for _, ref := range refs {
			id, e := l.run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
			if e != nil {
				return e
			}
			protected[id] = true
		}
		return nil
	}
	if b, e := os.ReadFile(composePath); e == nil {
		if e = protect(b); e != nil {
			return e
		}
	} else {
		return e
	}
	entries, e := os.ReadDir(l.Dir)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() != "management.json" && !(strings.HasPrefix(entry.Name(), "management-os-") && strings.HasSuffix(entry.Name(), ".json")) {
			continue
		}
		b, e := os.ReadFile(filepath.Join(l.Dir, entry.Name()))
		if e != nil {
			return e
		}
		var retained Status
		if e = json.Unmarshal(b, &retained); e != nil {
			return e
		}
		if e = protect(retained.PreviousCompose); e != nil {
			return e
		}
		if e = protect(retained.CandidateCompose); e != nil {
			return e
		}
	}
	for _, ref := range candidates {
		if !ownedImage.MatchString(ref) {
			continue
		}
		id := "sha256:" + ref[strings.LastIndex(ref, ":")+1:]
		if protected[id] {
			continue
		}
		containers, e := l.run(ctx, "docker", "ps", "-aq", "--filter", "ancestor="+id)
		if e != nil {
			return e
		}
		if strings.TrimSpace(containers) != "" {
			continue
		}
		actual, e := l.run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
		if e != nil {
			continue
		}
		if actual != id {
			return fmt.Errorf("retained image tag identity changed")
		}
		if _, e = l.run(ctx, "docker", "image", "rm", ref); e != nil {
			return e
		}
	}
	return nil
}
func composeImages(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	doc, e := composeDocument(b)
	if e != nil {
		return nil, e
	}
	var refs []string
	for _, service := range services(doc) {
		ref, ok := service["image"].(string)
		if !ok || ref == "" {
			return nil, fmt.Errorf("missing retained image reference")
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
