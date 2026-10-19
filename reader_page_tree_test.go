package gofpdi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// pageTreePDF builds a PDF whose catalog points at object 2 and whose page tree
// dictionary is pagesDict. extra provides the objects the page tree refers to.
func pageTreePDF(pagesDict string, extra map[int]string) []byte {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: pagesDict,
	}
	for id, body := range extra {
		objects[id] = body
	}
	return classicPDF(objects)
}

// pageTreeLeaf is the page object used by the page tree tests.
func pageTreeLeaf(content int) string {
	return fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Resources << >> /Contents %d 0 R >>", content)
}

// TestReadPageTreeCountsTheTree checks that the pages are taken from the page
// tree and that /Count is only used as a hint. A /Count that is missing,
// smaller or larger than the number of leaf nodes used to panic with an index
// out of range or to leave nil pages in the page list.
func TestReadPageTreeCountsTheTree(t *testing.T) {
	tests := []struct {
		name      string
		pagesDict string
		pages     int
	}{
		{
			name:      "missing count",
			pagesDict: "<< /Type /Pages /Kids [3 0 R] >>",
			pages:     1,
		},
		{
			name:      "count larger than the tree",
			pagesDict: "<< /Type /Pages /Kids [3 0 R] /Count 9 >>",
			pages:     1,
		},
		{
			name:      "count smaller than the tree",
			pagesDict: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
			pages:     2,
		},
		{
			name:      "count is not a number",
			pagesDict: "<< /Type /Pages /Kids [3 0 R] /Count /Many >>",
			pages:     1,
		},
		{
			name:      "count is absurd",
			pagesDict: "<< /Type /Pages /Kids [3 0 R] /Count 1000000000 >>",
			pages:     1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, pageTreePDF(test.pagesDict, map[int]string{
				3: pageTreeLeaf(5),
				4: pageTreeLeaf(6),
				5: streamObject([]byte("q Q\n"), ""),
				6: streamObject([]byte("BT ET\n"), ""),
			}))

			pages, err := reader.getNumPages()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pages != test.pages {
				t.Fatalf("page count = %d, want %d", pages, test.pages)
			}

			// The pages have to keep the order of the tree, because the page
			// number is what callers pass to ImportPage.
			wantContents := []string{"q Q\n", "BT ET\n"}
			for page := 1; page <= test.pages; page++ {
				if _, err := reader.getPageBoxes(page, 1); err != nil {
					t.Fatalf("page %d: unexpected error: %v", page, err)
				}
				content, err := reader.getContent(page)
				if err != nil {
					t.Fatalf("page %d: unexpected error: %v", page, err)
				}
				if content != wantContents[page-1] {
					t.Fatalf("page %d content = %q, want %q", page, content, wantContents[page-1])
				}
			}
		})
	}
}

