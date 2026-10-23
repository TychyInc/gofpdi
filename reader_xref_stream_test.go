package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

// streamXrefPDF builds a one-page PDF whose cross-reference table is a stream.
// The stream is object 5.
func streamXrefPDF(mediaBox string, opts xrefStreamOptions) *pdfBuilder {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [%s] /Contents 4 0 R /Resources << >> >>", mediaBox))
	b.stream(4, "", []byte("q Q\n"))
	b.xrefStream(5, opts)
	return b
}

// assertReadablePage parses pdf and checks that its only page has the given
// size and content.
func assertReadablePage(t *testing.T, pdf []byte, wantW, wantH int, wantContent string) {
	t.Helper()

	reader := newTestReader(t, pdf)
	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if len(boxes) != 1 {
		t.Fatalf("unexpected page count: %d", len(boxes))
	}
	if got := boxes[1]["/MediaBox"]; int(got["w"]) != wantW || int(got["h"]) != wantH {
		t.Fatalf("MediaBox = %v, want %dx%d", got, wantW, wantH)
	}

	content, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("failed to read page content: %v", err)
	}
	if content != wantContent {
		t.Fatalf("content = %q, want %q", content, wantContent)
	}
}

func TestReadXrefStream(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{Filter: filterFlate}).bytes()

	assertReadablePage(t, pdf, 595, 842, "q Q\n")
}

// TestReadUncompressedXrefStream covers the layout qpdf --stream-data=uncompress
// produces: the entries of the cross-reference stream are stored as they are and
// /Filter is left out. v1.0.19 inflated every xref stream and failed with
// "zlib.NewReader error: zlib: invalid header".
func TestReadUncompressedXrefStream(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{}).bytes()

	assertReadablePage(t, pdf, 595, 842, "q Q\n")
}

// TestReadUncompressedXrefStreamKeepsObjectTypes checks that free entries in an
// uncompressed xref stream stay free and regular entries keep their offsets.
func TestReadUncompressedXrefStreamKeepsObjectTypes(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	b.object(4, "<< /Length 4 >>\nstream\nq Q\nendstream")
	b.free(4)
	b.xrefStream(5, xrefStreamOptions{})

	reader := newTestReader(t, b.bytes())
	if _, ok := reader.xref[4]; !ok || len(reader.xref[4]) != 0 {
		t.Fatalf("free entry not registered as free: %+v", reader.xref[4])
	}
	if _, err := reader.resolveObject(&PdfValue{Type: PDF_TYPE_OBJREF, Id: 4, Gen: 0}); err == nil {
		t.Fatal("free object resolved")
	}
	if _, ok := reader.xref[3]; !ok {
		t.Fatalf("regular entry missing: %+v", reader.xref)
	}
}

// TestReadXrefStreamWithASCII85Filter checks that the /Filter chain is applied to
// cross-reference streams, not only to object and content streams.
func TestReadXrefStreamWithASCII85Filter(t *testing.T) {
	pdf := streamXrefPDF("0 0 300 200", xrefStreamOptions{Filter: filterASCII85}).bytes()

	assertReadablePage(t, pdf, 300, 200, "q Q\n")
}

// TestReadXrefStreamRejectsUnsupportedFilter checks that a filter which cannot
// appear in a cross-reference stream returns an error instead of panicking.
func TestReadXrefStreamRejectsUnsupportedFilter(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{Filter: filterLZW}).bytes()

	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
		t.Fatal("unsupported xref stream filter accepted")
	}
}

// TestReadXrefStreamWithoutTypeField covers a cross-reference stream with
// /W [0 4 2]: the entry type column is omitted and every entry is a regular
// object.
func TestReadXrefStreamWithoutTypeField(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))
	b.xrefStream(5, xrefStreamOptions{
		Index:  [][2]int{{1, 5}},
		Widths: [3]int{0, 4, 2},
	})

	assertReadablePage(t, b.bytes(), 595, 842, "q Q\n")
}

