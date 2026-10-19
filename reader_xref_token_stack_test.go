package gofpdi

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
)

// buildIndirectLengthPDF builds a PDF whose page content stream declares its
// /Length through an object stored in an object stream, and whose object stream
// holds a number right before a dictionary. Parsing that number reads one token
// ahead, which used to leak into the caller's token stream.
func buildIndirectLengthPDF(t *testing.T) []byte {
	t.Helper()

	content := "q Q\n"
	header := "8 0 9 2 " // object 8 at data offset 0, object 9 at offset 2
	data := "4 << /Type /Font >>"

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(header + data)); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}

	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: fmt.Sprintf("<< /Length 8 0 R >>\nstream\n%s\nendstream", content),
		5: fmt.Sprintf("<< /Type /ObjStm /N 2 /First %d /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream",
			len(header), compressed.Len(), compressed.String()),
	}

	return buildPDFWithXrefStream(t, bodies, 10, map[int]xrefEntry{
		8: {objType: 2, field2: 5, field3: 0},
		9: {objType: 2, field2: 5, field3: 1},
	})
}

// TestGetContentWithIndirectLengthInObjectStream checks that reading a stream
// whose /Length lives in an object stream does not corrupt the token stream of
// the surrounding object. Files re-saved by Ghostscript use this layout.
func TestGetContentWithIndirectLengthInObjectStream(t *testing.T) {
	pdf := buildIndirectLengthPDF(t)

	got := readPageContent(t, pdf, 1)
	if got != "q Q\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}
