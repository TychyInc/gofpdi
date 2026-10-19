package gofpdi

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildImporterFixture returns a two-page PDF. The first page inherits its
// MediaBox from the page tree, is rotated by 90 degrees and references a font
// object, so importing it exercises inherited boxes, rotation and imported
// resources.
func buildImporterFixture(t *testing.T) []byte {
	t.Helper()

	return fixturePDF(t, map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 595 842] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Rotate 90 /Resources << /Font << /F1 20 0 R >> >> /Contents 4 0 R >>",
		4:  "<< /Length 5 >>\nstream\n q Q\n\nendstream",
		5:  "<< /Type /Page /Parent 2 0 R /CropBox [10 20 110 220] /Resources << /ProcSet [/PDF] >> /Contents 6 0 R >>",
		6:  "<< /Length 6 >>\nstream\nBT ET\n\nendstream",
		20: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	})
}

func mustPanic(t *testing.T, fn func()) interface{} {
	t.Helper()

	var recovered interface{}
	func() {
		defer func() { recovered = recover() }()
		fn()
	}()
	if recovered == nil {
		t.Fatal("expected a panic")
	}
	return recovered
}

func TestImporterWithSourceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, buildImporterFixture(t), 0o600); err != nil {
		t.Fatalf("failed to write source PDF: %v", err)
	}

	importer := NewImporter()
	if importer.GetReader() != nil || importer.GetWriter() != nil {
		t.Fatal("reader and writer require a source")
	}

	importer.SetSourceFile(path)

	reader := importer.GetReader()
	writer := importer.GetWriter()
	if reader == nil || writer == nil {
		t.Fatal("source was not opened")
	}
	if importer.GetReaderForFile(path) != reader || importer.GetWriterForFile(path) != writer {
		t.Fatal("the source should be cached per file")
	}
	if importer.GetReaderForFile("/other.pdf") != nil {
		t.Fatal("unknown files should not resolve")
	}

	if got := importer.GetNumPages(); got != 2 {
		t.Fatalf("page count = %d, want 2", got)
	}

	sizes := importer.GetPageSizes()
	if got := sizes[1]["/MediaBox"]["w"]; got != 595 {
		t.Fatalf("page 1 MediaBox width = %v, want 595", got)
	}
	if got := sizes[2]["/CropBox"]["w"]; got != 100 {
		t.Fatalf("page 2 CropBox width = %v, want 100", got)
	}

	if tpl := importer.ImportPage(1, "/MediaBox"); tpl != 0 {
		t.Fatalf("first template id = %d, want 0", tpl)
	}
	if tpl := importer.ImportPage(1, "/MediaBox"); tpl != 0 {
		t.Fatalf("importing the same page again returned %d, want the cached 0", tpl)
	}
	if tpl := importer.ImportPage(2, "/CropBox"); tpl != 1 {
		t.Fatalf("second template id = %d, want 1", tpl)
	}

	w, h, err := importer.GetTemplateSize(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 842 || h != 595 {
		t.Fatalf("rotated template size = %vx%v, want 842x595", w, h)
	}
	if w, h, err = importer.GetTemplateSize(1); err != nil || w != 100 || h != 200 {
		t.Fatalf("second template size = %vx%v (%v), want 100x200", w, h, err)
	}
	if _, _, err := importer.GetTemplateSize(99); err == nil {
		t.Fatal("expected an error for an unknown template")
	}

	name, scaleX, scaleY, tx, ty := importer.UseTemplate(0, 10, 20, 0, 0)
	if name != "/GOFPDITPL0" || scaleX != 1 || scaleY != 1 || tx != 10 || ty != -20-595 {
		t.Fatalf("UseTemplate = %q %v %v %v %v", name, scaleX, scaleY, tx, ty)
	}

	names := importer.PutFormXobjects()
	if len(names) != 2 {
		t.Fatalf("template objects = %v, want 2 entries", names)
	}
	for _, name := range []string{"/GOFPDITPL0", "/GOFPDITPL1"} {
		if names[name] == 0 {
			t.Fatalf("missing object id for %s: %v", name, names)
		}
	}

	objects := importer.GetImportedObjects()
	if len(objects) == 0 {
		t.Fatal("expected imported objects")
	}
	if !containsObjectContent(objects, "/BaseFont /Helvetica") {
		t.Fatalf("referenced font object was not imported: %v", objects)
	}

	// The template resources reference the font through an indirect object, so
	// at least one hash position must have been recorded for the template.
	positions := importer.GetImportedObjHashPos()
	tplHash := ""
	for hash, posMap := range positions {
		if len(posMap) > 0 && tplHash == "" {
			tplHash = hash
		}
	}
	if tplHash == "" {
		t.Fatalf("no object reference positions were recorded: %v", positions)
	}

	unordered := importer.PutFormXobjectsUnordered()
	for name, hash := range unordered {
		if len(hash) != 40 {
			t.Fatalf("hash for %s = %q, want 40 hex characters", name, hash)
		}
	}
	unorderedObjects := importer.GetImportedObjectsUnordered()
	if len(unorderedObjects) == 0 {
		t.Fatal("expected imported objects with hashes")
	}
	if !containsUnorderedContent(unorderedObjects, "/BaseFont /Helvetica") {
		t.Fatalf("referenced font object was not imported in hash mode")
	}
}

