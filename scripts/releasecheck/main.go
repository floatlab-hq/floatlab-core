// releasecheck uses the runtime compatibility rules for ISO publication.
package main

import (
	"fmt"
	"github.com/floatlab/floatlab-core/pkg/release"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: releasecheck HOST MIN MAX")
		os.Exit(2)
	}
	ok, err := (release.HostRange{Min: os.Args[2], Max: os.Args[3]}).Contains(os.Args[1])
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "host %s is outside >=%s <%s: %v\n", os.Args[1], os.Args[2], os.Args[3], err)
		os.Exit(1)
	}
}
