//go:build windows

package pathownership

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCompareStringOrdinal = windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")
	compareOrdinalIgnoreCase = compareOrdinalIgnoreCaseWindows
)

type ordinalComparisonError struct {
	err error
}

func (err *ordinalComparisonError) Error() string { return err.err.Error() }
func (err *ordinalComparisonError) Unwrap() error { return err.err }

// EqualOrdinalIgnoreCase applies Windows' native ordinal, case-insensitive
// comparison to path text. It deliberately does not use Unicode simple folding,
// which treats some non-ASCII characters as equivalent to ASCII path text.
func EqualOrdinalIgnoreCase(left, right string) (bool, error) {
	equal, err := compareOrdinalIgnoreCase(left, right)
	if err != nil {
		return false, &ordinalComparisonError{err: err}
	}
	return equal, nil
}

func compareOrdinalIgnoreCaseWindows(left, right string) (bool, error) {
	leftUTF16, err := windows.UTF16FromString(left)
	if err != nil {
		return false, fmt.Errorf("encode first path for ordinal comparison: %w", err)
	}
	rightUTF16, err := windows.UTF16FromString(right)
	if err != nil {
		return false, fmt.Errorf("encode second path for ordinal comparison: %w", err)
	}
	result, _, callErr := procCompareStringOrdinal.Call(
		uintptr(unsafe.Pointer(&leftUTF16[0])),
		uintptr(len(leftUTF16)-1),
		uintptr(unsafe.Pointer(&rightUTF16[0])),
		uintptr(len(rightUTF16)-1),
		1,
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_SUCCESS) {
			return false, errors.New("CompareStringOrdinal failed without a Windows error code")
		}
		return false, fmt.Errorf("CompareStringOrdinal: %w", callErr)
	}
	return result == 2, nil // CSTR_EQUAL
}

func isOrdinalComparisonError(err error) bool {
	var comparisonErr *ordinalComparisonError
	return errors.As(err, &comparisonErr)
}