func containsObjectContent(objects map[int]string, needle string) bool {
	for _, content := range objects {
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func containsUnorderedContent(objects map[string][]byte, needle string) bool {
	for _, content := range objects {
		if bytes.Contains(content, []byte(needle)) {
			return true
		}
	}
	return false
}

func TestImporterWithSourceStream(t *testing.T) {
	stream := bytes.NewReader(buildImporterFixture(t))
	var source io.ReadSeeker = stream

	importer := NewImporter()
	importer.SetSourceStream(&source)

	if importer.GetReader() == nil || importer.GetWriter() == nil {
		t.Fatal("stream source was not opened")
	}
	if got := importer.GetNumPages(); got != 2 {
		t.Fatalf("page count = %d, want 2", got)
	}

	if tpl := importer.ImportPage(2, "/CropBox"); tpl != 0 {
		t.Fatalf("template id = %d, want 0", tpl)
	}

	names := importer.PutFormXobjectsUnordered()
	if len(names) != 1 || len(names["/GOFPDITPL0"]) != 40 {
		t.Fatalf("unexpected template objects: %v", names)
	}
	if len(importer.GetImportedObjectsUnordered()) == 0 {
		t.Fatal("expected imported objects")
	}
}

func TestImporterSetNextObjectID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, buildImporterFixture(t), 0o600); err != nil {
		t.Fatalf("failed to write source PDF: %v", err)
	}

	importer := NewImporter()
	importer.SetSourceFile(path)
	importer.ImportPage(1, "/MediaBox")
	importer.SetNextObjectID(1000)

	for name, id := range importer.PutFormXobjects() {
		if id < 1000 {
			t.Fatalf("object id for %s = %d, want >= 1000", name, id)
		}
	}
}

func TestImporterPanicsOnMissingSource(t *testing.T) {
	importer := NewImporter()

	recovered := mustPanic(t, func() { importer.SetSourceFile(filepath.Join(t.TempDir(), "missing.pdf")) })
	if recovered == nil {
		t.Fatal("missing source should panic")
	}
}

func TestImporterPanicsOnMissingPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, buildImporterFixture(t), 0o600); err != nil {
		t.Fatalf("failed to write source PDF: %v", err)
	}

	importer := NewImporter()
	importer.SetSourceFile(path)

	mustPanic(t, func() { importer.ImportPage(9, "/MediaBox") })
}

