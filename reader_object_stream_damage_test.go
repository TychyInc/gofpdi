package gofpdi

import (
	"bytes"
	"strings"
	"testing"
)

// TestResolveCompressedObjectReportsBrokenObjectStream covers the parts of an
// object stream that hold the sub-objects: the filter chain, the "id offset"
// header and the object stream's own cross-reference entry.
func TestResolveCompressedObjectReportsBrokenObjectStream(t *testing.T) {
	tests := []struct {
		name, dict, header, wantErr string
	}{
		{
			name:    "unsupported filter",
			dict:    "/Type /ObjStm /N 1 /First 4 /Filter 42",
			header:  "4 0 ",
			wantErr: "/Filter",
		},
		{
			name:    "data is not compressed",
			dict:    "/Type /ObjStm /N 1 /First 4 /Filter /FlateDecode",
			header:  "4 0 ",
			wantErr: "decode object stream",
		},
		{
			name:    "object index in the header is not a number",
			dict:    "/Type /ObjStm /N 1 /First 4",
			header:  "x 0 ",
			wantErr: "convert token into integer",
		},
		{
			name:    "object offset in the header is not a number",
			dict:    "/Type /ObjStm /N 1 /First 4",
			header:  "4 y ",
			wantErr: "convert token into integer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := objectStreamPDF(test.dict, test.header, 0)

			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestResolveCompressedObjectReportsObjectStreamEntryMismatch covers an object
// stream whose own cross-reference entry points at a different object. The
// object stream has to be resolved as an object stream, or the entries it
// describes would be read from unrelated data.
func TestResolveCompressedObjectReportsObjectStreamEntryMismatch(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")

	// Object 6 is the object stream, but its entry points at object 3.
	pageOffset := b.offset()
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>")
	b.stream(6, "/Type /ObjStm /N 1 /First 4", []byte("3 0 << /Type /Page /Parent 2 0 R >>"))
	b.compressed(3, 6, 0)
	b.setEntry(6, xrefEntry{objType: 1, field2: pageOffset})
	b.xrefStream(7, xrefStreamOptions{})

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
	if err == nil || !strings.Contains(err.Error(), "Failed to resolve compressed object") {
		t.Fatalf("err = %v, want the object stream entry to be reported", err)
	}
}