// TestReadPageTreeRejectsBrokenNodes covers page tree dictionaries that cannot
// be walked. Each of these dereferenced nil before.
func TestReadPageTreeRejectsBrokenNodes(t *testing.T) {
	tests := []struct {
		name    string
		pages   string
		extra   map[int]string
		wantErr string
	}{
		{
			name:    "page tree without kids",
			pages:   "<< /Type /Pages /Count 1 >>",
			wantErr: "no /Kids",
		},
		{
			name:    "page tree that is not a dictionary",
			pages:   "42",
			wantErr: "not a dictionary",
		},
		{
			name:    "root kids that is not an array",
			pages:   "<< /Type /Pages /Kids /Foo /Count 1 >>",
			wantErr: "not an array",
		},
		{
			name:    "node kids that is not an array",
			pages:   "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			extra:   map[int]string{3: "<< /Type /Pages /Kids /Foo >>"},
			wantErr: "not an array",
		},
		{
			name:    "page without type",
			pages:   "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			extra:   map[int]string{3: "<< /Parent 2 0 R >>"},
			wantErr: "no /Type",
		},
		{
			name:    "page that is not a dictionary",
			pages:   "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			extra:   map[int]string{3: "42"},
			wantErr: "not a dictionary",
		},
		{
			name:    "kids that does not exist",
			pages:   "<< /Type /Pages /Kids 9 0 R /Count 1 >>",
			wantErr: "Could not find object ID 9",
		},
		{
			name:    "page tree node without kids",
			pages:   "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			extra:   map[int]string{3: "<< /Type /Pages /Count 1 >>"},
			wantErr: "no /Kids",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pageTreePDF(test.pages, test.extra)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

// TestReadPageTreeRejectsMissingPagesObject covers a catalog whose /Pages points
// at an object that is not in the file.
func TestReadPageTreeRejectsMissingPagesObject(t *testing.T) {
	pdf := classicPDF(map[int]string{1: "<< /Type /Catalog /Pages 9 0 R >>"})

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err == nil || !strings.Contains(err.Error(), "Could not find object ID 9") {
		t.Fatalf("err = %v, want a missing /Pages object", err)
	}
}

// TestReadPageTreeAcceptsInlinePages checks a page written directly in /Kids
// instead of as an indirect object, which is legal and used to panic.
func TestReadPageTreeAcceptsInlinePages(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Resources << >> /Contents 3 0 R >>] /Count 1 >>",
		3: streamObject([]byte("q Q\n"), ""),
	})

	reader := newTestReader(t, pdf)
	boxes, err := reader.getPageBoxes(1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := boxes["/MediaBox"]; got["w"] != 200 || got["h"] != 100 {
		t.Fatalf("MediaBox = %v, want 200x100", got)
	}
	if got, err := reader.getContent(1); err != nil || got != "q Q\n" {
		t.Fatalf("content = %q (%v), want %q", got, err, "q Q\n")
	}

	// The wrapped node has no object number, so importing it must not depend on
	// the page having an id.
	writer := newTestWriter(t)
	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("failed to import the inline page: %v", err)
	}
	if _, err := writer.PutFormXobjects(reader); err != nil {
		t.Fatalf("failed to put form xobjects: %v", err)
	}
}

// TestReadPageTreeAcceptsInlinePageTree checks a page tree dictionary written
// directly in the catalog instead of as an indirect object.
func TestReadPageTreeAcceptsInlinePageTree(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages << /Type /Pages /Kids [3 0 R] /Count 1 >> >>",
		3: "<< /Type /Page /MediaBox [0 0 300 200] /Resources << >> >>",
	})

	boxes, err := newTestReader(t, pdf).getAllPageBoxes(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(boxes) != 1 {
		t.Fatalf("page count = %d, want 1", len(boxes))
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 300 || got["h"] != 200 {
		t.Fatalf("MediaBox = %v, want 300x200", got)
	}
}

// TestReadPageTreeRejectsCyclesAndDeepTrees checks the recursion limit. A /Kids
// array that points back at an ancestor used to recurse until the goroutine
// stack overflowed, which is a fatal error that recover() cannot catch.
func TestReadPageTreeRejectsCyclesAndDeepTrees(t *testing.T) {
	t.Run("self reference", func(t *testing.T) {
		pdf := classicPDF(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [2 0 R] /Count 1 >>",
		})

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
		if err == nil || !strings.Contains(err.Error(), "levels deep") {
			t.Fatalf("err = %v, want a depth error", err)
		}
	})

	t.Run("deep tree", func(t *testing.T) {
		objects := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
		const depth = maxPageTreeDepth + 10
		for i := 0; i < depth; i++ {
			objects[2+i] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", 3+i)
		}
		objects[2+depth] = "<< /Type /Page /Parent 2 0 R /Resources << >> >>"

		_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(classicPDF(objects)))
		if err == nil || !strings.Contains(err.Error(), "levels deep") {
			t.Fatalf("err = %v, want a depth error", err)
		}
	})
}

// TestReadPageTreeResolvesIndirectKids checks a /Kids entry that is an indirect
// array. It used to be read as an empty page tree, so the document silently had
// no pages.
func TestReadPageTreeResolvesIndirectKids(t *testing.T) {
	pdf := pageTreePDF("<< /Type /Pages /Kids 4 0 R /Count 1 /MediaBox [0 0 595 842] >>", map[int]string{
		4: "[3 0 R]",
		3: pageTreeLeaf(5),
		5: streamObject([]byte("q Q\n"), ""),
	})

	reader := newTestReader(t, pdf)
	pages, err := reader.getNumPages()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pages != 1 {
		t.Fatalf("page count = %d, want 1", pages)
	}
	if got, err := reader.getContent(1); err != nil || got != "q Q\n" {
		t.Fatalf("content = %q (%v), want %q", got, err, "q Q\n")
	}
}
