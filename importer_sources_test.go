package gofpdi

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestImporterSourceStreamPanicsOnBrokenPDF covers the panic contract of
// SetSourceStream: a stream that does not parse must not leave the importer
// with a reader it cannot use.
func TestImporterSourceStreamPanicsOnBrokenPDF(t *testing.T) {
	stream := bytes.NewReader([]byte("this is not a pdf"))
	var source io.ReadSeeker = stream

	importer := NewImporter()
	mustPanic(t, func() { importer.SetSourceStream(&source) })
}

// TestPutFormXobjectsUnorderedPanicsOnBrokenResources covers the panic contract
// of the hash based template writer. It is the entry point a document generator
// uses, so an unresolvable resource has to surface as a panic just like it does
// in PutFormXobjects.
func TestPutFormXobjectsUnorderedPanicsOnBrokenResources(t *testing.T) {
	stream := bytes.NewReader(classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 99 0 R >> >> /Contents 4 0 R >>",
		4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
	}))
	var source io.ReadSeeker = stream

	importer := NewImporter()
	importer.SetSourceStream(&source)
	importer.ImportPage(1, "/MediaBox")

	mustPanic(t, func() { importer.PutFormXobjectsUnordered() })
}

// TestImporterTemplateNamesAcrossSources checks the template numbering when a
// document imports pages from more than one source: the names of the second
// source continue after the templates of the first one, so the form XObjects of
// the two files cannot collide.
func TestImporterTemplateNamesAcrossSources(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.pdf")
	second := filepath.Join(dir, "second.pdf")

	if err := os.WriteFile(first, pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}}), 0o600); err != nil {
		t.Fatalf("failed to write the first source: %v", err)
	}
	if err := os.WriteFile(second, pagesPDF("/MediaBox [0 0 200 300] /Resources << >>", []pageSpec{{}}), 0o600); err != nil {
		t.Fatalf("failed to write the second source: %v", err)
	}

	importer := NewImporter()

	importer.SetSourceFile(first)
	firstTemplate := importer.ImportPage(1, "/MediaBox")
	firstNames := importer.PutFormXobjects()

	importer.SetSourceFile(second)
	secondTemplate := importer.ImportPage(1, "/MediaBox")
	secondNames := importer.PutFormXobjects()

	if firstTemplate != 0 || secondTemplate != 1 {
		t.Fatalf("template ids = %d, %d, want 0 and 1", firstTemplate, secondTemplate)
	}
	if _, ok := firstNames["/GOFPDITPL0"]; !ok || len(firstNames) != 1 {
		t.Fatalf("first source templates = %v, want only /GOFPDITPL0", firstNames)
	}
	if _, ok := secondNames["/GOFPDITPL1"]; !ok || len(secondNames) != 1 {
		t.Fatalf("second source templates = %v, want only /GOFPDITPL1", secondNames)
	}

	name, _, _, _, _ := importer.UseTemplate(secondTemplate, 0, 0, 0, 0)
	if name != "/GOFPDITPL1" {
		t.Fatalf("template name = %q, want /GOFPDITPL1", name)
	}

	w, h, err := importer.GetTemplateSize(secondTemplate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 200 || h != 300 {
		t.Fatalf("second source template size = %vx%v, want 200x300", w, h)
	}
}

// TestImporterKeepsResourcesPerPage checks that importing two pages of the same
// source gives each template its own resources: the second page must not inherit
// the resources of the first one.
func TestImporterKeepsResourcesPerPage(t *testing.T) {
	stream := bytes.NewReader(classicPDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 595 842] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Resources << /Font << /First 20 0 R >> >> /Contents 4 0 R >>",
		4:  "<< /Length 8 >>\nstream\nq Q q Q\nendstream",
		5:  "<< /Type /Page /Parent 2 0 R /Resources << /ProcSet [/PDF] >> /Contents 6 0 R >>",
		6:  "<< /Length 6 >>\nstream\nBT ET\nendstream",
		20: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}))
	var source io.ReadSeeker = stream

	importer := NewImporter()
	importer.SetSourceStream(&source)

	importer.ImportPage(1, "/MediaBox")
	importer.ImportPage(2, "/MediaBox")

	tpls := importer.GetWriter().tpls
	if len(tpls) != 2 {
		t.Fatalf("templates = %d, want 2", len(tpls))
	}
	if _, ok := tpls[0].Resources.Dictionary["/Font"]; !ok {
		t.Fatalf("first template resources = %v, want the page font", tpls[0].Resources.Dictionary)
	}
	if _, ok := tpls[1].Resources.Dictionary["/Font"]; ok {
		t.Fatalf("second template inherited the first page font: %v", tpls[1].Resources.Dictionary)
	}
	if _, ok := tpls[1].Resources.Dictionary["/ProcSet"]; !ok {
		t.Fatalf("second template resources = %v, want its own /ProcSet", tpls[1].Resources.Dictionary)
	}
}
