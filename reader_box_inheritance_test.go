package gofpdi

import "testing"

func TestImportPageDoesNotInheritOptionalBoxes(t *testing.T) {
	for _, box := range []string{"/BleedBox", "/TrimBox", "/ArtBox"} {
		for _, local := range []bool{false, true} {
			t.Run(box+map[bool]string{false: "/default", true: "/local"}[local], func(t *testing.T) {
				pageBox := ""
				wantW, wantH := 200.0, 300.0
				if local {
					pageBox = box + " [30 40 130 240]"
					wantW, wantH = 100, 200
				}
				r := newTestReader(t, classicPDF(map[int]string{
					1: "<< /Type /Catalog /Pages 2 0 R >>",
					2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] /CropBox [10 20 210 320] " + box + " [0 0 50 60] >>",
					3: "<< /Type /Page /Parent 2 0 R /Resources << >> " + pageBox + " >>",
				}))
				w, err := NewPdfWriter("")
				if err != nil {
					t.Fatal(err)
				}
				id, err := w.ImportPage(r, 1, box)
				if err != nil {
					t.Fatal(err)
				}
				if tpl := w.tpls[id]; tpl.W != wantW || tpl.H != wantH {
					t.Fatalf("got %gx%g, want %gx%g", tpl.W, tpl.H, wantW, wantH)
				}
			})
		}
	}
}
