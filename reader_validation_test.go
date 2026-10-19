package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

// TestResolveCompressedObjectValidatesObjectStream checks the checks applied to
// the /ObjStm that holds a compressed object. A cross-reference entry that
// points at an ordinary object must not be parsed as an object stream.
func TestResolveCompressedObjectValidatesObjectStream(t *testing.T) {
	tests := []struct {
		name         string
		objectStream string
		wantErr      string
	}{
		{name: "not an object stream", objectStream: "<< /Type /Font >>", wantErr: "ObjStm"},
		{name: "missing type", objectStream: "<< >>", wantErr: "determine"},
		{name: "no sub objects", objectStream: "<< /Type /ObjStm /N 0 /First 0 >>", wantErr: "No sub objects"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bodies := map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
				3: test.objectStream,
				// Object 4 is stored in the object stream, so it has no
				// direct body here.
				5: "<< >>",
			}
			pdf := buildPDFWithXrefStream(t, bodies, 6, map[int]xrefEntry{
				4: {objType: 2, field2: 3, field3: 0},
			})

			_, err := NewPdfReaderFromStream("objstm.pdf", bytes.NewReader(pdf))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error %q should mention %q", err, test.wantErr)
			}
		})
	}
}

func TestReadXrefRejectsInvalidClassicEntryStatus(t *testing.T) {
	pdf := buildSinglePagePDF(t, "q Q\n")
	broken := bytes.Replace(pdf, []byte(" n "), []byte(" x "), 1)
	if bytes.Equal(broken, pdf) {
		t.Fatal("fixture has no classic xref entry to corrupt")
	}

	_, err := NewPdfReaderFromStream("xref.pdf", bytes.NewReader(broken))
	if err == nil {
		t.Fatal("expected an error for an invalid entry status")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("objStatus")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFindXrefSkipsStartxrefThatDoesNotPointAtXref checks that a trailing
// startxref pointing at something other than a cross-reference section is
// ignored in favour of the previous revision.
func TestFindXrefSkipsStartxrefThatDoesNotPointAtXref(t *testing.T) {
	pdf, _ := buildIncrementalPDF(t, "0 0 419 595")

	garbage := bytes.Index(pdf, []byte("startxref"))
	if garbage < 0 {
		t.Fatal("fixture has no startxref")
	}
	pdf = append(pdf, []byte(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", garbage))...)

	reader, err := NewPdfReaderFromStream("xref.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 419 || got["h"] != 595 {
		t.Fatalf("stale revision used: %+v", got)
	}
}
