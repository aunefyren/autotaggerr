//go:build darwin

package utilities

func NormalizePathForExternalTool(p string) (string, error) {
	return p, nil
}
