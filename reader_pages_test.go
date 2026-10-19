package gofpdi

import (
	"testing"
)

func TestGetNumPagesWithoutPages(t *testing.T) {
	reader := &PdfReader{}
	if _, err := reader.getNumPages(); err == nil {
		t.Fatal("expected an error when the page count is 0")
	}
}

// rotationPDF builds a three-page PDF. Page 1 sets /Rotate directly, page 2
// inherits it from the page tree, and page 3 uses an indirect object.
func rotationPDF() []byte {
	return classicPDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R 5 0 R 7 0 R] /Count 3 /Rotate 270 /MediaBox [0 0 595 842] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Rotate 90 /Contents 4 0 R /Resources << >> >>",
		4:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		5:  "<< /Type /Page /Parent 2 0 R /Contents 6 0 R /Resources << >> >>",
		6:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		7:  "<< /Type /Page /Parent 2 0 R /Rotate 30 0 R /Contents 8 0 R /Resources << >> >>",
		8:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		30: "90",
	})
}

func TestGetPageRotation(t *testing.T) {
	reader := newTestReader(t, rotationPDF())

	tests := []struct {
		page int
		want int
	}{
		{page: 1, want: 90},
		{page: 2, want: 270}, // inherited from the page tree
		{page: 3, want: 90},  // indirect object
	}
	for _, test := range tests {
		rotation, err := reader.getPageRotation(test.page)
		if err != nil {
			t.Fatalf("page %d: unexpected error: %v", test.page, err)
		}
		if rotation.Int != test.want {
			t.Fatalf("page %d rotation = %d, want %d", test.page, rotation.Int, test.want)
		}
	}

	if _, err := reader.getPageRotation(9); err == nil {
		t.Fatal("expected an error for a missing page")
	}
}

func TestGetPageRotationDefaultsToZero(t *testing.T) {
	reader := newTestReader(t, pagesPDF("/MediaBox [0 0 595 842]", []pageSpec{{}}))

	rotation, err := reader.getPageRotation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 0 {
		t.Fatalf("default rotation = %d, want 0", rotation.Int)
	}
}

func TestGetPageResources(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /Resources << /ProcSet [/PDF] >> >>",
		3:  "<< /Type /Page /Parent 2 0 R /Resources 20 0 R /Contents 4 0 R >>",
		4:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		5:  "<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		6:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		20: "<< /Font << >> >>",
	})
	reader := newTestReader(t, pdf)

	direct, err := reader.getPageResources(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := direct.Dictionary["/Font"]; !ok {
		t.Fatalf("page resources should come from the referenced object: %v", direct.Dictionary)
	}

	inherited, err := reader.getPageResources(2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := inherited.Dictionary["/ProcSet"]; !ok {
		t.Fatalf("page without /Resources should inherit its parent's resources: %v", inherited.Dictionary)
	}
	if _, ok := inherited.Dictionary["/Kids"]; ok {
		t.Fatal("resources must not contain the parent page-tree dictionary")
	}

	if _, err := reader.getPageResources(3); err == nil {
		t.Fatal("expected an error for a missing page")
	}
}

func TestGetPageResourcesWithoutPageDictionary(t *testing.T) {
	// A page value without a dictionary returns the zero value instead of
	// panicking; ImportPage reports the missing resources.
	orphan := &PdfReader{pages: []*PdfValue{{
		Type:  PDF_TYPE_OBJECT,
		Value: &PdfValue{Type: PDF_TYPE_DICTIONARY, Dictionary: map[string]*PdfValue{}},
	}}}

	empty, err := orphan.getPageResources(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if empty.Type != PDF_TYPE_NULL {
		t.Fatalf("orphan page resources = %#v, want the zero value", empty)
	}
}

func TestGetPageBoxesInheritance(t *testing.T) {
	reader := newTestReader(t, pagesPDF("/MediaBox [0 0 595 842]", []pageSpec{{}}))

	boxes, err := reader.getPageBoxes(1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	media := boxes["/MediaBox"]
	if media["w"] != 595 || media["h"] != 842 {
		t.Fatalf("inherited MediaBox = %v", media)
	}
	if len(boxes["/CropBox"]) != 0 {
		t.Fatalf("CropBox should not be set: %v", boxes["/CropBox"])
	}

	if _, err := reader.getPageBoxes(2, 1); err == nil {
		t.Fatal("expected an error for a missing page")
	}

	all, err := reader.getAllPageBoxes(2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if all[1]["/MediaBox"]["w"] != 297.5 {
		t.Fatalf("scaled width = %v, want 297.5", all[1]["/MediaBox"]["w"])
	}
}

func TestGetPageBoxesRejectsNonArrayBox(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox /Bad /Resources << >> >>",
	})

	reader := newTestReader(t, pdf)
	if _, err := reader.getPageBoxes(1, 1); err == nil {
		t.Fatal("expected an error for a box that is not an array")
	}
	if _, err := reader.getAllPageBoxes(1); err == nil {
		t.Fatal("expected getAllPageBoxes to report the broken box")
	}
}

// TestNestedPageTreeIsFlattened checks a page tree with an intermediate /Pages
// node. Real PDFs routinely split large page trees into several levels.
func TestNestedPageTreeIsFlattened(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 2 /MediaBox [0 0 400 500] >>",
		3: "<< /Type /Pages /Parent 2 0 R /Kids [4 0 R 5 0 R] /Count 2 /Rotate 90 >>",
		4: "<< /Type /Page /Parent 3 0 R /Resources << >> /Contents 6 0 R >>",
		5: "<< /Type /Page /Parent 3 0 R /Resources << >> /Contents 7 0 R >>",
		6: streamObject([]byte("q Q\n"), ""),
		7: streamObject([]byte("BT ET\n"), ""),
	})
	reader := newTestReader(t, pdf)

	pages, err := reader.getNumPages()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pages != 2 {
		t.Fatalf("page count = %d, want 2", pages)
	}

	boxes, err := reader.getAllPageBoxes(1)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	for page := 1; page <= 2; page++ {
		if got := boxes[page]["/MediaBox"]; got["w"] != 400 || got["h"] != 500 {
			t.Fatalf("page %d MediaBox = %v, want 400x500", page, got)
		}
	}

	rotation, err := reader.getPageRotation(2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 90 {
		t.Fatalf("page 2 rotation = %d, want 90", rotation.Int)
	}

	if got, err := reader.getContent(2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if got != "BT ET\n" {
		t.Fatalf("page 2 content = %q", got)
	}
}
