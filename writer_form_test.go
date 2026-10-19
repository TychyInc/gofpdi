package gofpdi

import (
	"bytes"
	"strings"
	"testing"
)

// formXObjectBody returns the written body of a form XObject. The map returned
// by PutFormXobjects is keyed by a different pointer than the one used for
// written objects, so the objects are matched by their numeric id.
func formXObjectBody(t *testing.T, writer *PdfWriter, names map[string]*PdfObjectId, name string) string {
	t.Helper()

	id, ok := names[name]
	if !ok {
		t.Fatalf("missing template object %q: %v", name, names)
	}

	for writtenID, content := range writer.GetImportedObjects() {
		if writtenID.id == id.id {
			return string(content)
		}
	}

	t.Fatalf("template object %d was not written", id.id)
	return ""
}

// TestPutFormXobjectsUsesBoxOrigin checks that a page box with a non-zero origin
// (a common CropBox) is translated by the form matrix.
func TestPutFormXobjectsUsesBoxOrigin(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /CropBox [10 20 110 220] /Resources << >> /Contents 4 0 R >>",
		4: streamObject([]byte("q Q\n"), ""),
	}

	reader := newTestReader(t, classicPDF(objects))
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/CropBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names, err := writer.PutFormXobjects(reader)
	if err != nil {
		t.Fatalf("failed to put form xobjects: %v", err)
	}

	body := formXObjectBody(t, writer, names, "/GOFPDITPL0")
	if !bytes.Contains([]byte(body), []byte("/BBox [10.00 20.00 110.00 220.00]")) {
		t.Fatalf("unexpected BBox:\n%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("/Matrix [")) || !bytes.Contains([]byte(body), []byte("-10.00000 -20.00000")) {
		t.Fatalf("box origin was not translated:\n%s", body)
	}
}

func TestImportPageRejectsMissingResourcesObject(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources 99 0 R /Contents 4 0 R >>",
		4: streamObject([]byte("q Q\n"), ""),
	}

	reader := newTestReader(t, classicPDF(objects))
	writer := newTestWriter(t)

	_, err := writer.ImportPage(reader, 1, "/MediaBox")
	if err == nil {
		t.Fatal("expected an error for resources that do not exist")
	}
	if !strings.Contains(err.Error(), "resources") {
		t.Fatalf("error should mention resources: %v", err)
	}
}
