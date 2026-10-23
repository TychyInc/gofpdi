package gofpdi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// replaceLast replaces the last occurrence of old in pdf. These fixtures end
// with the cross-reference section, and a one-page file has a content stream
// that uses the same keywords, so the first match is not the one to corrupt.
func replaceLast(t *testing.T, pdf []byte, old, new string) []byte {
	t.Helper()

	index := bytes.LastIndex(pdf, []byte(old))
	if index < 0 {
		t.Fatalf("fixture has no %q to corrupt", old)
	}

	out := append([]byte(nil), pdf[:index]...)
	out = append(out, new...)
	return append(out, pdf[index+len(old):]...)
}

// rawReader returns a reader positioned at xrefPos without going through
// findXref, so that a test can point the parser at data a damaged startxref
// would select.
func rawReader(pdf []byte, xrefPos int) *PdfReader {
	return &PdfReader{
		f:          bytes.NewReader(pdf),
		nBytes:     int64(len(pdf)),
		xrefPos:    xrefPos,
		xref:       make(map[int]map[int]int),
		xrefStream: make(map[int][2]int),
	}
}

// TestReadXrefStreamRejectsBrokenStructure covers cross-reference streams whose
// dictionary is intact but whose object body is damaged: the stream keyword is
// missing or a terminator is misspelled. The parser has to report the damaged
// keyword instead of reading the entries from data it does not own.
func TestReadXrefStreamRejectsBrokenStructure(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{Filter: filterFlate}).bytes()

	tests := []struct {
		name, old, new, wantErr string
	}{
		{name: "missing stream keyword", old: ">>\nstream\n", new: ">>\nstrem\n", wantErr: "next token to be: stream"},
		{name: "misspelled endstream", old: "\nendstream\n", new: "\nendstrem\n", wantErr: "next token to be: endstream"},
		{name: "misspelled endobj", old: "\nendobj\n", new: "\nendobk\n", wantErr: "next token to be: endobj"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := replaceLast(t, pdf, test.old, test.new)

			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(broken))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestReadReportsBrokenPreviousSection checks the two ways an incremental
// update links to an older section: /Prev of a cross-reference stream and
// /XRefStm of a classic trailer. Both are followed only after the current
// section was read, so a broken link has to fail the file rather than leave the
// reader with a partial table.
func TestReadReportsBrokenPreviousSection(t *testing.T) {
	t.Run("/Prev of a cross-reference stream", func(t *testing.T) {
		b := streamXrefPDF("0 0 595 842", xrefStreamOptions{})

		// A previous section whose entry count is not a number.
		broken := b.offset()
		b.raw("xref\n0 x\n")

		b.object(6, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 500] /Contents 4 0 R /Resources << >> >>")
		b.xrefStream(7, xrefStreamOptions{Prev: broken})

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
		if err == nil || !strings.Contains(err.Error(), "prev xref") {
			t.Fatalf("err = %v, want the /Prev section to be reported", err)
		}
	})

	t.Run("/XRefStm of a classic trailer", func(t *testing.T) {
		// A hybrid-reference file whose cross-reference stream has a /W that
		// the reader cannot use.
		b := newPDF("1.5")
		b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
		b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
		b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
		b.stream(4, "", []byte("q Q\n"))
		xrefOffset := b.xrefStream(5, xrefStreamOptions{Extra: " /W [1 4]", NoFooter: true})
		b.classicXrefRange(0, 6, fmt.Sprintf(" /XRefStm %d", xrefOffset))

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
		if err == nil || !strings.Contains(err.Error(), "/XRefStm") {
			t.Fatalf("err = %v, want the /XRefStm section to be reported", err)
		}
	})
}

// TestFindXrefIgnoresStartxrefPointingAtNonXrefData covers the startxref
// candidates whose offset lands on data that is neither a table nor a
// cross-reference stream object: an object reference, a free entry or the
// trailer after it. Each has to fall back to the previous revision instead of
// failing the file.
func TestFindXrefIgnoresStartxrefPointingAtNonXrefData(t *testing.T) {
	b, _ := incrementalPDF("0 0 419 595")

	tests := map[string]string{
		"comment at end of file": "%%EOF\n",
		"object reference":       "1 0 R\n",
		"free entry":             "0000000000 65535 f\n",
	}

	for name, tail := range tests {
		t.Run(name, func(t *testing.T) {
			pdf := b.bytes()
			offset := len(pdf)
			pdf = append(pdf, tail...)
			pdf = append(pdf, fmt.Sprintf("startxref\n%d\n%%%%EOF\n", offset)...)

			assertPageSize(t, pdf, 419, 595, "stale revision used")
		})
	}
}

// TestReadClassicXrefRejectsMalformedRows checks the numbers a classic table is
// made of. A damaged row used to be converted with the error ignored, which
// either dropped the entry or attributed it to a wrong offset.
func TestReadClassicXrefRejectsMalformedRows(t *testing.T) {
	pdf := singlePagePDF("q Q\n")

	tests := []struct {
		name, old, new, wantErr string
	}{
		{name: "start object is not a number", old: "xref\n0 5\n", new: "xref\nx 5\n", wantErr: "start object"},
		{name: "entry count is not a number", old: "xref\n0 5\n", new: "xref\n0 z\n", wantErr: "num object"},
		{name: "offset is not a number", old: "0000000000 00000 f", new: "?????????? 00000 f", wantErr: "object position"},
		{name: "generation is not a number", old: "0000000000 00000 f", new: "0000000000 ????? f", wantErr: "object generation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := bytes.Replace(pdf, []byte(test.old), []byte(test.new), 1)
			if bytes.Equal(broken, pdf) {
				t.Fatalf("fixture has no %q to corrupt", test.old)
			}

			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(broken))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestReadXrefRejectsDataThatIsNotAnXrefSection covers a cross-reference
// offset that points at data which is not a section at all. findXref filters
// the offsets it takes from startxref, so these are the two shapes a damaged
// /Prev or /XRefStm can still present.
func TestReadXrefRejectsDataThatIsNotAnXrefSection(t *testing.T) {
	t.Run("name token", func(t *testing.T) {
		pdf := singlePagePDF("q Q\n")
		offset := bytes.Index(pdf, []byte("/Type /Page"))
		if offset < 0 {
			t.Fatal("fixture has no page dictionary")
		}

		err := rawReader(pdf, offset).readXref()
		if err == nil || !strings.Contains(err.Error(), "Expected xref to start with") {
			t.Fatalf("err = %v, want a non-xref section to be reported", err)
		}
	})

	t.Run("unterminated dictionary", func(t *testing.T) {
		pdf := []byte("%PDF-1.5\n<< /Size 2 /Root 1 0 R")

		err := rawReader(pdf, 9).readXref()
		if err == nil || !strings.Contains(err.Error(), "Failed to read XRef stream") {
			t.Fatalf("err = %v, want the unreadable dictionary to be reported", err)
		}
	})
}

// TestIndirectStreamLengthThatCannotBeResolved covers a /Length that points at
// an object the file does not contain. The stream data must not be read with a
// length the reader could not resolve.
func TestIndirectStreamLengthThatCannotBeResolved(t *testing.T) {
	reader := newTestReader(t, contentStreamPDF("<< /Length 99 0 R >>", "0"))

	_, err := reader.getContent(1)
	if err == nil || !strings.Contains(err.Error(), "length object") {
		t.Fatalf("err = %v, want the dangling /Length to be reported", err)
	}
}
