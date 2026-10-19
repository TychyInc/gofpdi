package gofpdi

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// incrementalPDF returns a one-page PDF whose cross-reference table is a stream,
// followed by an incremental update that replaces the page object with the given
// MediaBox. It also returns the offset of the update's cross-reference stream.
func incrementalPDF(newMediaBox string) (*pdfBuilder, int) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))
	b.xrefStream(5, xrefStreamOptions{})

	b.object(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [%s] /Contents 4 0 R /Resources << >> >>", newMediaBox))
	offset := b.xrefStream(6, xrefStreamOptions{Prev: previousXref})

	return b, offset
}

// assertPageSize parses pdf and checks the MediaBox of its only page.
func assertPageSize(t *testing.T, pdf []byte, wantW, wantH int, message string) {
	t.Helper()

	boxes, err := newTestReader(t, pdf).getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; int(got["w"]) != wantW || int(got["h"]) != wantH {
		t.Fatalf("%s: MediaBox = %v, want %dx%d", message, got, wantW, wantH)
	}
}

// TestReadUsesNewestRevisionOfIncrementalUpdate checks that an object replaced
// by an incremental update resolves to the new revision. The update is small
// enough that the previous startxref also falls inside the trailer window the
// reader scans.
func TestReadUsesNewestRevisionOfIncrementalUpdate(t *testing.T) {
	b, _ := incrementalPDF("0 0 419 595")

	assertPageSize(t, b.bytes(), 419, 595, "stale page size used")
}

// TestReadXrefStreamKeepsNewerEntries checks that the older cross-reference
// stream reached through /Prev does not overwrite entries of the newest one, and
// that entries after a split /Index range are attributed to the right objects.
func TestReadXrefStreamKeepsNewerEntries(t *testing.T) {
	b, xrefOffset := incrementalPDF("0 0 200 400")
	reader := newTestReader(t, b.bytes())

	mediaBox := reader.pages[0].Value.Dictionary["/MediaBox"].Array
	if mediaBox[2].Int != 200 || mediaBox[3].Int != 400 {
		t.Fatalf("stale page object resolved: %+v", mediaBox)
	}

	if offset, ok := reader.xref[6][0]; !ok || offset != xrefOffset {
		t.Fatalf("xref stream object 6 was not registered at its own offset: %+v", reader.xref[6])
	}
}

// TestReadFallsBackWhenTrailingStartxrefIsBroken checks that a file which was
// passed through a tool that appended data after %%EOF stays readable: the last
// startxref points outside of the file, so the previous revision is used.
func TestReadFallsBackWhenTrailingStartxrefIsBroken(t *testing.T) {
	b, _ := incrementalPDF("0 0 419 595")
	b.raw("startxref\n99999999\n%%%%EOF\n")

	assertPageSize(t, b.bytes(), 419, 595, "unexpected page size")
}

// TestFindXrefSkipsStartxrefThatDoesNotPointAtXref checks that a trailing
// startxref pointing at something other than a cross-reference section is
// ignored in favour of the previous revision.
func TestFindXrefSkipsStartxrefThatDoesNotPointAtXref(t *testing.T) {
	b, _ := incrementalPDF("0 0 419 595")

	pdf := b.bytes()
	notAnXref := bytes.Index(pdf, []byte("startxref"))
	if notAnXref < 0 {
		t.Fatal("fixture has no startxref")
	}
	pdf = append(pdf, []byte(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", notAnXref))...)

	assertPageSize(t, pdf, 419, 595, "stale revision used")
}

// TestXrefRejectsInvalidRanges checks the bounds applied to /Index and /Size so
// that a damaged cross-reference stream cannot make the reader allocate or loop
// on absurd values.
func TestXrefRejectsInvalidRanges(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	b.xrefStream(4, xrefStreamOptions{Index: [][2]int{{0, 5}}, Filter: filterFlate})
	original := b.bytes()

	for _, index := range []string{"0 1000000000000", "0 -1", "-1 5", "0 4", "9223372036854775807 5"} {
		t.Run(index, func(t *testing.T) {
			pdf := bytes.Replace(original, []byte("/Index [0 5]"), []byte("/Index ["+index+"]"), 1)
			if bytes.Equal(pdf, original) {
				t.Fatal("fixture has no /Index to corrupt")
			}
			if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
				t.Fatal("invalid range accepted")
			}
		})
	}

	t.Run("Size without Index", func(t *testing.T) {
		pdf := bytes.Replace(original, []byte("/Index [0 5] "), nil, 1)
		pdf = bytes.Replace(pdf, []byte("/Size 5"), []byte("/Size 1000000000000"), 1)
		if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
			t.Fatal("invalid Size accepted")
		}
	})
}

