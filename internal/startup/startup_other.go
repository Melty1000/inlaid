//go:build !windows

package startup

func RelaunchFromExplorer() (bool, error) {
	return false, nil
}
