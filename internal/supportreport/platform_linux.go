//go:build linux

package supportreport

import (
	"bufio"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func platformRelease() (string, string, string) {
	distribution, version := linuxRelease()
	var info unix.Utsname
	kernel := ""
	if unix.Uname(&info) == nil {
		kernel = safeToken(utsString(info.Release[:]), 64)
	}
	return version, distribution, kernel
}

func linuxRelease() (string, string) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return "", ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 16<<10))
	var distribution, version string
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		switch key {
		case "ID":
			distribution = safeToken(value, 32)
		case "VERSION_ID":
			version = safeToken(value, 32)
		}
	}
	return distribution, version
}

func utsString[T ~byte | ~int8](value []T) string {
	end := 0
	for end < len(value) && value[end] != 0 {
		end++
	}
	converted := make([]byte, end)
	for index := range converted {
		converted[index] = byte(value[index])
	}
	return string(converted)
}