func TestImporterHighObjectIDs(t *testing.T) {
	// Object numbers above 9999 used to be dropped by putImportedObjects.
	pdf := fixturePDF(t, map[int]string{
		1:     "<< /Type /Catalog /Pages 2 0 R >>",
		2:     "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3:     "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 10000 0 R >> >> /Contents 4 0 R >>",
		4:     "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		10000: "<< /Type /Font /Subtype /Type1 /BaseFont /HighIDFont >>",
	})

	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := writer.PutFormXobjects(reader); err != nil {
		t.Fatalf("failed to put form xobjects: %v", err)
	}

	if !containsObjectContent(byteMapToStrings(writer.GetImportedObjects()), "/BaseFont /HighIDFont") {
		t.Fatalf("object 10000 was not imported: %v", writer.GetImportedObjects())
	}
	if len(writer.obj_stack) != 0 {
		t.Fatalf("object 10000 should be removed from the stack: %v", writer.obj_stack)
	}
}

func byteMapToStrings(objects map[*PdfObjectId][]byte) map[int]string {
	converted := make(map[int]string, len(objects))
	for id, content := range objects {
		converted[id.id] = string(content)
	}
	return converted
}

func TestPutImportedObjectsDrainsStack(t *testing.T) {
	// Every referenced object has to be written and removed from the stack,
	// including objects that are discovered while resolving another object.
	pdf := fixturePDF(t, map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 20 0 R /F2 21 0 R /F3 22 0 R >> >> /Contents 4 0 R >>",
		4:  "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		20: "<< /Type /Font /BaseFont /Font20 /Descendant 21 0 R >>",
		21: "<< /Type /Font /BaseFont /Font21 >>",
		22: "<< /Type /Font /BaseFont /Font22 >>",
	})

	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := writer.PutFormXobjects(reader); err != nil {
		t.Fatalf("failed to put form xobjects: %v", err)
	}

	if len(writer.obj_stack) != 0 {
		t.Fatalf("object stack should be empty after import: %v", writer.obj_stack)
	}
	for _, marker := range []string{"/Font20", "/Font21", "/Font22"} {
		if !containsObjectContent(byteMapToStrings(writer.GetImportedObjects()), marker) {
			t.Fatalf("%s was not imported", marker)
		}
	}
}

func TestPutFormXobjectsRejectsInvalidTemplates(t *testing.T) {
	reader := &PdfReader{sourceFile: "test.pdf"}

	t.Run("nil template", func(t *testing.T) {
		writer := newTestWriter(t)
		writer.tpls = append(writer.tpls, nil)

		if _, err := writer.PutFormXobjects(reader); err == nil {
			t.Fatal("expected an error for a nil template")
		}
	})

	t.Run("missing resources", func(t *testing.T) {
		writer := newTestWriter(t)
		writer.tpls = append(writer.tpls, &PdfTemplate{
			Buffer: "q Q",
			Box:    map[string]float64{"llx": 0, "lly": 0, "urx": 595, "ury": 842},
		})

		_, err := writer.PutFormXobjects(reader)
		if err == nil {
			t.Fatal("expected an error for missing resources")
		}
		if !strings.Contains(err.Error(), "resources") {
			t.Fatalf("error should mention resources: %v", err)
		}
	})
}

func TestPutFormXobjectsWrapsResolveErrors(t *testing.T) {
	pdf := fixturePDF(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 99 0 R >> >> /Contents 4 0 R >>",
		4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
	})

	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := writer.PutFormXobjects(reader)
	if err == nil {
		t.Fatal("expected an error for an unresolvable resource")
	}
	if !strings.Contains(err.Error(), "Failed to put imported objects") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImporterFixtureObjectCount(t *testing.T) {
	// Guard the fixture itself: the page tree must stay readable so the tests
	// above fail for the reason they describe.
	reader := newTestReader(t, buildImporterFixture(t))
	pages, err := reader.getNumPages()
	if err != nil || pages != 2 {
		t.Fatalf("fixture pages = %d (%v), want 2", pages, err)
	}
	if got := fmt.Sprint(reader.sourceFile); got != "test.pdf" {
		t.Fatalf("source file = %q", got)
	}
}
