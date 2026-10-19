package gofpdi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// xrefStreamData returns the raw entries for the given ranges together with the
// /Index string that describes them.
func xrefStreamData(entries []xrefEntry, ranges [][2]int) ([]byte, string) {
	var raw bytes.Buffer
	index := make([]string, 0, len(ranges)*2)
	for _, r := range ranges {
		index = append(index, fmt.Sprintf("%d %d", r[0], r[1]))
		for _, entry := range entries[r[0] : r[0]+r[1]] {
			raw.WriteByte(byte(entry.objType))
			raw.Write([]byte{byte(entry.field2 >> 24), byte(entry.field2 >> 16), byte(entry.field2 >> 8), byte(entry.field2)})
			raw.Write([]byte{byte(entry.field3 >> 8), byte(entry.field3)})
		}
	}
	return raw.Bytes(), strings.Join(index, " ")
}

// appendXrefStreamObject writes a cross-reference stream with the given data and
// extra dictionary entries (for example " /Filter /ASCII85Decode").
func appendXrefStreamObject(buf *bytes.Buffer, xrefNum, size int, index, dictExtra string, data []byte) {
	fmt.Fprintf(buf, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Index [%s] /Length %d%s >>\nstream\n%s\nendstream\nendobj\n",
		xrefNum, size, index, len(data), dictExtra, data)
}

// appendRawXrefStream writes a cross-reference stream whose entries are stored
// as they are, without /Filter. qpdf --stream-data=uncompress produces this
// form: the stream dictionary keeps /Type /XRef but loses /Filter.
func appendRawXrefStream(t *testing.T, buf *bytes.Buffer, xrefNum int, entries []xrefEntry, size int, ranges [][2]int) {
	t.Helper()

	data, index := xrefStreamData(entries, ranges)
	appendXrefStreamObject(buf, xrefNum, size, index, "", data)
}

// buildPDFWithRawXrefStream builds a PDF 1.5 file whose cross-reference table is
// an uncompressed stream, mirroring qpdf --object-streams=generate followed by
// qpdf --stream-data=uncompress. Object numbers listed in free are written as
// free entries even though their bodies are present.
func buildPDFWithRawXrefStream(t *testing.T, bodies map[int]string, xrefNum int, free ...int) []byte {
	t.Helper()

	entries := newEntries(xrefNum + 1)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")
	for num := 1; num < xrefNum; num++ {
		if body, ok := bodies[num]; ok {
			writeBody(&buf, entries, num, body)
		}
	}
	for _, num := range free {
		entries[num] = xrefEntry{objType: 0, field3: 65535}
	}

	xrefOffset := buf.Len()
	entries[xrefNum] = xrefEntry{objType: 1, field2: xrefOffset}
	appendRawXrefStream(t, &buf, xrefNum, entries, xrefNum+1, [][2]int{{0, xrefNum + 1}})
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes()
}

// TestReadUncompressedXrefStream checks that a cross-reference stream without
// /Filter is read as it is. v1.0.19 inflated every xref stream, so this file
// failed with "zlib.NewReader error: zlib: invalid header".
func TestReadUncompressedXrefStream(t *testing.T) {
	pdf := buildPDFWithRawXrefStream(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\nq Q\nendstream",
	}, 5)

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF with an uncompressed xref stream: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if len(boxes) != 1 {
		t.Fatalf("unexpected page count: %d", len(boxes))
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 595 || got["h"] != 842 {
		t.Fatalf("unexpected page size: %+v", got)
	}
}

// TestReadUncompressedXrefStreamKeepsObjectTypes checks that the entries of an
// uncompressed xref stream are decoded like the compressed ones: a free entry
// stays free and the regular entries keep their offsets.
func TestReadUncompressedXrefStreamKeepsObjectTypes(t *testing.T) {
	pdf := buildPDFWithRawXrefStream(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\nq Q\nendstream",
	}, 5, 4)

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}
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
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << >> >>",
	}
	xrefNum := 4

	entries := newEntries(xrefNum + 1)
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")
	for num := 1; num < xrefNum; num++ {
		writeBody(&buf, entries, num, bodies[num])
	}

	xrefOffset := buf.Len()
	entries[xrefNum] = xrefEntry{objType: 1, field2: xrefOffset}
	data, index := xrefStreamData(entries, [][2]int{{0, xrefNum + 1}})
	appendXrefStreamObject(&buf, xrefNum, xrefNum+1, index, " /Filter /ASCII85Decode", ascii85Encode(t, data))
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("failed to parse PDF with an ASCII85 xref stream: %v", err)
	}
	mediaBox := reader.pages[0].Value.Dictionary["/MediaBox"].Array
	if mediaBox[2].Int != 300 || mediaBox[3].Int != 200 {
		t.Fatalf("unexpected page size: %+v", mediaBox)
	}
}

// TestReadXrefStreamRejectsUnsupportedFilter checks that a filter which cannot
// appear in a cross-reference stream returns an error instead of panicking.
func TestReadXrefStreamRejectsUnsupportedFilter(t *testing.T) {
	pdf := buildPDFWithRawXrefStream(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
	}, 4)
	pdf = bytes.Replace(pdf, []byte("/Index ["), []byte("/Filter /DCTDecode /Index ["), 1)

	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
		t.Fatal("unsupported xref stream filter accepted")
	}
}
