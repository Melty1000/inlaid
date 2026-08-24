//go:build !windows && !linux && !darwin

package supportreport

func platformRelease() (string, string, string) {
	return "", "", ""
}
