//go:build !integrationcoverage

package integrationcoverage

func Flush() {}

func ConfigureSandbox(map[string]any) {}

func ArchiveSandbox(string, string) error { return nil }
