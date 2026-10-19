package gofpdi

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNewPdfWriterCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.pdf")

	writer, err := NewPdfWriter(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if writer.f == nil || writer.w == nil {
		t.Fatal("writer should own an open file")
	}
	if err := writer.f.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("output file should exist: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("output file should be empty before anything is written, got %d bytes", info.Size())
	}

	if _, err := NewPdfWriter(filepath.Join(path, "nested", "out.pdf")); err == nil {
		t.Fatal("expected an error for an uncreatable path")
	}
}

func TestImportPageRotations(t *testing.T) {
	tests := []struct {
		name           string
		dict           string
		wantW          float64
		wantH          float64
		wantRotation   int
		wantMatrixLine bool
	}{
		{name: "unrotated", dict: "", wantW: 595, wantH: 842},
		{name: "90", dict: "/Rotate 90", wantW: 842, wantH: 595, wantRotation: -90, wantMatrixLine: true},
		{name: "180", dict: "/Rotate 180", wantW: 595, wantH: 842, wantRotation: -180, wantMatrixLine: true},
		{name: "270", dict: "/Rotate 270", wantW: 842, wantH: 595, wantRotation: -270, wantMatrixLine: true},
		// -90 rotates by 270 degrees; steps is -1 and the page is swapped.
		{name: "negative", dict: "/Rotate -90", wantW: 842, wantH: 595, wantRotation: -270, wantMatrixLine: true},
		// A full turn is no rotation at all, and angles above 360 are reduced.
		{name: "360", dict: "/Rotate 360", wantW: 595, wantH: 842},
		{name: "450", dict: "/Rotate 450", wantW: 842, wantH: 595, wantRotation: -90, wantMatrixLine: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{dict: test.dict}})
			reader := newTestReader(t, pdf)
			writer := newTestWriter(t)

			tplID, err := writer.ImportPage(reader, 1, "/MediaBox")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tplID != 0 {
				t.Fatalf("unexpected template id: %d", tplID)
			}

			tpl := writer.tpls[tplID]
			if tpl.W != test.wantW || tpl.H != test.wantH {
				t.Fatalf("template size = %vx%v, want %vx%v", tpl.W, tpl.H, test.wantW, test.wantH)
			}
			if tpl.Rotation != test.wantRotation {
				t.Fatalf("rotation = %d, want %d", tpl.Rotation, test.wantRotation)
			}

			names, err := writer.PutFormXobjects(reader)
			if err != nil {
				t.Fatalf("failed to put form xobjects: %v", err)
			}
			objID, ok := names["/GOFPDITPL0"]
			if !ok {
				t.Fatalf("missing template object: %v", names)
			}
			// GetImportedObjects is keyed by the object id created while writing,
			// not by the PdfObjectId returned from PutFormXobjects.
			var body string
			for id, content := range writer.GetImportedObjects() {
				if id.id == objID.id {
					body = string(content)
				}
			}
			if body == "" {
				t.Fatalf("template object %d was not written", objID.id)
			}
			if hasMatrix := bytes.Contains([]byte(body), []byte("/Matrix [")); hasMatrix != test.wantMatrixLine {
				t.Fatalf("matrix line present = %t, want %t in:\n%s", hasMatrix, test.wantMatrixLine, body)
			}
			if !bytes.Contains([]byte(body), []byte("/BBox [0.00 0.00 595.00 842.00]")) {
				t.Fatalf("unexpected BBox:\n%s", body)
			}
		})
	}
}

func TestImportPageInheritsBoxes(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tpl := writer.tpls[0]
	if tpl.Box["llx"] != 0 || tpl.Box["lly"] != 0 || tpl.Box["urx"] != 595 || tpl.Box["ury"] != 842 {
		t.Fatalf("unexpected inherited box: %v", tpl.Box)
	}
}

func TestImportPageResolvesIndirectBox(t *testing.T) {
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox 5 0 R /Contents 4 0 R /Resources << >> >>",
		4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
		5: "[10 20 110 220]",
	}
	reader := newTestReader(t, classicPDF(objects))
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tpl := writer.tpls[0]
	if tpl.W != 100 || tpl.H != 200 {
		t.Fatalf("template size = %vx%v, want 100x200", tpl.W, tpl.H)
	}
	if tpl.Box["x"] != 10 || tpl.Box["y"] != 20 {
		t.Fatalf("unexpected box origin: %v", tpl.Box)
	}
}

func TestImportPageRejectsUnknownBox(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	_, err := writer.ImportPage(reader, 1, "/FooBox")
	if err == nil {
		t.Fatal("expected an error for an unknown box")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("/FooBox")) {
		t.Fatalf("error should name the box: %v", err)
	}
}

func TestImportPageRejectsMissingPage(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 2, "/MediaBox"); err == nil {
		t.Fatal("expected an error for a missing page")
	}
}

