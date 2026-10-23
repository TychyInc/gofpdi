package gofpdi

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// importContents imports every page of source and returns the written objects
// keyed by their content hash, plus the template id → hash map. The source is
// opened from a file, so the hash of an object only depends on the file name and
// the object id and two runs are comparable.
func importContents(t *testing.T, path string) (map[string]string, error) {
	t.Helper()

	importer := NewImporter()
	importer.SetSourceFile(path)

	pages := importer.GetNumPages()
	for page := 1; page <= pages; page++ {
		importer.ImportPage(page, "/MediaBox")
	}

	tplNames := importer.PutFormXobjectsUnordered()
	if len(tplNames) != pages {
		return nil, fmt.Errorf("template objects = %d, want %d", len(tplNames), pages)
	}

	objects := importer.GetImportedObjectsUnordered()
	if len(objects) == 0 {
		return nil, fmt.Errorf("no objects were imported")
	}

	result := make(map[string]string, len(objects)+len(tplNames))
	for hash, content := range objects {
		if len(hash) != 40 {
			return nil, fmt.Errorf("object key %q is not a 40 character hash", hash)
		}
		result[hash] = string(content)
	}
	for name, hash := range tplNames {
		result[name] = hash
	}

	return result, nil
}

// diffImports describes the first difference between two imports.
func diffImports(want, got map[string]string) string {
	keys := make([]string, 0, len(want)+len(got))
	for key := range want {
		keys = append(keys, key)
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		wantValue, ok := want[key]
		if !ok {
			return fmt.Sprintf("%q was written but not before", key)
		}
		gotValue, ok := got[key]
		if !ok {
			return fmt.Sprintf("%q was not written", key)
		}
		if wantValue != gotValue {
			return fmt.Sprintf("%q changed", key)
		}
	}

	return ""
}

// TestImportIsDeterministic checks that importing the same file twice produces
// the same objects, in the same order, with the same references. The objects
// used to be written in Go's randomized map iteration order.
func TestImportIsDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, buildImporterFixture(t), 0o600); err != nil {
		t.Fatalf("failed to write the source PDF: %v", err)
	}

	first, err := importContents(t, path)
	if err != nil {
		t.Fatalf("failed to import the fixture: %v", err)
	}

	for run := 0; run < 8; run++ {
		got, err := importContents(t, path)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if diff := diffImports(first, got); diff != "" {
			t.Fatalf("run %d differs from the first one: %s", run, diff)
		}
	}
}

// TestConcurrentImporters runs the import workflow the package is used for from
// several goroutines at once. Each document generator owns its Importer, and CI
// runs the suite with -race, so state shared between them shows up as a data
// race or as differing objects.
func TestConcurrentImporters(t *testing.T) {
	source := buildImporterFixture(t)

	want, err := importStream(source)
	if err != nil {
		t.Fatalf("failed to import the fixture: %v", err)
	}

	const workers = 8

	var wg sync.WaitGroup
	failures := make(chan string, workers)

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()

			got, err := importStream(source)
			if err != nil {
				failures <- fmt.Sprintf("worker %d: %v", worker, err)
				return
			}
			if !equalStrings(want, got) {
				failures <- fmt.Sprintf("worker %d: imported objects differ", worker)
			}
		}(worker)
	}

	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
}

// importStream imports the fixture from an io.ReadSeeker and returns the sorted
// contents of the written objects. Object ids are used instead of hashes
// because the hash embeds the name of the source, which for a stream is its
// address.
func importStream(source []byte) ([]string, error) {
	stream := bytes.NewReader(source)
	var rs io.ReadSeeker = stream

	importer := NewImporter()
	importer.SetSourceStream(&rs)

	pages := importer.GetNumPages()
	for page := 1; page <= pages; page++ {
		importer.ImportPage(page, "/MediaBox")
	}

	if names := importer.PutFormXobjects(); len(names) != pages {
		return nil, fmt.Errorf("template objects = %d, want %d", len(names), pages)
	}

	objects := importer.GetImportedObjects()
	if len(objects) == 0 {
		return nil, fmt.Errorf("no objects were imported")
	}

	contents := make([]string, 0, len(objects))
	for _, content := range objects {
		contents = append(contents, content)
	}
	sort.Strings(contents)

	return contents, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestImportFixtureHasExpectedContent guards the fixture used above: both the
// form XObjects and the referenced font have to be imported, otherwise the
// determinism and concurrency checks would pass on an empty import.
func TestImportFixtureHasExpectedContent(t *testing.T) {
	contents, err := importStream(buildImporterFixture(t))
	if err != nil {
		t.Fatalf("failed to import the fixture: %v", err)
	}

	joined := strings.Join(contents, "\n")
	for _, marker := range []string{"/BaseFont /Helvetica", "/Subtype /Form"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("imported objects do not contain %s", marker)
		}
	}
}
