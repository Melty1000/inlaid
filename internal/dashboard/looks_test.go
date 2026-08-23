package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

func TestLookCatalogBypassesIdentityTransform(t *testing.T) {
	catalog := builtInLookCatalog()
	for _, test := range []struct {
		name     string
		strength int
		wantID   string
	}{
		{name: "NONE", strength: 100, wantID: "none:100"},
		{name: "WARM", strength: 0, wantID: "warm:0"},
		{name: "missing", strength: 77, wantID: "none:77"},
	} {
		transform, transformID := catalog.resolve(test.name, test.strength)
		if transform != nil {
			t.Errorf("resolve(%q, %d) returned an identity transform instead of nil", test.name, test.strength)
		}
		if transformID != test.wantID {
			t.Errorf("resolve(%q, %d) ID = %q, want %q", test.name, test.strength, transformID, test.wantID)
		}
	}
	transform, transformID := catalog.resolve("WARM", 100)
	if transform == nil || transformID != "warm:100" {
		t.Fatalf("active look = %T/%q, want nonnil/warm:100", transform, transformID)
	}
	if got := transform.TransformRGB(cellframe.NewRGB(30, 80, 140)); got == cellframe.NewRGB(30, 80, 140) {
		t.Fatal("active warm look behaved like identity")
	}
}

func TestLookCatalogEnforcesAggregateRetainedBudget(t *testing.T) {
	directory := t.TempDir()
	writeTestCube(t, directory, "a.cube", "First")
	writeTestCube(t, directory, "b.cube", "Second")

	// Each two-row 1D table retains exactly 48 numeric bytes. The first file
	// fits; the second must be skipped rather than allowing aggregate growth.
	catalog, err := loadLookCatalogBounded(directory, 48)
	if err == nil || !strings.Contains(err.Error(), "aggregate color-look budget") {
		t.Fatalf("budget error = %v", err)
	}
	if len(catalog.names) != 1 || catalog.names[0] != "First" {
		t.Fatalf("bounded catalog names = %v, want [First]", catalog.names)
	}
	if catalog.transforms["first"] == nil || catalog.transforms["second"] != nil {
		t.Fatal("bounded catalog retained the wrong tables")
	}

	complete, err := loadLookCatalogBounded(directory, 96)
	if err != nil || len(complete.names) != 2 {
		t.Fatalf("complete catalog = %v, err %v", complete.names, err)
	}
}

func writeTestCube(t *testing.T, directory, name, title string) {
	t.Helper()
	source := "TITLE \"" + title + "\"\nLUT_1D_SIZE 2\n0 0 0\n1 1 1\n"
	if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}
