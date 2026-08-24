//go:build darwin

package supportreport

import "golang.org/x/sys/unix"

func platformRelease() (string, string, string) {
	version, _ := unix.Sysctl("kern.osproductversion")
	kernel, _ := unix.Sysctl("kern.osrelease")
	return safeToken(version, 32), "", safeToken(kernel, 64)
}
