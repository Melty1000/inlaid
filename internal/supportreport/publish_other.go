//go:build !windows

package supportreport

import "os"

func publishNoReplace(source, destination string) error {
	return os.Link(source, destination)
}
