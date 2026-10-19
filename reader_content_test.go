package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

// streamObject renders an indirect stream object with the given bytes and an
// optional /Filter entry.
func streamObject(data []byte, filter string) string {
	filterPart := ""
	if filter != "" {
		filterPart = " /Filter " + filter
	}
	return fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(data), filterPart, data)
}

func TestGetContentConcatenatesContentsArray(t *testing.T) {
	// /Contents may be an array of streams, and an array may contain a nested
	// array. The streams are concatenated in order.
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents [4 0 R [5 0 R]] >>",
		4: streamObject([]byte("q "), ""),
		5: streamObject([]byte("Q\n"), ""),
	}

	got := readPageContent(t, fixturePDF(t, objects), 1)
	if got != "q Q\n" {
		t.Fatalf("content = %q, want %q", got, "q Q\n")
	}
}

func TestGetContentWithoutContents(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> >>",
	}

	reader := newTestReader(t, fixturePDF(t, objects))
	content, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Fatalf("content = %q, want an empty string", content)
	}
}

// TestStreamFiltersResolveIndirectReferences covers /Filter entries that are
// indirect objects, both as a single value and as array elements.
func TestStreamFiltersResolveIndirectReferences(t *testing.T) {
	content := "BT /F1 12 Tf 100 700 Td (hello) Tj ET\n"

	tests := []struct {
		name   string
		filter string
		extra  map[int]string
		stream []byte
	}{
		{
			name:   "single reference",
			filter: "20 0 R",
			extra:  map[int]string{20: "/ASCII85Decode"},
			stream: ascii85Encode(t, []byte(content)),
		},
		{
			name:   "array of references",
			filter: "[20 0 R 21 0 R]",
			extra:  map[int]string{20: "/ASCII85Decode", 21: "/FlateDecode"},
			// Filters are listed in the order they are applied.
			stream: ascii85Encode(t, flate(t, []byte(content))),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
				4: streamObject(test.stream, test.filter),
			}
			for id, body := range test.extra {
				objects[id] = body
			}

			got := readPageContent(t, fixturePDF(t, objects), 1)
			if got != content {
				t.Fatalf("content = %q, want %q", got, content)
			}
		})
	}
}

func TestStreamFilterWithMissingReferenceReturnsError(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: streamObject([]byte("q Q\n"), "99 0 R"),
	}

	reader := newTestReader(t, fixturePDF(t, objects))
	if _, err := reader.getContent(1); err == nil {
		t.Fatal("expected an error for a filter that does not exist")
	}
}

func TestGetContentRejectsNonStreamContent(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: "<< /Type /Font /BaseFont /Helvetica >>",
	}

	reader := newTestReader(t, fixturePDF(t, objects))
	if _, err := reader.getContent(1); err == nil {
		t.Fatal("expected an error for content that is not a stream")
	}

	writer := newTestWriter(t)
	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err == nil {
		t.Fatal("expected ImportPage to reject non-stream content")
	}
}

// TestNestedPageTreeIsFlattened checks a page tree with an intermediate /Pages
// node. Real PDFs routinely split large page trees into several levels.
func TestNestedPageTreeIsFlattened(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 2 /MediaBox [0 0 400 500] >>",
		3: "<< /Type /Pages /Parent 2 0 R /Kids [4 0 R 5 0 R] /Count 2 /Rotate 90 >>",
		4: "<< /Type /Page /Parent 3 0 R /Resources << >> /Contents 6 0 R >>",
		5: "<< /Type /Page /Parent 3 0 R /Resources << >> /Contents 7 0 R >>",
		6: streamObject([]byte("q Q\n"), ""),
		7: streamObject([]byte("BT ET\n"), ""),
	}

	reader := newTestReader(t, fixturePDF(t, objects))

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

func TestGetPageBoxesRejectsNonArrayBox(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox /Bad /Resources << >> >>",
	}

	reader := newTestReader(t, fixturePDF(t, objects))
	if _, err := reader.getPageBoxes(1, 1); err == nil {
		t.Fatal("expected an error for a box that is not an array")
	}
	if _, err := reader.getAllPageBoxes(1); err == nil {
		t.Fatal("expected getAllPageBoxes to report the broken box")
	}
}

// TestReadAcceptsMissingTrailingNewline covers files whose last bytes are
// "%%EOF" without a line ending after it.
func TestReadAcceptsMissingTrailingNewline(t *testing.T) {
	pdf := buildSinglePagePDF(t, "q Q\n")
	pdf = bytes.TrimRight(pdf, "\r\n")

	reader := newTestReader(t, pdf)
	content, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("content = %q, want %q", content, "q Q\n")
	}
}

func TestDecodeASCII85RejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "z inside a group", input: "!z~>"},
		{name: "truncated single character", input: "!~>"},
		{name: "invalid character", input: "v~>"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeASCII85([]byte(test.input)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestDecodeASCII85WithoutEndMarker checks data that ends without "~>", which
// some producers write.
func TestDecodeASCII85WithoutEndMarker(t *testing.T) {
	got, err := decodeASCII85([]byte("87cURD]i,\"Ebo80"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, []byte("Hello World!")) {
		t.Fatalf("decoded = %q", got)
	}
}
