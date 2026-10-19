package gofpdi

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// singlePagePDFWithEntry builds a one-page PDF and lets the caller override the
// cross-reference entry of an object afterwards. Objects are written in
// ascending order before the overrides are applied, so every recorded offset
// still matches the file.
func singlePagePDFWithEntry(objects map[int]string, overrides func(b *pdfBuilder)) []byte {
	b := newPDF("1.5")

	ids := make([]int, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		b.object(id, objects[id])
	}

	if overrides != nil {
		overrides(b)
	}
	b.classicXref("")
	return b.bytes()
}

func pageObjects(contentBody string) map[int]string {
	return map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: contentBody,
	}
}

// TestResolveObjectRejectsMismatchedObjects covers cross-reference entries that
// point at an object whose header does not match the reference. Reading the
// wrong object would silently import a page or a resource from another place in
// the file, so each case is reported as an error.
func TestResolveObjectRejectsMismatchedObjects(t *testing.T) {
	t.Run("object id", func(t *testing.T) {
		pdf := singlePagePDFWithEntry(pageObjects(streamObject([]byte("q Q\n"), "")), func(b *pdfBuilder) {
			other := b.offset()
			b.object(5, "<< /Type /Font /BaseFont /Other >>")
			// Object 3 is listed at the offset of object 5.
			b.setEntry(3, xrefEntry{objType: 1, field2: other})
		})

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("err = %v, want an object id mismatch", err)
		}
	})

	t.Run("generation", func(t *testing.T) {
		pdf := singlePagePDFWithEntry(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 1 R] /Count 1 >>",
		}, func(b *pdfBuilder) {
			offset := b.offset()
			// The object is written with generation 0 but the entry and the
			// reference say generation 1.
			b.raw("3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << >> >>\nendobj\n")
			b.setEntry(3, xrefEntry{objType: 1, field2: offset, field3: 1})
		})

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
		if err == nil || !strings.Contains(err.Error(), "Gen does not match") {
			t.Fatalf("err = %v, want a generation mismatch", err)
		}
	})

	t.Run("not an object declaration", func(t *testing.T) {
		pdf := singlePagePDFWithEntry(pageObjects(streamObject([]byte("q Q\n"), "")), func(b *pdfBuilder) {
			notAnObject := b.offset()
			b.raw("trailer\n")
			b.setEntry(4, xrefEntry{objType: 1, field2: notAnObject})
		})

		reader := newTestReader(t, pdf)
		_, err := reader.getContent(1)
		if err == nil || !strings.Contains(err.Error(), "Expected type to be") {
			t.Fatalf("err = %v, want an object declaration error", err)
		}
	})
}

// TestResolveObjectRejectsDanglingGeneration covers a reference whose
// generation is not in the cross-reference table and whose object is not in an
// object stream. Falling back to generation 0 would return a different object.
func TestResolveObjectRejectsDanglingGeneration(t *testing.T) {
	objects := pageObjects(streamObject([]byte("q Q\n"), ""))
	objects[3] = "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 2 R >>"
	pdf := singlePagePDFWithEntry(objects, nil)

	reader := newTestReader(t, pdf)
	_, err := reader.getContent(1)
	if err == nil || !strings.Contains(err.Error(), "xref stream or xref table") {
		t.Fatalf("err = %v, want a missing xref entry", err)
	}
}

// TestResolveObjectValidatesStreamTerminators covers stream objects that are not
// terminated correctly. A wrong /Length must not be mistaken for the end of the
// stream, and the object has to end with "endobj".
func TestResolveObjectValidatesStreamTerminators(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "length past endstream", body: "<< /Length 1 >>\nstream\nq Q\nendstream", wantErr: "endstream"},
		{name: "missing endobj", body: "<< /Length 4 >>\nstream\nq Q\nendstream\n", wantErr: "endobj"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newPDF("1.5")
			b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
			b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>")
			b.object(3, "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>")
			offset := b.offset()
			// Write the object without the wrapper the builder adds, so the
			// terminator can be left out.
			b.setEntry(4, xrefEntry{objType: 1, field2: offset})
			b.raw("4 0 obj\n%s\n", test.body)
			b.classicXref("")

			_, err := newTestReader(t, b.bytes()).getContent(1)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

func TestReadXrefStreamIgnoresUnknownEntryType(t *testing.T) {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>")
	b.stream(4, "", []byte("q Q\n"))
	// An entry type other than 0, 1 and 2 is not defined by the spec. It must
	// not be treated as a regular object.
	b.setEntry(5, xrefEntry{objType: 3, field2: b.offset()})
	b.xrefStream(6, xrefStreamOptions{})

	reader := newTestReader(t, b.bytes())
	if reader.hasXrefEntry(5) {
		t.Fatalf("object with unknown entry type was registered: %+v", reader.xref[5])
	}
	if got, err := reader.getContent(1); err != nil || got != "q Q\n" {
		t.Fatalf("content = %q (%v), want %q", got, err, "q Q\n")
	}
}

func TestReadClassicIncrementalUpdate(t *testing.T) {
	// The update lists only the range that contains the replaced page object,
	// which is what a viewer writes when it saves a file incrementally.
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	b.classicXref("")

	pageOffset := b.offset()
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 419 595] /Resources << >> >>")
	b.classicXrefRange(3, 1, fmt.Sprintf(" /Prev %d", b.lastXref))
	pdf := b.bytes()

	reader := newTestReader(t, pdf)
	if offset := reader.xref[3][0]; offset != pageOffset {
		t.Fatalf("object 3 offset = %d, want the updated %d", offset, pageOffset)
	}
	assertPageSize(t, pdf, 419, 595, "stale classic revision used")
}

func TestReadClassicIncrementalUpdateDeletesObject(t *testing.T) {
	// A free entry in the newest revision removes the object; the older
	// revision must not revive it.
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	b.classicXref("")

	b.free(3)
	b.classicXrefRange(3, 1, fmt.Sprintf(" /Prev %d", b.lastXref))

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(b.bytes()))
	if err == nil || !strings.Contains(err.Error(), "Could not find object ID 3") {
		t.Fatalf("err = %v, want the deleted page to stay deleted", err)
	}
}
