package gofpdi

import (
	"strings"
	"testing"
)

// TestGetPageResourcesReportsDanglingParent covers a page without /Resources
// whose /Parent points at an object the file does not contain. The page itself
// is listed in the page tree, so the file parses and only the resource walk
// reports the broken reference.
func TestGetPageResourcesReportsDanglingParent(t *testing.T) {
	reader := newTestReader(t, classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 99 0 R /MediaBox [0 0 595 842] >>",
	}))

	_, err := reader.getPageResources(1)
	if err == nil || !strings.Contains(err.Error(), "page resource ancestor") {
		t.Fatalf("err = %v, want the dangling /Parent to be reported", err)
	}
}

// TestGetPageBoxReportsDanglingIndirectBox covers a /MediaBox that is an
// indirect reference to a missing object. Importing the page must fail instead
// of using an empty box.
func TestGetPageBoxReportsDanglingIndirectBox(t *testing.T) {
	reader := newTestReader(t, classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox 99 0 R >>",
	}))

	if _, err := reader.getPageBoxes(1, 1); err == nil || !strings.Contains(err.Error(), "Failed to resolve object") {
		t.Fatalf("err = %v, want the dangling /MediaBox to be reported", err)
	}
}

// TestGetContentReportsBrokenFilter covers page content whose /Filter cannot be
// used: an entry of the filter array points at a missing object, or the array
// holds something that is not a name.
func TestGetContentReportsBrokenFilter(t *testing.T) {
	tests := []struct {
		name, dict, wantErr string
	}{
		{
			name:    "filter array entry is a dangling reference",
			dict:    "<< /Length 4 /Filter [99 0 R] >>",
			wantErr: "Failed to resolve filter object",
		},
		{
			name:    "filter array entry is not a name",
			dict:    "<< /Length 4 /Filter [42] >>",
			wantErr: "/Filter array entry is not a name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, contentStreamPDF(test.dict, "0"))

			_, err := reader.getContent(1)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestGetPageRotationInheritsIndirectParentRotate covers a /Rotate that the page
// inherits from a page tree node which stores the angle as an indirect object.
func TestGetPageRotationInheritsIndirectParentRotate(t *testing.T) {
	reader := newTestReader(t, classicPDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R] /Count 1 /Rotate 20 0 R >>",
		3:  "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		20: "180",
	}))

	rotation, err := reader.getPageRotation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 180 {
		t.Fatalf("rotation = %d, want 180", rotation.Int)
	}
}

// TestGetPageRotationRejectsInlineParent documents the limit of the /Parent
// walk: it follows indirect objects only, so a page whose /Parent is written
// inline is reported instead of being dereferenced.
func TestGetPageRotationRejectsInlineParent(t *testing.T) {
	reader := newTestReader(t, classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent << /Type /Pages /Kids [] /Count 0 >> /MediaBox [0 0 595 842] >>",
	}))

	_, err := reader.getPageRotation(1)
	if err == nil || !strings.Contains(err.Error(), "Page dictionary is missing") {
		t.Fatalf("err = %v, want the inline /Parent to be reported", err)
	}
}

// TestTrackPageAncestorRejectsNilPage covers the guards of the /Parent walk
// directly: a nil page and an inline node are not steps of a parent chain.
func TestTrackPageAncestorRejectsNilPage(t *testing.T) {
	seen := make(map[[2]int]bool)

	if err := trackPageAncestor(nil, seen, 0); err == nil {
		t.Fatal("a nil page should be an error")
	}
	if len(seen) != 0 {
		t.Fatalf("a rejected step must not be recorded: %v", seen)
	}

	inline := &PdfValue{Type: PDF_TYPE_DICTIONARY}
	if err := trackPageAncestor(inline, seen, 0); err != nil {
		t.Fatalf("an inline node has no object number to track: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("an inline node must not be recorded: %v", seen)
	}
}
