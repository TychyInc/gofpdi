package gofpdi

import (
	"bytes"
	"strings"
	"testing"
)

// TestBrokenTrailerOrCatalogIsAnError covers documents whose trailer or catalog
// lacks a required entry. Each of these dereferenced nil before the fix.
func TestBrokenTrailerOrCatalogIsAnError(t *testing.T) {
	withoutRoot := bytes.Replace(classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
	}), []byte("/Root 1 0 R"), []byte("/Info 1 0 R"), 1)

	tests := []struct {
		name    string
		pdf     []byte
		wantErr string
	}{
		{name: "trailer without /Root", pdf: withoutRoot, wantErr: "no /Root"},
		{
			name: "trailer /Root is not a reference",
			pdf: bytes.Replace(classicPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
			}), []byte("/Root 1 0 R"), []byte("/Root 42"), 1),
			wantErr: "not an object reference",
		},
		{name: "catalog is not a dictionary", pdf: classicPDF(map[int]string{1: "42"}), wantErr: "not a dictionary"},
		{name: "catalog without /Pages", pdf: classicPDF(map[int]string{1: "<< /Type /Catalog >>"}), wantErr: "no /Pages"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(test.pdf))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestBrokenXrefStreamDictionaryIsAnError covers cross-reference streams whose
// /W or /Size is malformed. A /W with fewer than three entries indexed out of
// range; a missing /Size without /Index dereferenced nil. Later entries in the
// dictionary override the ones written by the builder.
func TestBrokenXrefStreamDictionaryIsAnError(t *testing.T) {
	tests := []struct {
		name, extra, wantErr string
		skipIndex            bool
	}{
		{name: "W with two widths", extra: " /W [1 4]", wantErr: "/W"},
		{name: "W is not an array", extra: " /W 7", wantErr: "/W"},
		{name: "W with a name", extra: " /W [1 /Foo 2]", wantErr: "/W"},
		{name: "Size is not a number", extra: " /Size /Foo", skipIndex: true, wantErr: "/Size"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{
				Extra:         test.extra,
				SkipIndexDict: test.skipIndex,
			}).bytes()

			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestXrefStreamWithoutSizeOrIndex checks a cross-reference stream that has
// neither /Index nor /Size. Before the fix this dereferenced nil.
func TestXrefStreamWithoutSizeOrIndex(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{SkipIndexDict: true}).bytes()
	pdf = bytes.Replace(pdf, []byte("/Size "), []byte("/Sxze "), 1)

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err == nil || !strings.Contains(err.Error(), "/Size") {
		t.Fatalf("err = %v, want it to mention /Size", err)
	}
}

// TestReadRejectsEncryptedPDF covers files that declare /Encrypt, both with a
// classic trailer and with a cross-reference stream. The parser used to read
// them until a stream failed to inflate, so the error described the symptom
// ("zlib: invalid header") instead of the cause.
func TestReadRejectsEncryptedPDF(t *testing.T) {
	tests := []struct {
		name string
		pdf  []byte
	}{
		{
			name: "classic trailer",
			pdf: func() []byte {
				b := newPDF("1.5")
				b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
				b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
				b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
				b.stream(4, "", []byte("q Q\n"))
				b.object(5, "<< /Filter /Standard /V 2 /R 3 /O <00> /U <00> /P -4 >>")
				b.classicXrefRange(0, 6, " /Encrypt 5 0 R")
				return b.bytes()
			}(),
		},
		{
			name: "cross-reference stream dictionary",
			pdf: streamXrefPDF("0 0 595 842", xrefStreamOptions{
				Filter: filterFlate,
				Extra:  " /Encrypt 9 0 R",
			}).bytes(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("encrypted.pdf", bytes.NewReader(test.pdf))
			if err == nil || !strings.Contains(err.Error(), "Encrypted PDF is not supported") {
				t.Fatalf("err = %v, want it to report an encrypted PDF", err)
			}
		})
	}
}
