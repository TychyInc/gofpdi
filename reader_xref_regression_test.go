package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

func TestXrefRejectsInvalidRanges(t *testing.T) {
	bodies := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>", 2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", 3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>"}
	original := buildPDFWithXrefStream(t, bodies, 4, nil)
	for _, index := range []string{"0 1000000000000", "0 -1", "-1 5", "0 4", "9223372036854775807 5"} {
		t.Run(index, func(t *testing.T) {
			pdf := bytes.Replace(original, []byte("/Index [0 5]"), []byte("/Index ["+index+"]"), 1)
			if _, err := NewPdfReaderFromStream("test", bytes.NewReader(pdf)); err == nil {
				t.Fatal("invalid range accepted")
			}
		})
	}
	t.Run("Size without Index", func(t *testing.T) {
		pdf := bytes.Replace(original, []byte("/Index [0 5]"), nil, 1)
		pdf = bytes.Replace(pdf, []byte("/Size 5"), []byte("/Size 1000000000000"), 1)
		if _, err := NewPdfReaderFromStream("test", bytes.NewReader(pdf)); err == nil {
			t.Fatal("invalid Size accepted")
		}
	})
}

func TestXrefNewestEntryWinsAcrossTypes(t *testing.T) {
	for _, newest := range []xrefEntry{{objType: 0, field3: 1}, {objType: 1, field2: 100, field3: 1}, {objType: 2, field2: 6}} {
		for _, old := range []xrefEntry{{objType: 1, field2: 50}, {objType: 2, field2: 5}} {
			for _, classic := range []bool{false, true} {
				if classic && newest.objType == 2 {
					continue
				}
				t.Run(fmt.Sprintf("new%d-old%d-classic%v", newest.objType, old.objType, classic), func(t *testing.T) {
					var buf bytes.Buffer
					buf.WriteString("%PDF-1.5\n")
					entries := newEntries(2)
					entries[1] = old
					previous := buf.Len()
					appendXrefStream(t, &buf, 5, entries, 7, 0, [][2]int{{1, 1}})
					offset := buf.Len()
					if classic {
						status := "n"
						if newest.objType == 0 {
							status = "f"
						}
						fmt.Fprintf(&buf, "xref\n1 1\n%010d %05d %s \ntrailer\n<< /Size 7 /Root 1 0 R /Prev %d >>\n", newest.field2, newest.field3, status, previous)
					} else {
						entries[1] = newest
						appendXrefStream(t, &buf, 6, entries, 7, previous, [][2]int{{1, 1}})
					}
					reader := &PdfReader{f: bytes.NewReader(buf.Bytes()), xrefPos: offset, xref: make(map[int]map[int]int), xrefStream: make(map[int][2]int)}
					if err := reader.readXref(); err != nil {
						t.Fatal(err)
					}
					if newest.objType == 2 {
						if _, ok := reader.xref[1]; ok {
							t.Fatal("old regular entry retained")
						}
						if got := reader.xrefStream[1]; got != [2]int{newest.field2, newest.field3} {
							t.Fatalf("wrong compressed entry: %v", got)
						}
					} else {
						if _, ok := reader.xrefStream[1]; ok {
							t.Fatal("old compressed entry retained")
						}
						if newest.objType == 0 && len(reader.xref[1]) != 0 {
							t.Fatal("free object revived")
						}
						if newest.objType == 1 && (len(reader.xref[1]) != 1 || reader.xref[1][newest.field3] != newest.field2) {
							t.Fatalf("wrong regular entry: %v", reader.xref[1])
						}
					}
				})
			}
		}
	}
}

func TestNewCompressedPageOverridesOldRegular(t *testing.T) {
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\nq Q\nendstream",
	}
	base := buildPDFWithXrefStream(t, bodies, 5, nil)
	var buf bytes.Buffer
	buf.Write(base)
	entries := newEntries(8)
	page := "3 0 << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 400] /Contents 4 0 R /Resources << >> >>"
	writeBody(&buf, entries, 6, fmt.Sprintf("<< /Type /ObjStm /N 1 /First 4 /Length %d >>\nstream\n%s\nendstream", len(page), page))
	entries[3] = xrefEntry{objType: 2, field2: 6, field3: 0}
	offset := buf.Len()
	entries[7] = xrefEntry{objType: 1, field2: offset}
	appendXrefStream(t, &buf, 7, entries, 8, lastStartXref(t, base), [][2]int{{3, 1}, {6, 2}})
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", offset)
	reader, err := NewPdfReaderFromStream("test", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	box := reader.pages[0].Value.Dictionary["/MediaBox"].Array
	if box[2].Int != 200 {
		t.Fatalf("old page wins: width=%d; xref=%v compressed=%v", box[2].Int, reader.xref[3], reader.xrefStream[3])
	}
}
