//go:build !windows

package utilities

func NormalizePathForExternalTool(p string) (string, error) {
	return p, nil
}