// TestReadXrefStreamWithPredictors checks the PNG predictor types used by
// cross-reference streams. Encoders commonly write predictor 12 (Up), but the
// decoder has to honour whichever filter byte each row carries.
func TestReadXrefStreamWithPredictors(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))
	b.xrefStream(5, xrefStreamOptions{
		Filter:     filterFlate,
		Predictor:  12,
		RowFilters: []byte{0, 1, 2, 3, 4, 0},
	})

	reader := newTestReader(t, b.bytes())

	if offset, ok := reader.xref[1][0]; !ok || offset <= 0 {
		t.Fatalf("object 1 was not registered: %+v", reader.xref[1])
	}
	assertReadablePage(t, b.bytes(), 595, 842, "q Q\n")
}

// TestReadXrefStreamWithoutPrediction covers /DecodeParms << /Predictor 1 >>.
// Predictor 1 is legal and means "no prediction", so the rows carry no filter
// byte. Treating every /DecodeParms as PNG prediction rejected these files with
// "Incomplete xref stream entry" because the extra filter byte was assumed.
func TestReadXrefStreamWithoutPrediction(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{
		Filter:    filterFlate,
		Predictor: 1,
	}).bytes()

	assertReadablePage(t, pdf, 595, 842, "q Q\n")
}

// TestReadXrefStreamWithDecodeParmsArray covers the array form of
// /DecodeParms, which a stream with a /Filter array uses: one entry per filter,
// and only the entry of the last filter describes the final row layout.
func TestReadXrefStreamWithDecodeParmsArray(t *testing.T) {
	tests := []struct {
		name      string
		parms     string
		predictor int
		rows      []byte
	}{
		{
			name:      "predictor of the last filter",
			parms:     "[<< /Predictor 12 /Columns 7 >>]",
			predictor: 12,
			rows:      []byte{1, 4, 0},
		},
		{
			name:  "null entry means no parameters",
			parms: "[null]",
		},
		{
			name:  "empty array means no parameters",
			parms: "[]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{
				Filter:      "[/FlateDecode]",
				Predictor:   test.predictor,
				RowFilters:  test.rows,
				DecodeParms: test.parms,
			}).bytes()

			assertReadablePage(t, pdf, 595, 842, "q Q\n")
		})
	}
}

// TestReadXrefStreamWithNullDecodeParms covers /DecodeParms null, which means
// that the filter has no parameters.
func TestReadXrefStreamWithNullDecodeParms(t *testing.T) {
	pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{
		Filter:      filterFlate,
		DecodeParms: "null",
	}).bytes()

	assertReadablePage(t, pdf, 595, 842, "q Q\n")
}

// TestReadXrefStreamRejectsInvalidPredictorData checks the checks applied to a
// cross-reference stream dictionary and to the decoded entry data.
func TestReadXrefStreamRejectsInvalidPredictorData(t *testing.T) {
	base := streamXrefPDF("0 0 595 842", xrefStreamOptions{
		Index:  [][2]int{{0, 6}},
		Filter: filterFlate,
	}).bytes()

	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{
			name:    "predictor out of range",
			old:     "/Filter /FlateDecode",
			new:     "/DecodeParms << /Predictor 16 /Columns 7 >> /Filter /FlateDecode",
			wantErr: "Predictor",
		},
		{
			name:    "TIFF predictor is not supported",
			old:     "/Filter /FlateDecode",
			new:     "/DecodeParms << /Predictor 2 /Columns 7 >> /Filter /FlateDecode",
			wantErr: "Predictor",
		},
		{
			name:    "decode parms is not a dictionary",
			old:     "/Filter /FlateDecode",
			new:     "/DecodeParms /Predictor /Filter /FlateDecode",
			wantErr: "DecodeParms",
		},
		{
			name:    "index without pairs",
			old:     "/Index [0 6]",
			new:     "/Index [0 6 1]",
			wantErr: "pairs",
		},
		{
			name:    "field width too large",
			old:     "/W [1 4 2]",
			new:     "/W [9 4 2]",
			wantErr: "field widths",
		},
		{
			name:    "entry does not fill the row width",
			old:     "/W [1 4 2]",
			new:     "/W [1 5 2]",
			wantErr: "Incomplete",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := bytes.Replace(base, []byte(test.old), []byte(test.new), 1)
			if bytes.Equal(pdf, base) {
				t.Fatalf("%q was not found in the fixture", test.old)
			}

			_, err := NewPdfReaderFromStream("predictor.pdf", bytes.NewReader(pdf))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error %q should mention %q", err, test.wantErr)
			}
		})
	}
}

