package gofpdi

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

// objectStreamPDF builds a PDF whose page (object 4) is stored in the object
// stream 6 at the given index. header is the "id offset" table of the object
// stream and streamDict its dictionary; the page dictionary follows the header
// at /First.
func objectStreamPDF(streamDict, header string, index int) []byte {
	page := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>"

	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
	b.object(3, "<< >>")
	b.stream(6, streamDict, []byte(header+page))
	b.compressed(4, 6, index)
	b.xrefStream(7, xrefStreamOptions{})
	return b.bytes()
}

func TestResolveCompressedObjectReadsTheRequestedEntry(t *testing.T) {
	// Object 4 is the second entry; the first entry is a different object.
	header := "9 0 4 0 "
	pdf := objectStreamPDF("/Type /ObjStm /N 2 /First 8", header, 1)

	value, err := newTestReader(t, pdf).resolveObject(&PdfValue{Type: PDF_TYPE_OBJREF, Id: 4})
	if err != nil {
		t.Fatalf("failed to resolve compressed object: %v", err)
	}
	if value.Id != 4 || value.Value.Dictionary["/Type"].Token != "/Page" {
		t.Fatalf("unexpected object: id=%d %+v", value.Id, value.Value.Dictionary)
	}
}

// TestResolveCompressedObjectRejectsMismatches covers xref entries that do not
// agree with the object stream. Before the fix the reader silently returned
// whatever object sat at the offset it computed (object 9, or object 0 at
// offset 0), so a page could be replaced by a different object.
func TestResolveCompressedObjectRejectsMismatches(t *testing.T) {
	tests := []struct {
		name, dict, header string
		index              int
		wantErr            string
	}{
		{name: "header names another object", dict: "/Type /ObjStm /N 1 /First 4", header: "9 0 ", wantErr: "expected 4"},
		{name: "index beyond /N", dict: "/Type /ObjStm /N 1 /First 4", header: "4 0 ", index: 5, wantErr: "index 5"},
		{name: "missing /N", dict: "/Type /ObjStm /First 4", header: "4 0 ", wantErr: "/N"},
		{name: "missing /First", dict: "/Type /ObjStm /N 1", header: "4 0 ", wantErr: "/First"},
		{name: "negative /First", dict: "/Type /ObjStm /N 1 /First -4", header: "4 0 ", wantErr: "/First"},
		{name: "offset beyond stream", dict: "/Type /ObjStm /N 1 /First 4", header: "4 9999", wantErr: "outside"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(objectStreamPDF(test.dict, test.header, test.index)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error %q should mention %q", err, test.wantErr)
			}
		})
	}
}

// TestObjectStreamWithoutStreamData checks a /Type /ObjStm dictionary that is
// not a stream. Before the fix this dereferenced nil.
func TestObjectStreamWithoutStreamData(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
	b.object(3, "<< /Type /ObjStm /N 1 /First 4 >>")
	b.compressed(4, 3, 0)
	b.xrefStream(7, xrefStreamOptions{})

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
	if err == nil || !strings.Contains(err.Error(), "no stream data") {
		t.Fatalf("err = %v, want missing stream data", err)
	}
}

// TestCompressedObjectStreamIsRejected checks object streams that are listed as
// compressed objects themselves. An entry that points at its own object stream
// used to recurse until the goroutine stack overflowed, a fatal error that
// recover() cannot catch.
func TestCompressedObjectStreamIsRejected(t *testing.T) {
	old := debug.SetMaxStack(64 << 20)
	defer debug.SetMaxStack(old)

	for name, stream := range map[string]int{"self": 8, "other compressed object": 9} {
		t.Run(name, func(t *testing.T) {
			b := newPDF("1.5")
			b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
			b.object(2, "<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
			b.object(3, "<< >>")
			b.compressed(4, 8, 0)
			b.compressed(8, stream, 0)
			b.compressed(9, 8, 1)
			b.xrefStream(7, xrefStreamOptions{})

			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
			if err == nil || !strings.Contains(err.Error(), "not an uncompressed object") {
				t.Fatalf("err = %v, want object stream error", err)
			}
		})
	}
}
