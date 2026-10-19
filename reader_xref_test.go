package gofpdi

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
)

// xrefEntry describes one entry of a cross-reference stream.
type xrefEntry struct {
	objType int
	field2  int
	field3  int
}

// newEntries returns a free cross-reference entry for every object number.
func newEntries(count int) []xrefEntry {
	entries := make([]xrefEntry, count)
	for i := range entries {
		entries[i] = xrefEntry{objType: 0, field3: 65535}
	}
	return entries
}

// appendXrefStream writes a cross-reference stream that describes the entries
// in the given ranges. Ranges are (first object number, count) pairs.
func appendXrefStream(t *testing.T, buf *bytes.Buffer, xrefNum int, entries []xrefEntry, size, prev int, ranges [][2]int) {
	t.Helper()

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

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}

	dict := fmt.Sprintf("<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Index [%s] /Length %d /Filter /FlateDecode",
		size, joinStrings(index), compressed.Len())
	if prev != 0 {
		dict += fmt.Sprintf(" /Prev %d", prev)
	}
	dict += " >>"

	fmt.Fprintf(buf, "%d 0 obj\n%s\nstream\n%s\nendstream\nendobj\n", xrefNum, dict, compressed.String())
}

func joinStrings(values []string) string {
	var buf bytes.Buffer
	for i, value := range values {
		if i > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(value)
	}
	return buf.String()
}

// writeBody writes one uncompressed object and records its offset.
func writeBody(buf *bytes.Buffer, entries []xrefEntry, num int, body string) {
	entries[num] = xrefEntry{objType: 1, field2: buf.Len()}
	fmt.Fprintf(buf, "%d 0 obj\n%s\nendobj\n", num, body)
}

// buildPDFWithXrefStream writes a PDF 1.5 file whose cross-reference table is a
// single stream. compressed lists the object numbers that are stored inside an
// object stream; their entries are kept as they are.
func buildPDFWithXrefStream(t *testing.T, bodies map[int]string, xrefNum int, compressed map[int]xrefEntry) []byte {
	t.Helper()

	entries := newEntries(xrefNum + 1)
	for num, entry := range compressed {
		entries[num] = entry
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")
	for num := 1; num < xrefNum; num++ {
		if body, ok := bodies[num]; ok {
			writeBody(&buf, entries, num, body)
		}
	}

	xrefOffset := buf.Len()
	entries[xrefNum] = xrefEntry{objType: 1, field2: xrefOffset}
	appendXrefStream(t, &buf, xrefNum, entries, xrefNum+1, 0, [][2]int{{0, xrefNum + 1}})
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes()
}

// buildIncrementalPDF returns a PDF with an incremental update. The updated page
// object and the new cross-reference stream are stored in two separate /Index
// ranges, which is what incremental saves commonly produce.
func buildIncrementalPDF(t *testing.T, newMediaBox string) ([]byte, int) {
	t.Helper()

	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\nq Q\nendstream",
	}
	base := buildPDFWithXrefStream(t, bodies, 5, nil)
	baseStartXref := lastStartXref(t, base)

	var buf bytes.Buffer
	buf.Write(base)

	pageOffset := buf.Len()
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [%s] /Contents 4 0 R /Resources << >> >>\nendobj\n", newMediaBox)

	xrefOffset := buf.Len()
	entries := newEntries(7)
	entries[3] = xrefEntry{objType: 1, field2: pageOffset}
	entries[6] = xrefEntry{objType: 1, field2: xrefOffset}
	appendXrefStream(t, &buf, 6, entries, 7, baseStartXref, [][2]int{{3, 1}, {6, 1}})
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes(), xrefOffset
}

func lastStartXref(t *testing.T, pdf []byte) int {
	t.Helper()

	index := bytes.LastIndex(pdf, []byte("startxref"))
	if index < 0 {
		t.Fatal("no startxref in PDF")
	}
	var offset int
	if _, err := fmt.Sscanf(string(pdf[index:]), "startxref\n%d", &offset); err != nil {
		t.Fatalf("failed to parse startxref: %v", err)
	}
	return offset
}

// TestReadUsesNewestRevisionOfIncrementalUpdate checks that an object replaced
// by an incremental update resolves to the new revision. The update is small
// enough that the previous startxref also falls inside the trailer window the
// reader scans.
func TestReadUsesNewestRevisionOfIncrementalUpdate(t *testing.T) {
	pdf, _ := buildIncrementalPDF(t, "0 0 419 595")

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}

	got := boxes[1]["/MediaBox"]
	if got["w"] != 419 || got["h"] != 595 {
		t.Fatalf("stale page size used: %+v", got)
	}
}

// TestReadXrefStreamKeepsNewerEntries checks that the older cross-reference
// stream reached through /Prev does not overwrite entries of the newest one, and
// that entries after a split /Index range are attributed to the right objects.
func TestReadXrefStreamKeepsNewerEntries(t *testing.T) {
	pdf, xrefOffset := buildIncrementalPDF(t, "0 0 200 400")

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	mediaBox := reader.pages[0].Value.Dictionary["/MediaBox"].Array
	if mediaBox[2].Int != 200 || mediaBox[3].Int != 400 {
		t.Fatalf("stale page object resolved: %+v", mediaBox)
	}

	if offset, ok := reader.xref[6][0]; !ok || offset != xrefOffset {
		t.Fatalf("xref stream object 6 was not registered at its own offset: %+v", reader.xref[6])
	}
}

// TestReadCompressedObjectWithFilterArray checks that an object stored in an
// object stream declared with a filter array is decoded.
func TestReadCompressedObjectWithFilterArray(t *testing.T) {
	page := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 5 0 R /Resources << >> >>"
	raw := fmt.Sprintf("4 0 %s", page)

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(raw)); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}

	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		3: "<< >>",
		5: "<< /Length 4 >>\nstream\nq Q\nendstream",
		6: fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Filter [ /FlateDecode ] /Length %d >>\nstream\n%s\nendstream",
			len("4 0 "), compressed.Len(), compressed.String()),
	}
	pdf := buildPDFWithXrefStream(t, bodies, 7, map[int]xrefEntry{
		4: {objType: 2, field2: 6, field3: 0},
	})

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	value, err := reader.resolveObject(&PdfValue{Type: PDF_TYPE_OBJREF, Id: 4, Gen: 0})
	if err != nil {
		t.Fatalf("failed to resolve compressed object: %v", err)
	}
	if value.Value.Dictionary["/Type"].Token != "/Page" {
		t.Fatalf("unexpected object type: %+v", value.Value.Dictionary["/Type"])
	}
}

// TestReadFallsBackWhenTrailingStartxrefIsBroken checks that a file which was
// passed through a tool that appended data after %%EOF stays readable: the last
// startxref points outside of the file, so the previous revision is used.
func TestReadFallsBackWhenTrailingStartxrefIsBroken(t *testing.T) {
	pdf, _ := buildIncrementalPDF(t, "0 0 419 595")
	pdf = append(pdf, []byte("startxref\n99999999\n%%EOF\n")...)

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 419 || got["h"] != 595 {
		t.Fatalf("unexpected page size: %+v", got)
	}
}