// TestXrefNewestEntryWinsAcrossTypes checks the merge order between a newer
// section and the section its /Prev points at: a newer entry wins whatever
// storage type either side uses, and a newer free entry must not be revived.
func TestXrefNewestEntryWinsAcrossTypes(t *testing.T) {
	newerEntries := []xrefEntry{
		{objType: 0, field3: 1},
		{objType: 1, field2: 100, field3: 1},
		{objType: 2, field2: 6},
	}
	olderEntries := []xrefEntry{
		{objType: 1, field2: 50},
		{objType: 2, field2: 5},
	}

	for _, newest := range newerEntries {
		for _, old := range olderEntries {
			for _, classic := range []bool{false, true} {
				// A classic table cannot describe a compressed object.
				if classic && newest.objType == 2 {
					continue
				}

				name := fmt.Sprintf("new%d-old%d-classic%v", newest.objType, old.objType, classic)
				t.Run(name, func(t *testing.T) {
					b := newPDF("1.5")
					b.setEntry(1, old)
					previous := b.xrefStream(5, xrefStreamOptions{Index: [][2]int{{1, 1}}})

					b.setEntry(1, newest)
					var offset int
					if classic {
						offset = b.classicXrefRange(1, 1, fmt.Sprintf(" /Prev %d", previous))
					} else {
						offset = b.xrefStream(6, xrefStreamOptions{
							Index: [][2]int{{1, 1}},
							Prev:  previous,
						})
					}

					reader := &PdfReader{
						f:          bytes.NewReader(b.bytes()),
						xrefPos:    offset,
						xref:       make(map[int]map[int]int),
						xrefStream: make(map[int][2]int),
					}
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
						return
					}

					if _, ok := reader.xrefStream[1]; ok {
						t.Fatal("old compressed entry retained")
					}
					if newest.objType == 0 && len(reader.xref[1]) != 0 {
						t.Fatal("free object revived")
					}
					if newest.objType == 1 {
						if len(reader.xref[1]) != 1 || reader.xref[1][newest.field3] != newest.field2 {
							t.Fatalf("wrong regular entry: %v", reader.xref[1])
						}
					}
				})
			}
		}
	}
}

// TestNewCompressedPageOverridesOldRegular checks that a page stored in a
// newer object stream replaces the regular object of the older revision.
func TestNewCompressedPageOverridesOldRegular(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	b.stream(4, "", []byte("q Q\n"))
	b.xrefStream(5, xrefStreamOptions{})

	page := "3 0 << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 400] /Contents 4 0 R /Resources << >> >>"
	b.stream(6, "/Type /ObjStm /N 1 /First 4", []byte(page))
	b.compressed(3, 6, 0)
	b.xrefStream(7, xrefStreamOptions{Prev: previousXref})

	reader := newTestReader(t, b.bytes())
	box := reader.pages[0].Value.Dictionary["/MediaBox"].Array
	if box[2].Int != 200 || box[3].Int != 400 {
		t.Fatalf("old page wins: width=%d; xref=%v compressed=%v", box[2].Int, reader.xref[3], reader.xrefStream[3])
	}
}

// TestReadXrefRejectsInvalidClassicEntryStatus checks that a status other than
// "n" or "f" in a classic table is reported instead of being treated as free.
func TestReadXrefRejectsInvalidClassicEntryStatus(t *testing.T) {
	pdf := singlePagePDF("q Q\n")
	broken := bytes.Replace(pdf, []byte(" n "), []byte(" x "), 1)
	if bytes.Equal(broken, pdf) {
		t.Fatal("fixture has no classic xref entry to corrupt")
	}

	_, err := NewPdfReaderFromStream("xref.pdf", bytes.NewReader(broken))
	if err == nil {
		t.Fatal("expected an error for an invalid entry status")
	}
	if !strings.Contains(err.Error(), "objStatus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadRejectsUnknownObjectType(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /NotAPage >>",
	})

	if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
		t.Fatal("expected an error for an unknown object type")
	}
}

func TestReadRejectsNonPDFData(t *testing.T) {
	tests := map[string][]byte{
		"not a PDF": []byte("this is not a pdf"),
		"empty":     nil,
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(data)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewPdfReaderFromFile(t *testing.T) {
	path := writeTempPDF(t, classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
	}))

	reader, err := NewPdfReader(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.sourceFile != path {
		t.Fatalf("source file = %q, want %q", reader.sourceFile, path)
	}
	pages, err := reader.getNumPages()
	if err != nil || pages != 1 {
		t.Fatalf("pages = %d (%v), want 1", pages, err)
	}

	if _, err := NewPdfReader(filepath.Join(path, "missing")); err == nil {
		t.Fatal("expected an error for a missing file")
	} else if !strings.Contains(err.Error(), "Failed to open file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
