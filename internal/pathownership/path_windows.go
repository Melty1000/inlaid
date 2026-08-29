//go:build windows

// Package pathownership owns the exact, conservative current-user PATH policy
// used by the Windows installer helper.
package pathownership

import (
	"fmt"
	"path/filepath"
	"strings"
)

const MarkerSchema uint32 = 2

type Marker struct {
	Present                         bool
	Valid                           bool
	Owned                           bool
	PathValueExistedBeforeOwnership bool
	NormalizedProgramDirectory      string
	InsertedSegment                 string
}

type Plan struct {
	Path        string
	PathPresent bool
	Marker      Marker
	Warn        string
}

type ExpandFunc func(string) string

func NormalizeSegment(value string, expand ExpandFunc) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	if expand != nil {
		value = expand(value)
	}
	if strings.ContainsRune(value, ';') || !filepath.IsAbs(value) {
		return "", false
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return "", false
	}
	return value, true
}

func NormalizeProgramDirectory(value string, expand ExpandFunc) (string, error) {
	if strings.ContainsRune(value, ';') {
		return "", fmt.Errorf("program directory contains the PATH delimiter ';'")
	}
	normalized, ok := NormalizeSegment(value, expand)
	if !ok {
		return "", fmt.Errorf("program directory is not a rooted Windows path")
	}
	return normalized, nil
}

func PlanApply(currentPath string, currentPathPresent bool, marker Marker, programDirectory string, expand ExpandFunc) (Plan, error) {
	program, err := NormalizeProgramDirectory(programDirectory, expand)
	if err != nil {
		return Plan{}, err
	}
	if err := validateMarker(marker, program, expand); err != nil {
		return Plan{}, err
	}

	segments := strings.Split(currentPath, ";")
	equivalent := 0
	ownedMatches := 0
	for _, segment := range segments {
		normalized, ok := NormalizeSegment(segment, expand)
		equivalentMatch := ok && strings.EqualFold(normalized, program)
		if equivalentMatch {
			equivalent++
		}
		if marker.Present && segment == marker.InsertedSegment && equivalentMatch {
			ownedMatches++
		}
	}

	result := Plan{Path: currentPath, PathPresent: currentPathPresent, Marker: Marker{
		Present: true, Valid: true, Owned: false,
		NormalizedProgramDirectory: program,
	}}
	if marker.Present && marker.Owned && ownedMatches == 1 && equivalent == 1 {
		result.Marker.Owned = true
		result.Marker.InsertedSegment = marker.InsertedSegment
		result.Marker.PathValueExistedBeforeOwnership = marker.PathValueExistedBeforeOwnership
		return result, nil
	}
	if equivalent > 0 {
		result.Warn = "an equivalent user-owned PATH segment already exists"
		return result, nil
	}
	if currentPath == "" {
		result.Path = program
	} else {
		result.Path = currentPath + ";" + program
	}
	result.PathPresent = true
	result.Marker.Owned = true
	result.Marker.InsertedSegment = program
	result.Marker.PathValueExistedBeforeOwnership = currentPathPresent
	return result, nil
}

func PlanUninstall(currentPath string, currentPathPresent bool, marker Marker, programDirectory string, expand ExpandFunc) (Plan, error) {
	program, err := NormalizeProgramDirectory(programDirectory, expand)
	if err != nil {
		return Plan{}, err
	}
	result := Plan{Path: currentPath, PathPresent: currentPathPresent, Marker: Marker{}}
	if !marker.Present {
		result.Warn = "PATH was preserved because installer ownership was absent"
		return result, nil
	}
	if err := validateMarker(marker, program, expand); err != nil {
		result.Warn = "PATH was preserved because installer ownership was absent, false, malformed, or stale"
		return result, nil
	}
	if !marker.Owned {
		result.Warn = "PATH was preserved because installer ownership was false"
		return result, nil
	}
	segments := strings.Split(currentPath, ";")
	equivalent := 0
	ownedIndex := -1
	ownedMatches := 0
	for index, segment := range segments {
		normalized, ok := NormalizeSegment(segment, expand)
		equivalentMatch := ok && strings.EqualFold(normalized, program)
		if equivalentMatch {
			equivalent++
		}
		if segment == marker.InsertedSegment && equivalentMatch {
			ownedIndex = index
			ownedMatches++
		}
	}
	if ownedMatches != 1 || equivalent != 1 {
		result.Warn = "PATH was preserved because the owned segment became missing, duplicated, or ambiguous"
		return result, nil
	}
	result.Path = removeSegment(currentPath, ownedIndex)
	if result.Path == "" && currentPath == marker.InsertedSegment && !marker.PathValueExistedBeforeOwnership {
		result.PathPresent = false
	}
	return result, nil
}

func validateMarker(marker Marker, program string, expand ExpandFunc) error {
	if !marker.Present {
		return nil
	}
	if !marker.Valid {
		return fmt.Errorf("installer PATH provenance marker is malformed")
	}
	normalizedMarker, ok := NormalizeSegment(marker.NormalizedProgramDirectory, expand)
	if !ok {
		return fmt.Errorf("installer PATH provenance marker is malformed")
	}
	if !strings.EqualFold(normalizedMarker, program) {
		return fmt.Errorf("installer PATH provenance marker is stale")
	}
	if marker.Owned {
		inserted, ok := NormalizeSegment(marker.InsertedSegment, expand)
		if !ok || !strings.EqualFold(inserted, normalizedMarker) {
			return fmt.Errorf("installer PATH provenance marker is malformed")
		}
		return nil
	}
	if marker.InsertedSegment != "" || marker.PathValueExistedBeforeOwnership {
		return fmt.Errorf("installer PATH provenance marker is malformed")
	}
	return nil
}

func removeSegment(value string, index int) string {
	if value == "" {
		return ""
	}
	start := 0
	current := 0
	for position := 0; position <= len(value); position++ {
		if position != len(value) && value[position] != ';' {
			continue
		}
		if current == index {
			if start > 0 {
				return value[:start-1] + value[position:]
			}
			if position < len(value) {
				return value[position+1:]
			}
			return ""
		}
		current++
		start = position + 1
	}
	return value
}
