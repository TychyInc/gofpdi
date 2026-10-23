package gofpdi

import (
	"fmt"
	"strings"
	"testing"
)

// parentChainPDF builds a PDF whose page is object 3 and whose root page tree
// node is object 2. extra overrides objects of the default layout, which is
// what the cycle cases below use to point /Parent at a node already visited.
func parentChainPDF(pageDict string, extra map[int]string) []byte {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: pageDict,
		4: streamObject([]byte("q Q\n"), ""),
	}
	for id, body := range extra {
		objects[id] = body
	}

	return classicPDF(objects)
}

// TestPageBoxParentCycleIsAnError covers /MediaBox inheritance that never
// reaches a page tree node with a box. The /Parent walk used to recurse until
// the goroutine stack overflowed, which is a fatal error that recover() cannot
// catch, so one uploaded file could take the process down.
func TestPageBoxParentCycleIsAnError(t *testing.T) {
	tests := []struct {
		name     string
		pageDict string
		extra    map[int]string
	}{
		{
			name:     "page points at itself",
			pageDict: "<< /Type /Page /Parent 3 0 R /Contents 4 0 R >>",
		},
		{
			name:     "two pages point at each other",
			pageDict: "<< /Type /Page /Parent 5 0 R /Contents 4 0 R >>",
			extra:    map[int]string{5: "<< /Type /Page /Parent 3 0 R >>"},
		},
		{
			name:     "page tree node points at itself",
			pageDict: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
			extra:    map[int]string{2: "<< /Type /Pages /Parent 2 0 R /Kids [3 0 R] /Count 1 >>"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, parentChainPDF(test.pageDict, test.extra))

			if _, err := reader.getPageBoxes(1, 1); err == nil || !strings.Contains(err.Error(), "Cycle in page /Parent chain") {
				t.Fatalf("getPageBoxes error = %v, want a /Parent cycle error", err)
			}
			if _, err := reader.getPageRotation(1); err == nil || !strings.Contains(err.Error(), "Cycle in page /Parent chain") {
				t.Fatalf("getPageRotation error = %v, want a /Parent cycle error", err)
			}
		})
	}
}

// TestImportPageRejectsParentCycle checks that the importer reports the cycle
// instead of crashing, both when /MediaBox is inherited and when only /Rotate
// is. A page that carries its own box reaches the rotation walk, which has its
// own recursion.
func TestImportPageRejectsParentCycle(t *testing.T) {
	tests := []struct {
		name      string
		pageDict  string
		wantError string
	}{
		{
			name:      "inherited MediaBox",
			pageDict:  "<< /Type /Page /Parent 3 0 R /Contents 4 0 R >>",
			wantError: "Failed to get page boxes",
		},
		{
			name:      "own MediaBox, inherited Rotate",
			pageDict:  "<< /Type /Page /Parent 3 0 R /MediaBox [0 0 595 842] /CropBox [0 0 595 842] /Resources << >> /Contents 4 0 R >>",
			wantError: "Failed to get page rotation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, parentChainPDF(test.pageDict, nil))
			writer := newTestWriter(t)

			_, err := writer.ImportPage(reader, 1, "/MediaBox")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ImportPage error = %v, want it to mention %q", err, test.wantError)
			}
		})
	}
}

// TestPageParentChainOfLegalDepthIsReadable guards the depth limit: a page tree
// nested to maxPageTreeDepth levels is legal, so its /Parent chain must still be
// walked without an error.
func TestPageParentChainOfLegalDepthIsReadable(t *testing.T) {
	reader := newTestReader(t, parentChainPDFWithDepth(maxPageTreeDepth))

	boxes, err := reader.getPageBoxes(1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := boxes["/MediaBox"]; got["w"] != 200 || got["h"] != 300 {
		t.Fatalf("MediaBox = %v, want the 200x300 box at the end of the chain", got)
	}

	rotation, err := reader.getPageRotation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 90 {
		t.Fatalf("rotation = %d, want the 90 at the end of the chain", rotation.Int)
	}
}

// TestPageParentChainBeyondTheTreeDepthIsAnError checks the upper bound. A chain
// longer than any page tree the reader accepts can only come from a damaged or
// crafted file, so it is reported rather than followed.
func TestPageParentChainBeyondTheTreeDepthIsAnError(t *testing.T) {
	reader := newTestReader(t, parentChainPDFWithDepth(maxPageTreeDepth+1))

	wantError := fmt.Sprintf("nested more than %d levels deep", maxPageTreeDepth)
	if _, err := reader.getPageBoxes(1, 1); err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("getPageBoxes error = %v, want it to mention %q", err, wantError)
	}
	if _, err := reader.getPageRotation(1); err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("getPageRotation error = %v, want it to mention %q", err, wantError)
	}
}

// TestInlinePageParentIsNotACycle checks that page tree nodes without an object
// number are not mistaken for a cycle. /MediaBox and /Rotate are inherited from
// the page tree node the inline page points at.
func TestInlinePageParentIsNotACycle(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [<< /Type /Page /Parent 2 0 R /Contents 3 0 R >>] /Count 1" +
			" /MediaBox [0 0 200 100] /Rotate 90 >>",
		3: streamObject([]byte("q Q\n"), ""),
	})

	reader := newTestReader(t, pdf)
	boxes, err := reader.getPageBoxes(1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := boxes["/MediaBox"]; got["w"] != 200 || got["h"] != 100 {
		t.Fatalf("MediaBox = %v, want the inherited 200x100 box", got)
	}

	rotation, err := reader.getPageRotation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 90 {
		t.Fatalf("rotation = %d, want the inherited 90", rotation.Int)
	}
}

// parentChainPDFWithDepth builds a PDF whose page (object 3) inherits its box
// and rotation depth levels up the /Parent chain. The last node in the chain
// carries /MediaBox and /Rotate, so the walk visits exactly depth ancestors.
func parentChainPDFWithDepth(depth int) []byte {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
	}

	for i := 0; i <= depth; i++ {
		id := 3 + i

		if i == depth {
			// The end of the chain owns the box and the rotation.
			entry := "/MediaBox [0 0 200 300] /Rotate 90"
			if i == 0 {
				entry += " /Type /Page /Contents 1000 0 R"
			}
			objects[id] = "<< " + entry + " >>"
			continue
		}

		entry := fmt.Sprintf("/Parent %d 0 R", id+1)
		if i == 0 {
			entry += " /Type /Page /Contents 1000 0 R"
		}
		objects[id] = "<< " + entry + " >>"
	}

	objects[1000] = streamObject([]byte("q Q\n"), "")

	return classicPDF(objects)
}
