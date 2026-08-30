//go:build windows

package pathownership

import (
	"errors"
	"strings"
	"testing"
)

func testExpand(value string) string {
	return strings.ReplaceAll(value, "%LOCALAPPDATA%", `C:\Users\Cody\AppData\Local`)
}

func TestNormalizeSegmentUsesComparisonOnlyRules(t *testing.T) {
	got, ok := NormalizeSegment(`  "%LOCALAPPDATA%\Programs\Inlaid\.\"  `, testExpand)
	if !ok || got != `C:\Users\Cody\AppData\Local\Programs\Inlaid` {
		t.Fatalf("normalized = %q, %v", got, ok)
	}
	for _, invalid := range []string{"", `relative\Inlaid`, `C:\In;laid`} {
		if got, ok := NormalizeSegment(invalid, testExpand); ok {
			t.Fatalf("invalid segment %q normalized as %q", invalid, got)
		}
	}
}

func TestEqualOrdinalIgnoreCaseUsesWindowsPathSemantics(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "ASCII casing", left: `C:\Programs\Inlaid`, right: `c:\programs\INLAID`, want: true},
		{name: "non-ASCII casing", left: `C:\TÉST\Inlaid`, right: `c:\tést\inlaid`, want: true},
		{name: "Kelvin sign is not ASCII K", left: `C:\Temp\Kelp`, right: `C:\Temp\Kelp`, want: false},
		{name: "canonical forms stay distinct", left: `C:\Temp\Å`, right: "C:\\Temp\\A\u030A", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := EqualOrdinalIgnoreCase(test.left, test.right)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("EqualOrdinalIgnoreCase(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
	if _, err := EqualOrdinalIgnoreCase("C:\\Bad\x00Path", `C:\Bad`); err == nil {
		t.Fatal("embedded NUL was accepted for ordinal path comparison")
	}
}

func TestApplyPreservesLiteralPathAndTracksOnlyItsAppend(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	for _, test := range []struct {
		name, before, want string
	}{
		{"empty", "", program},
		{"ordinary", `C:\One;"C:\Two"`, `C:\One;"C:\Two";` + program},
		{"trailing empty", `C:\One;`, `C:\One;;` + program},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanApply(test.before, true, Marker{}, program, testExpand)
			if err != nil || plan.Path != test.want || !plan.Marker.Owned || plan.Marker.InsertedSegment != program {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
		})
	}
}

func TestApplyTreatsEquivalentTextAsUserOwned(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	before := `C:\One; "%LOCALAPPDATA%\Programs\Inlaid\" ;C:\Two`
	plan, err := PlanApply(before, true, Marker{}, program, testExpand)
	if err != nil || plan.Path != before || plan.Marker.Owned {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
}

func TestApplyDoesNotUseUnicodeSimpleFoldingForWindowsPaths(t *testing.T) {
	program := `C:\Temp\Kelp`
	foreign := `C:\Temp\Kelp`
	plan, err := PlanApply(foreign, true, Marker{}, program, testExpand)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != foreign+`;`+program || !plan.Marker.Owned || plan.Marker.InsertedSegment != program {
		t.Fatalf("Kelvin-sign path was treated as equivalent: %+v", plan)
	}
}

func TestRepairReappendsMissingOwnedSegmentButYieldsToUserEdit(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	marker := Marker{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program, InsertedSegment: program}
	plan, err := PlanApply(`C:\One`, true, marker, program, testExpand)
	if err != nil || plan.Path != `C:\One;`+program || !plan.Marker.Owned {
		t.Fatalf("missing repair = %+v, %v", plan, err)
	}
	quoted := `C:\One;"C:\Users\Cody\AppData\Local\Programs\Inlaid\"`
	plan, err = PlanApply(quoted, true, marker, program, testExpand)
	if err != nil || plan.Path != quoted || plan.Marker.Owned {
		t.Fatalf("edited repair = %+v, %v", plan, err)
	}
}

func TestUninstallRemovesOnlyOneProvenOwnedSegment(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	marker := Marker{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program, InsertedSegment: program}
	plan, err := PlanUninstall(`C:\One;;`+program, true, marker, program, testExpand)
	if err != nil || plan.Path != `C:\One;` || plan.Marker.Present {
		t.Fatalf("uninstall = %+v, %v", plan, err)
	}
	ambiguous := `C:\One;` + program + `;"` + program + `"`
	plan, err = PlanUninstall(ambiguous, true, marker, program, testExpand)
	if err != nil || plan.Path != ambiguous || plan.Warn == "" {
		t.Fatalf("ambiguous uninstall = %+v, %v", plan, err)
	}
}

func TestUninstallDoesNotTreatKelvinSignAsOwnedPath(t *testing.T) {
	program := `C:\Temp\Kelp`
	foreign := `C:\Temp\Kelp`
	marker := Marker{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program, InsertedSegment: program}
	plan, err := PlanUninstall(foreign+`;`+program, true, marker, program, testExpand)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != foreign || plan.Warn != "" {
		t.Fatalf("uninstall did not remove only the ordinally equivalent owned segment: %+v", plan)
	}
}

func TestKelvinSignMarkerDoesNotValidateAsASCIIK(t *testing.T) {
	program := `C:\Temp\Kelp`
	marker := Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory: `C:\Temp\Kelp`,
		InsertedSegment:            `C:\Temp\Kelp`,
	}
	if _, err := PlanApply(program, true, marker, program, testExpand); err == nil {
		t.Fatal("marker using the Kelvin sign validated as an ASCII-K program directory")
	}
	plan, err := PlanUninstall(program, true, marker, program, testExpand)
	if err != nil || plan.Path != program || plan.Warn == "" {
		t.Fatalf("uninstall did not preserve PATH for a stale Unicode marker: %+v, %v", plan, err)
	}
}

