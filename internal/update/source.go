package update

import (
	"fmt"
	"regexp"
)

const DefaultRepository = "floatlab-hq/floatlab-core"

var repository = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$")

func ValidateUpstream(upstream string) error {
	if upstream != "" && !repository.MatchString(upstream) {
		return fmt.Errorf("upstream must be a GitHub owner/repo")
	}
	return nil
}
func Repository(upstream string) string {
	if upstream == "" {
		return DefaultRepository
	}
	return upstream
}