// TestReadHybridReferenceFile checks that the /XRefStm of a hybrid-reference
// file is read, so compressed objects (here the page tree) can be resolved.
func TestReadHybridReferenceFile(t *testing.T) {
	pagesObject := "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
	objectStream := "2 0 " + pagesObject

	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))
	b.object(5, "<< >>")
	b.stream(6, "/Type /ObjStm /N 1 /First 4", []byte(objectStream))
	b.compressed(2, 6, 0)

	xrefStreamOffset := b.xrefStream(7, xrefStreamOptions{
		Index:    [][2]int{{1, 7}},
		Filter:   filterFlate,
		NoFooter: true,
	})

	// The classic table lists the compressed object as free and points at the
	// stream through /XRefStm, which is what hybrid-reference producers write.
	b.classicXrefRange(0, 8, fmt.Sprintf(" /XRefStm %d", xrefStreamOffset))

	assertReadablePage(t, b.bytes(), 595, 842, "q Q\n")
}

// TestReadHybridReferenceFileWithUnreachableXRefStm covers a hybrid-reference
// file whose /XRefStm points past the end of the file. Producers occasionally
// leave the offset of a revision they dropped; the classic table of the same
// revision still lists every uncompressed object, so the file stays readable.
func TestReadHybridReferenceFileWithUnreachableXRefStm(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))

	b.classicXrefRange(0, 5, fmt.Sprintf(" /XRefStm %d", b.offset()+100000))

	assertReadablePage(t, b.bytes(), 595, 842, "q Q\n")
}

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
			b := newPDF("1.5")
			b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
			b.object(2, "<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
			b.object(3, test.objectStream)
			b.compressed(4, 3, 0)
			b.object(5, "<< >>")
			b.xrefStream(6, xrefStreamOptions{})

			_, err := NewPdfReaderFromStream("objstm.pdf", bytes.NewReader(b.bytes()))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error %q should mention %q", err, test.wantErr)
			}
		})
	}
}

// TestReadCompressedObjectWithFilterArray checks that an object stored in an
// object stream declared with a filter array is decoded.
func TestReadCompressedObjectWithFilterArray(t *testing.T) {
	page := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 5 0 R /Resources << >> >>"
	objectStream := "4 0 " + page

	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
	b.object(3, "<< >>")
	b.stream(5, "", []byte("q Q\n"))
	b.stream(6, "/Type /ObjStm /N 1 /First 4 /Filter [ /FlateDecode ]", flateBytes([]byte(objectStream)))
	b.compressed(4, 6, 0)
	b.xrefStream(7, xrefStreamOptions{})

	reader := newTestReader(t, b.bytes())
	value, err := reader.resolveObject(&PdfValue{Type: PDF_TYPE_OBJREF, Id: 4, Gen: 0})
	if err != nil {
		t.Fatalf("failed to resolve compressed object: %v", err)
	}
	if value.Value.Dictionary["/Type"].Token != "/Page" {
		t.Fatalf("unexpected object type: %+v", value.Value.Dictionary["/Type"])
	}
}

// TestGetContentWithIndirectLengthInObjectStream checks that reading a stream
// whose /Length lives in an object stream does not corrupt the token stream of
// the surrounding object. Files re-saved by Ghostscript use this layout.
func TestGetContentWithIndirectLengthInObjectStream(t *testing.T) {
	header := "8 0 9 2 " // object 8 at data offset 0, object 9 at offset 2
	data := "4 << /Type /Font >>"

	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.object(4, "<< /Length 8 0 R >>\nstream\nq Q\n\nendstream")
	b.stream(5, "/Type /ObjStm /N 2 /First 8 /Filter /FlateDecode", flateBytes([]byte(header+data)))
	b.compressed(8, 5, 0)
	b.compressed(9, 5, 1)
	b.xrefStream(10, xrefStreamOptions{})

	content, err := newTestReader(t, b.bytes()).getContent(1)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}