func TestUninstallPropagatesOrdinalComparisonFailure(t *testing.T) {
	original := compareOrdinalIgnoreCase
	compareOrdinalIgnoreCase = func(string, string) (bool, error) {
		return false, errors.New("injected ordinal comparison failure")
	}
	t.Cleanup(func() { compareOrdinalIgnoreCase = original })

	program := `C:\Temp\Inlaid`
	marker := Marker{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program, InsertedSegment: program}
	if _, err := PlanUninstall(program, true, marker, program, testExpand); err == nil || !strings.Contains(err.Error(), "injected ordinal comparison failure") {
		t.Fatalf("ordinal comparison failure was not propagated: %v", err)
	}
}

func TestInconsistentMarkersFailClosed(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	tests := []Marker{
		{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program},
		{Present: true, Valid: true, Owned: false, NormalizedProgramDirectory: program, InsertedSegment: program},
		{Present: true, Valid: true, Owned: false, PathValueExistedBeforeOwnership: true, NormalizedProgramDirectory: program},
		{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: program, InsertedSegment: `C:\Other`},
	}
	for index, marker := range tests {
		if _, err := PlanApply(`C:\One;`+program, true, marker, program, testExpand); err == nil {
			t.Fatalf("inconsistent marker %d was accepted for apply", index)
		}
		plan, err := PlanUninstall(`C:\One;`+program, true, marker, program, testExpand)
		if err != nil || plan.Path != `C:\One;`+program || plan.Warn == "" {
			t.Fatalf("inconsistent marker %d uninstall = %+v, %v", index, plan, err)
		}
	}
}

func TestLiteralAndNormalizedOwnershipMustBelongToSameSegment(t *testing.T) {
	program := `C:\Program`
	marker := Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory: program,
		InsertedSegment:            `C:\Literal`,
	}
	plan, err := PlanUninstall(`C:\Literal;C:\Program`, true, marker, program, testExpand)
	if err != nil || plan.Path != `C:\Literal;C:\Program` || plan.Warn == "" {
		t.Fatalf("split literal/normalized ownership was accepted: %+v, %v", plan, err)
	}
}

func TestAbsentAndPresentEmptyPATHRemainDistinct(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	for _, test := range []struct {
		name                 string
		beforePresent        bool
		wantUninstallPresent bool
	}{
		{name: "absent", beforePresent: false, wantUninstallPresent: false},
		{name: "present empty", beforePresent: true, wantUninstallPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			apply, err := PlanApply("", test.beforePresent, Marker{}, program, testExpand)
			if err != nil || !apply.PathPresent || apply.Path != program || !apply.Marker.Owned ||
				apply.Marker.PathValueExistedBeforeOwnership != test.beforePresent {
				t.Fatalf("apply = %+v, %v", apply, err)
			}
			uninstall, err := PlanUninstall(apply.Path, apply.PathPresent, apply.Marker, program, testExpand)
			if err != nil || uninstall.Path != "" || uninstall.PathPresent != test.wantUninstallPresent {
				t.Fatalf("uninstall = %+v, %v", uninstall, err)
			}
		})
	}
}

func TestRepairPreservesOrRefreshesPathPresenceProvenance(t *testing.T) {
	program := `C:\Users\Cody\AppData\Local\Programs\Inlaid`
	owned := Marker{
		Present: true, Valid: true, Owned: true,
		NormalizedProgramDirectory:      program,
		InsertedSegment:                 program,
		PathValueExistedBeforeOwnership: true,
	}

	preserved, err := PlanApply(program, true, owned, program, testExpand)
	if err != nil || !preserved.Marker.PathValueExistedBeforeOwnership {
		t.Fatalf("preserved ownership = %+v, %v", preserved, err)
	}

	reacquired, err := PlanApply("", false, owned, program, testExpand)
	if err != nil || !reacquired.Marker.Owned || reacquired.Marker.PathValueExistedBeforeOwnership {
		t.Fatalf("reacquired absent ownership = %+v, %v", reacquired, err)
	}

	userOwned := Marker{
		Present: true, Valid: true, Owned: false,
		NormalizedProgramDirectory: program,
	}
	reacquired, err = PlanApply("", true, userOwned, program, testExpand)
	if err != nil || !reacquired.Marker.Owned || !reacquired.Marker.PathValueExistedBeforeOwnership {
		t.Fatalf("reacquired present-empty ownership = %+v, %v", reacquired, err)
	}
}

func TestSemicolonProgramDirectoryAlwaysFailsClosed(t *testing.T) {
	marker := Marker{Present: true, Valid: true, Owned: true, NormalizedProgramDirectory: `C:\Good`, InsertedSegment: `C:\Good`}
	for _, operation := range []func() error{
		func() error { _, err := PlanApply(`C:\One`, true, Marker{}, `C:\Bad;Path`, testExpand); return err },
		func() error { _, err := PlanApply(`C:\One`, true, marker, `C:\Bad;Path`, testExpand); return err },
		func() error { _, err := PlanUninstall(`C:\One`, true, marker, `C:\Bad;Path`, testExpand); return err },
	} {
		if err := operation(); err == nil {
			t.Fatal("semicolon program directory was accepted")
		}
	}
}
