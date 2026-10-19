package gofpdi

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
)

// buildHybridPDF writes a hybrid-reference file: a classic cross-reference
// table whose trailer points at a cross-reference stream through /XRefStm, and
// a page tree object (object 2) that only exists as a compressed object in an
// object stream. Readers that ignore /XRefStm cannot resolve the page tree.
func buildHybridPDF(t *testing.T) []byte {
	t.Helper()

	content := "q Q\n"
	pagesObject := "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte("2 0 " + pagesObject)); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress object stream: %v", err)
	}

	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< >>",
		6: fmt.Sprintf("<< /Type /ObjStm /N 1 /First 4 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream",
			compressed.Len(), compressed.String()),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")

	offsets := map[int]int{}
	for num := 1; num <= 6; num++ {
		body, ok := bodies[num]
		if !ok {
			// Object 2 is stored in the object stream
			continue
		}
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	xrefStreamOffset := buf.Len()
	entries := []xrefEntry{
		{objType: 1, field2: offsets[1]},
		{objType: 2, field2: 6},
		{objType: 1, field2: offsets[3]},
		{objType: 1, field2: offsets[4]},
		{objType: 1, field2: offsets[5]},
		{objType: 1, field2: offsets[6]},
		{objType: 1, field2: xrefStreamOffset},
	}
	appendXrefStreamForObjects(t, &buf, 7, entries, 8, [][2]int{{1, 7}}, nil)

	tableOffset := buf.Len()
	buf.WriteString("xref\n0 8\n0000000000 65535 f \n")
	for num := 1; num <= 7; num++ {
		if num == 2 {
			// Compressed objects are listed as free entries in a hybrid file
			buf.WriteString("0000000000 00000 f \n")
			continue
		}
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[num])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 8 /Root 1 0 R /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n",
		xrefStreamOffset, tableOffset)

	return buf.Bytes()
}

// appendXrefStreamForObjects writes a cross-reference stream with the given
// entries. prev and W are optional.
func appendXrefStreamForObjects(t *testing.T, buf *bytes.Buffer, xrefNum int, entries []xrefEntry, size int, ranges [][2]int, prev *int) {
	t.Helper()

	var raw bytes.Buffer
	for _, entry := range entries {
		raw.WriteByte(byte(entry.objType)) //nolint:gosec // entry types are 0, 1 or 2
		raw.Write([]byte{byte(entry.field2 >> 24), byte(entry.field2 >> 16), byte(entry.field2 >> 8), byte(entry.field2)})
		raw.Write([]byte{byte(entry.field3 >> 8), byte(entry.field3)})
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}

	index := ""
	for i, r := range ranges {
		if i > 0 {
			index += " "
		}
		index += fmt.Sprintf("%d %d", r[0], r[1])
	}

	dict := fmt.Sprintf("<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Index [%s] /Length %d /Filter /FlateDecode",
		size, index, compressed.Len())
	if prev != nil {
		dict += fmt.Sprintf(" /Prev %d", *prev)
	}
	dict += " >>"

	fmt.Fprintf(buf, "%d 0 obj\n%s\nstream\n%s\nendstream\nendobj\n", xrefNum, dict, compressed.String())
}

// buildOmittedTypeWidthPDF writes a PDF whose cross-reference stream uses
// /W [0 4 2]: the entry type column is omitted and every entry is a regular
// object.
func buildOmittedTypeWidthPDF(t *testing.T) []byte {
	t.Helper()

	content := "q Q\n"
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")

	offsets := map[int]int{}
	for num := 1; num <= 4; num++ {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, bodies[num])
	}

	xrefOffset := buf.Len()

	var raw bytes.Buffer
	for num := 1; num <= 5; num++ {
		offset := xrefOffset
		if num <= 4 {
			offset = offsets[num]
		}
		raw.Write([]byte{byte(offset >> 24), byte(offset >> 16), byte(offset >> 8), byte(offset)})
		raw.Write([]byte{0, 0})
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress xref stream: %v", err)
	}

	fmt.Fprintf(&buf, "5 0 obj\n<< /Type /XRef /Size 6 /W [0 4 2] /Root 1 0 R /Index [1 5] /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream\nendobj\n",
		compressed.Len(), compressed.String())
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes()
}

// TestReadHybridReferenceFile checks that the /XRefStm of a hybrid-reference
// file is read, so compressed objects (here the page tree) can be resolved.
func TestReadHybridReferenceFile(t *testing.T) {
	pdf := buildHybridPDF(t)

	reader, err := NewPdfReaderFromStream("hybrid.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 595 || got["h"] != 842 {
		t.Fatalf("unexpected page size: %+v", got)
	}

	content, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}

// TestReadXrefStreamWithoutTypeField checks a cross-reference stream with
// /W [0 4 2], where the entry type defaults to 1.
func TestReadXrefStreamWithoutTypeField(t *testing.T) {
	pdf := buildOmittedTypeWidthPDF(t)

	reader, err := NewPdfReaderFromStream("omitted-type.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 595 || got["h"] != 842 {
		t.Fatalf("unexpected page size: %+v", got)
	}
}