func TestGetTemplateSizeKeepsAspectRatio(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	size := writer.getTemplateSize(0, 0, 0)
	if size["w"] != 595 || size["h"] != 842 {
		t.Fatalf("natural size = %v, want 595x842", size)
	}

	size = writer.getTemplateSize(0, 119, 0)
	if size["w"] != 119 {
		t.Fatalf("width = %v, want 119", size["w"])
	}
	if want := 119.0 * 842 / 595; size["h"] != want {
		t.Fatalf("height = %v, want %v", size["h"], want)
	}

	size = writer.getTemplateSize(0, 0, 421)
	if want := 421.0 * 595 / 842; size["w"] != want {
		t.Fatalf("width = %v, want %v", size["w"], want)
	}
}

func TestUseTemplate(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	name, scaleX, scaleY, tx, ty := writer.UseTemplate(0, 10, 20, 0, 0)
	if name != "/GOFPDITPL0" {
		t.Fatalf("template name = %q", name)
	}
	if scaleX != 1 || scaleY != 1 {
		t.Fatalf("unexpected scale: %v %v", scaleX, scaleY)
	}
	if tx != 10 {
		t.Fatalf("tx = %v, want 10", tx)
	}
	if want := -20.0 - 842; ty != want {
		t.Fatalf("ty = %v, want %v", ty, want)
	}

	writer.SetTplIdOffset(7)
	name, scaleX, _, _, _ = writer.UseTemplate(0, 0, 0, 297.5, 0)
	if name != "/GOFPDITPL7" {
		t.Fatalf("template name with offset = %q", name)
	}
	if scaleX != 0.5 {
		t.Fatalf("scaleX = %v, want 0.5", scaleX)
	}
}

