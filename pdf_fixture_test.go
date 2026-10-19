package gofpdi

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePDF writes every object in objects as a direct object and builds a
// classic cross-reference table that points at each of them. Object numbers may
// have gaps; missing numbers are written as free entries. The catalog is always
// object 1.
func fixturePDF(t *testing.T, objects map[int]string) []byte {
	t.Helper()

	maxID := 0
	for id := range objects {
		if id > maxID {
			maxID = id
		}
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")

	offsets := make(map[int]int, len(objects))
	for id := 1; id <= maxID; id++ {
		body, ok := objects[id]
		if !ok {
			continue
		}
		offsets[id] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", id, body)
	}

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", maxID+1)
	buf.WriteString("0000000000 65535 f \n")
	for id := 1; id <= maxID; id++ {
		if offset, ok := offsets[id]; ok {
			fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
		} else {
			buf.WriteString("0000000000 00000 f \n")
		}
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", maxID+1, xrefOffset)

	return buf.Bytes()
}

// pageSpec describes a page for buildPagesPDF.
type pageSpec struct {
	// dict holds extra page dictionary entries, for example "/Rotate 90".
	dict string
	// content is the page content stream. An empty value means "q Q\n".
	content string
}

// buildPagesPDF builds a PDF with the given pages. Object 1 is the catalog and
// object 2 the page tree; extraPagesDict is appended to the page tree
// dictionary so tests can put inherited entries there.
func buildPagesPDF(t *testing.T, extraPagesDict string, pages []pageSpec) []byte {
	t.Helper()

	objects := make(map[int]string, 2+2*len(pages))
	kids := make([]string, 0, len(pages))

	type pendingPage struct {
		pageID    int
		contentID int
		spec      pageSpec
	}
	pending := make([]pendingPage, 0, len(pages))

	next := 3
	for _, page := range pages {
		pageID := next
		contentID := next + 1
		next += 2
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		pending = append(pending, pendingPage{pageID: pageID, contentID: contentID, spec: page})
	}

	objects[1] = "<< /Type /Catalog /Pages 2 0 R >>"
	objects[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d %s >>",
		strings.Join(kids, " "), len(pages), extraPagesDict)

	for _, page := range pending {
		content := page.spec.content
		if content == "" {
			content = "q Q\n"
		}
		objects[page.pageID] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R %s /Contents %d 0 R >>",
			page.spec.dict, page.contentID)
		objects[page.contentID] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}

	return fixturePDF(t, objects)
}

// buildSinglePagePDF returns a one-page PDF whose content stream is body.
func buildSinglePagePDF(t *testing.T, body string) []byte {
	t.Helper()

	return fixturePDF(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
	})
}

// writeTempPDF writes pdf to a temporary file and returns its path.
func writeTempPDF(t *testing.T, pdf []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, pdf, 0o600); err != nil {
		t.Fatalf("failed to write PDF: %v", err)
	}
	return path
}
