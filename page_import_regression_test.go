package gofpdi

import "testing"

func TestImportPageDefaultBoxes(t *testing.T) {
	for _, box := range []string{"/CropBox", "/BleedBox", "/TrimBox", "/ArtBox"} {
		for _, crop := range []string{"", " /CropBox [10 20 210 320]"} {
			t.Run(box+crop, func(t *testing.T) {
				r := newTestReader(t, buildPagesPDF(t, "/MediaBox [0 0 595 842] /Resources << >>"+crop, []pageSpec{{}}))
				w, err := NewPdfWriter("")
				if err != nil {
					t.Fatal(err)
				}
				id, err := w.ImportPage(r, 1, box)
				if err != nil {
					t.Fatal(err)
				}
				wantW, wantH := 595.0, 842.0
				if crop != "" {
					wantW, wantH = 200, 300
				}
				tpl := w.tpls[id]
				if tpl.W != wantW || tpl.H != wantH {
					t.Fatalf("got %gx%g; want %gx%g", tpl.W, tpl.H, wantW, wantH)
				}
			})
		}
	}
}

func TestImportPageIndirectContentsArray(t *testing.T) {
	for _, contents := range []string{"[4 0 R 4 0 R]", "[]"} {
		t.Run(contents, func(t *testing.T) {
			r := newTestReader(t, fixturePDF(t, map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 5 0 R >>",
				4: "<< /Length 4 >>\nstream\nq Q\n\nendstream",
				5: contents,
			}))
			w, err := NewPdfWriter("")
			if err != nil {
				t.Fatal(err)
			}
			id, err := w.ImportPage(r, 1, "/MediaBox")
			if err != nil {
				t.Fatal(err)
			}
			want := "q Q\nq Q\n"
			if contents == "[]" {
				want = ""
			}
			if got := w.tpls[id].Buffer; got != want {
				t.Fatalf("got %q; want %q", got, want)
			}
		})
	}
}