func TestWriteValue(t *testing.T) {
	newWriter := func() *PdfWriter {
		writer := &PdfWriter{}
		writer.Init()
		writer.r = &PdfReader{sourceFile: "test.pdf"}
		writer.current_obj = &PdfObject{id: &PdfObjectId{id: 1, hash: "hash"}, buffer: new(bytes.Buffer)}
		writer.written_obj_pos[writer.current_obj.id] = make(map[int]string)
		return writer
	}

	tests := []struct {
		name  string
		value *PdfValue
		want  string
	}{
		{
			name:  "token",
			value: &PdfValue{Type: PDF_TYPE_TOKEN, Token: "q"},
			want:  "q ",
		},
		{
			name:  "numeric",
			value: &PdfValue{Type: PDF_TYPE_NUMERIC, Int: 42},
			want:  "42 ",
		},
		{
			name:  "real",
			value: &PdfValue{Type: PDF_TYPE_REAL, Real: 1.5},
			want:  "1.5 ",
		},
		{
			name:  "small nonzero real",
			value: &PdfValue{Type: PDF_TYPE_REAL, Real: 0.0000001},
			want:  "0.0000001 ",
		},
		{
			name:  "negative small real",
			value: &PdfValue{Type: PDF_TYPE_REAL, Real: -0.0000001},
			want:  "-0.0000001 ",
		},
		{
			name:  "high precision real",
			value: &PdfValue{Type: PDF_TYPE_REAL, Real: 1.23456789},
			want:  "1.23456789 ",
		},
		{
			name:  "large real without exponent",
			value: &PdfValue{Type: PDF_TYPE_REAL, Real: 1e20},
			want:  "100000000000000000000 ",
		},
		{
			name: "array",
			value: &PdfValue{Type: PDF_TYPE_ARRAY, Array: []*PdfValue{
				{Type: PDF_TYPE_NUMERIC, Int: 1},
				{Type: PDF_TYPE_NUMERIC, Int: 2},
			}},
			want: "[1 2 ]\n",
		},
		{
			name: "dictionary",
			value: &PdfValue{Type: PDF_TYPE_DICTIONARY, Dictionary: map[string]*PdfValue{
				"/A": {Type: PDF_TYPE_NUMERIC, Int: 5},
			}},
			want: "<</A 5 >>",
		},
		{
			name:  "string",
			value: &PdfValue{Type: PDF_TYPE_STRING, String: "abc"},
			want:  "(abc)",
		},
		{
			name:  "hex",
			value: &PdfValue{Type: PDF_TYPE_HEX, String: "AB"},
			want:  "<AB>",
		},
		{
			name:  "true",
			value: &PdfValue{Type: PDF_TYPE_BOOLEAN, Bool: true},
			want:  "true ",
		},
		{
			name:  "false",
			value: &PdfValue{Type: PDF_TYPE_BOOLEAN, Bool: false},
			want:  "false ",
		},
		{
			name:  "null",
			value: &PdfValue{Type: PDF_TYPE_NULL},
			want:  "null ",
		},
		{
			name: "stream",
			value: &PdfValue{Type: PDF_TYPE_STREAM,
				Value: &PdfValue{Type: PDF_TYPE_DICTIONARY, Dictionary: map[string]*PdfValue{
					"/Length": {Type: PDF_TYPE_NUMERIC, Int: 3},
				}},
				Stream: &PdfValue{Bytes: []byte("abc")},
			},
			want: "<</Length 3 >>stream\nabc\nendstream\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := newWriter()
			writer.writeValue(test.value)

			if got := writer.current_obj.buffer.String(); got != test.want {
				t.Fatalf("writeValue = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteValueObjectReference(t *testing.T) {
	writer := &PdfWriter{}
	writer.Init()
	writer.r = &PdfReader{sourceFile: "test.pdf"}
	writer.current_obj = &PdfObject{id: &PdfObjectId{id: 1, hash: "hash"}, buffer: new(bytes.Buffer)}
	writer.written_obj_pos[writer.current_obj.id] = make(map[int]string)

	ref := &PdfValue{Type: PDF_TYPE_OBJREF, Id: 5, Gen: 0}
	writer.writeValue(ref)

	if got := writer.current_obj.buffer.String(); got != "1 0 R " {
		t.Fatalf("object reference = %q, want %q", got, "1 0 R ")
	}
	if len(writer.written_obj_pos[writer.current_obj.id]) != 1 {
		t.Fatalf("reference position should be recorded: %v", writer.written_obj_pos[writer.current_obj.id])
	}
	if writer.don_obj_stack[5] == nil || writer.obj_stack[5] == nil {
		t.Fatal("referenced object should be queued for import")
	}

	// Writing the same reference again reuses the assigned object number.
	writer.writeValue(ref)
	if got := writer.current_obj.buffer.String(); got != "1 0 R 1 0 R " {
		t.Fatalf("repeated reference = %q", got)
	}
}

func TestWriteValueObjectReferenceWithHash(t *testing.T) {
	writer := &PdfWriter{}
	writer.Init()
	writer.SetUseHash(true)
	writer.r = &PdfReader{sourceFile: "test.pdf"}
	writer.current_obj = &PdfObject{id: &PdfObjectId{id: 1, hash: "hash"}, buffer: new(bytes.Buffer)}
	writer.written_obj_pos[writer.current_obj.id] = make(map[int]string)

	writer.writeValue(&PdfValue{Type: PDF_TYPE_OBJREF, Id: 5, Gen: 0})

	want := fmt.Sprintf("%s 0 R ", writer.shaOfInt(1))
	if got := writer.current_obj.buffer.String(); got != want {
		t.Fatalf("hashed reference = %q, want %q", got, want)
	}
	if got := writer.written_obj_pos[writer.current_obj.id][0]; got != writer.shaOfInt(1) {
		t.Fatalf("recorded hash = %q", got)
	}
}

func TestSetNextObjectID(t *testing.T) {
	writer := newTestWriter(t)
	writer.r = &PdfReader{sourceFile: "test.pdf"}
	writer.SetNextObjectID(10)
	writer.newObj(-1, false)

	if writer.current_obj_id != 10 {
		t.Fatalf("next object id = %d, want 10", writer.current_obj_id)
	}
}

func TestClearImportedObjects(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >>", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	if _, err := writer.ImportPage(reader, 1, "/MediaBox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := writer.PutFormXobjects(reader); err != nil {
		t.Fatalf("failed to put form xobjects: %v", err)
	}
	if len(writer.GetImportedObjects()) == 0 {
		t.Fatal("expected imported objects")
	}

	writer.ClearImportedObjects()
	if got := len(writer.GetImportedObjects()); got != 0 {
		t.Fatalf("imported objects = %d, want 0", got)
	}
}

func TestShaOfIntIsStablePerSource(t *testing.T) {
	first := &PdfWriter{r: &PdfReader{sourceFile: "a.pdf"}}
	second := &PdfWriter{r: &PdfReader{sourceFile: "a.pdf"}}
	third := &PdfWriter{r: &PdfReader{sourceFile: "b.pdf"}}

	if first.shaOfInt(3) != second.shaOfInt(3) {
		t.Fatal("hash should be stable for the same source")
	}
	if first.shaOfInt(3) == third.shaOfInt(3) {
		t.Fatal("hash should depend on the source file")
	}
	if first.shaOfInt(3) == first.shaOfInt(4) {
		t.Fatal("hash should depend on the object id")
	}
}

// TestImportPageInheritsRotation checks that a /Rotate defined on the page tree
// applies to a page that does not set it, which is how rotated scans are often
// written.
func TestImportPageInheritsRotation(t *testing.T) {
	pdf := pagesPDF("/MediaBox [0 0 595 842] /Resources << >> /Rotate 90", []pageSpec{{}})
	reader := newTestReader(t, pdf)
	writer := newTestWriter(t)

	tplID, err := writer.ImportPage(reader, 1, "/MediaBox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tpl := writer.tpls[tplID]
	if tpl.Rotation != -90 {
		t.Fatalf("rotation = %d, want -90", tpl.Rotation)
	}
	if tpl.W != 842 || tpl.H != 595 {
		t.Fatalf("template size = %vx%v, want the rotated 842x595", tpl.W, tpl.H)
	}
}
