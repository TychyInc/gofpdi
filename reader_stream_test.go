package gofpdi

import (
	"bytes"
	"strings"
	"testing"
)

const testContent = "BT /F1 12 Tf 100 700 Td (hello) Tj ET\n"

// filteredPagePDF returns a one-page PDF whose content stream is declared with
// the given /Filter entry. The stream bytes are written as they are.
func filteredPagePDF(filter string, stream []byte) []byte {
	return classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: streamObject(stream, filter),
	})
}

// pagePDFWithObjects returns a one-page PDF that uses the extra objects as page
// resources or content.
func pagePDFWithObjects(pageDict string, extra map[int]string) []byte {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> " + pageDict + " >>",
	}
	for id, body := range extra {
		objects[id] = body
	}
	return classicPDF(objects)
}

func TestGetContentConcatenatesContentsArray(t *testing.T) {
	// /Contents may be an array of streams, and an array may contain a nested
	// array. The streams are concatenated in order.
	pdf := pagePDFWithObjects("/Contents [4 0 R [5 0 R]]", map[int]string{
		4: streamObject([]byte("q "), ""),
		5: streamObject([]byte("Q\n"), ""),
	})

	if got := readPageContent(t, pdf, 1); got != "q Q\n" {
		t.Fatalf("content = %q, want %q", got, "q Q\n")
	}
}

func TestGetContentWithoutContents(t *testing.T) {
	pdf := pagePDFWithObjects("", nil)

	content, err := newTestReader(t, pdf).getContent(1)
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
			stream: ascii85Bytes([]byte(testContent)),
		},
		{
			name:   "array of references",
			filter: "[20 0 R 21 0 R]",
			extra:  map[int]string{20: "/ASCII85Decode", 21: "/FlateDecode"},
			// Filters are listed in the order they are applied.
			stream: ascii85Bytes(flateBytes([]byte(testContent))),
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

			if got := readPageContent(t, classicPDF(objects), 1); got != testContent {
				t.Fatalf("content = %q, want %q", got, testContent)
			}
		})
	}
}

func TestStreamFilterWithMissingReferenceReturnsError(t *testing.T) {
	pdf := pagePDFWithObjects("/Contents 4 0 R", map[int]string{
		4: streamObject([]byte("q Q\n"), "99 0 R"),
	})

	if _, err := newTestReader(t, pdf).getContent(1); err == nil {
		t.Fatal("expected an error for a filter that does not exist")
	}
}

func TestGetContentRejectsNonStreamContent(t *testing.T) {
	pdf := pagePDFWithObjects("/Contents 4 0 R", map[int]string{
		4: "<< /Type /Font /BaseFont /Helvetica >>",
	})

	reader := newTestReader(t, pdf)
	if _, err := reader.getContent(1); err == nil {
		t.Fatal("expected an error for content that is not a stream")
	}

	if _, err := newTestWriter(t).ImportPage(reader, 1, "/MediaBox"); err == nil {
		t.Fatal("expected ImportPage to reject non-stream content")
	}
}

func TestIndirectContentsArrayRejectsCycle(t *testing.T) {
	pdf := pagePDFWithObjects("/Contents 4 0 R", map[int]string{4: "[4 0 R]"})

	_, err := newTestReader(t, pdf).getContent(1)
	if err == nil || !strings.Contains(err.Error(), "cyclic page content reference") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

// TestReadAcceptsMissingTrailingNewline covers files whose last bytes are
// "%%EOF" without a line ending after it.
func TestReadAcceptsMissingTrailingNewline(t *testing.T) {
	pdf := bytes.TrimRight(singlePagePDF("q Q\n"), "\r\n")

	content, err := newTestReader(t, pdf).getContent(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("content = %q, want %q", content, "q Q\n")
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
			pdf := singlePagePDF(test.stream)
			if got := readPageContent(t, pdf, 1); got != test.stream {
				t.Fatalf("content = %q, want %q", got, test.stream)
			}
		})
	}
}

func TestGetContentHandlesCRLFAfterStreamKeyword(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\r\nq Q\n\nendstream",
	})

	content, err := newTestReader(t, pdf).getContent(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("content = %q, want %q", content, "q Q\n")
	}
}

// TestGetContentDecodesStreamFilters covers every filter the reader supports,
// alone and in a chain. Filters are listed in the order they are applied, so
// [/ASCII85Decode /FlateDecode] means Flate first and ASCII85 second.
func TestGetContentDecodesStreamFilters(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		stream []byte
	}{
		{name: "flate", filter: "/FlateDecode", stream: flateBytes([]byte(testContent))},
		{name: "ascii85", filter: "/ASCII85Decode", stream: ascii85Bytes([]byte(testContent))},
		{
			name:   "ascii85 then flate",
			filter: "[/ASCII85Decode /FlateDecode]",
			stream: ascii85Bytes(flateBytes([]byte(testContent))),
		},
		{name: "run length", filter: "/RunLengthDecode", stream: runLengthEncode([]byte(testContent))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := readPageContent(t, filteredPagePDF(test.filter, test.stream), 1)
			if got != testContent {
				t.Fatalf("content = %q, want %q", got, testContent)
			}
		})
	}
}

func TestGetContentReturnsErrorForUnsupportedFilter(t *testing.T) {
	pdf := filteredPagePDF(filterLZW, []byte("garbage"))

	_, err := newTestReader(t, pdf).getContent(1)
	if err == nil {
		t.Fatal("expected an error for an unsupported filter")
	}
	if !strings.Contains(err.Error(), filterLZW) {
		t.Fatalf("error should name the filter: %v", err)
	}
}

func TestGetContentReturnsErrorForBrokenFlateData(t *testing.T) {
	// Invalid zlib data used to panic with a nil pointer dereference because the
	// zlib reader error was ignored.
	pdf := filteredPagePDF("/FlateDecode", []byte("this is not zlib data"))

	if _, err := newTestReader(t, pdf).getContent(1); err == nil {
		t.Fatal("expected an error for broken Flate data")
	}
}
