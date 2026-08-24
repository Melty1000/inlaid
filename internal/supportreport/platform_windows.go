//go:build windows

package supportreport

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func platformRelease() (string, string, string) {
	version := windows.RtlGetVersion()
	if version == nil {
		return "", "", ""
	}
	value := fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber)
	return safeToken(value, 32), "", ""
}
