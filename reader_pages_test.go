package gofpdi

import (
	"bytes"
	"strings"
	"testing"
)

func TestGetNumPagesWithoutPages(t *testing.T) {
	reader := &PdfReader{}
	if _, err := reader.getNumPages(); err == nil {
		t.Fatal("expected an error when the page count is 0")
	}
}

func TestGetPageRotation(t *testing.T) {
	pdf := fixturePDF(t, map[int]string{
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
	reader := newTestReader(t, pdf)

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

	plain := newTestReader(t, buildPagesPDF(t, "/MediaBox [0 0 595 842]", []pageSpec{{}}))
	rotation, err := plain.getPageRotation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotation.Int != 0 {
		t.Fatalf("default rotation = %d, want 0", rotation.Int)
	}
}

func TestGetPageResources(t *testing.T) {
	pdf := fixturePDF(t, map[int]string{
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
	pdf := buildPagesPDF(t, "/MediaBox [0 0 595 842]", []pageSpec{{}})
	reader := newTestReader(t, pdf)

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

func TestGetContentKeepsLeadingWhitespace(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{name: "space", stream: " q Q\n"},
		{name: "newline", stream: "\nq Q\n"},
		{name: "tab", stream: "\tq Q\n"},
		{name: "carriage return", stream: "\rq Q\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := buildSinglePagePDF(t, test.stream)
			if got := readPageContent(t, pdf, 1); got != test.stream {
				t.Fatalf("content = %q, want %q", got, test.stream)
			}
		})
	}
}

func TestGetContentHandlesCRLFAfterStreamKeyword(t *testing.T) {
	content := "q Q\n"
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\r\n" + content + "\nendstream",
	}
	reader := newTestReader(t, fixturePDF(t, objects))

	got, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != content {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

func TestDecodeRunLength(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{name: "literal run", input: []byte{3, 'a', 'b', 'c', 'd', 128}, want: "abcd"},
		{name: "repeat run", input: []byte{254, 'x', 128}, want: "xxx"},
		{name: "literal past end", input: []byte{10, 'a'}, want: "a"},
		{name: "no end marker", input: []byte{1, 'a', 'b'}, want: "ab"},
		{name: "truncated repeat", input: []byte{255}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRunLength(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("decoded = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDecodeFlateAcceptsTruncatedChecksum(t *testing.T) {
	content := []byte("BT ET\n")
	compressed := flate(t, content)
	truncated := compressed[:len(compressed)-4]

	got, err := decodeFlate(truncated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decoded = %q, want %q", got, content)
	}

	if _, err := decodeFlate([]byte{0x78, 0x9c}); err == nil {
		t.Fatal("expected an error for a stream without data")
	}
}

func TestReadRejectsUnknownObjectType(t *testing.T) {
	pdf := fixturePDF(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /NotAPage >>",
	})

	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
		t.Fatal("expected an error for an unknown object type")
	}
}

func TestReadRejectsNonPDFData(t *testing.T) {
	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader([]byte("this is not a pdf"))); err == nil {
		t.Fatal("expected an error for non-PDF data")
	}

	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(nil)); err == nil {
		t.Fatal("expected an error for empty data")
	}
}

func TestNewPdfReaderFromFile(t *testing.T) {
	path := writeTempPDF(t, fixturePDF(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
	}))

	reader, err := NewPdfReader(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.sourceFile != path {
		t.Fatalf("source file = %q, want %q", reader.sourceFile, path)
	}
	pages, err := reader.getNumPages()
	if err != nil || pages != 1 {
		t.Fatalf("pages = %d (%v), want 1", pages, err)
	}

	if _, err := NewPdfReader(path + ".missing"); err == nil {
		t.Fatal("expected an error for a missing file")
	} else if !strings.Contains(err.Error(), "Failed to open file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
