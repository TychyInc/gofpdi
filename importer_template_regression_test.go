package gofpdi

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
)

func TestUseTemplateSizesSelectedPage(t *testing.T) {
	for _, rotated := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotated=%v", rotated), func(t *testing.T) {
			rotation := ""
			naturalW, naturalH := 100.0, 200.0
			if rotated {
				rotation = "/Rotate 90"
				naturalW, naturalH = 200, 100
			}
			data := classicPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
				3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << >> >>",
				4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 200] /Resources << >> " + rotation + " >>",
			})
			importer := NewImporter()
			var source io.ReadSeeker = bytes.NewReader(data)
			importer.SetSourceStream(&source)
			importer.ImportPage(1, "/MediaBox")
			id := importer.ImportPage(2, "/MediaBox")
			for _, tc := range []struct {
				name               string
				w, h, wantW, wantH float64
			}{
				{"natural", 0, 0, naturalW, naturalH},
				{"width only", 50, 0, 50, 50 * naturalH / naturalW},
				{"height only", 0, 50, 50 * naturalW / naturalH, 50},
				{"explicit", 30, 40, 30, 40},
			} {
				t.Run(tc.name, func(t *testing.T) {
					name, sx, sy, tx, ty := importer.UseTemplate(id, 11, 17, tc.w, tc.h)
					if name != "/GOFPDITPL1" || math.Abs(sx-tc.wantW/naturalW) > 1e-9 || math.Abs(sy-tc.wantH/naturalH) > 1e-9 || tx != 11 || math.Abs(ty-(-17-tc.wantH)) > 1e-9 {
						t.Fatalf("UseTemplate = %s %g %g %g %g; want scales %g %g, translation 11 %g", name, sx, sy, tx, ty, tc.wantW/naturalW, tc.wantH/naturalH, -17-tc.wantH)
					}
				})
			}
		})
	}
}

func TestImportPageCacheIncludesBox(t *testing.T) {
	for _, boxes := range [][]string{{"/MediaBox", "/CropBox"}, {"/CropBox", "/MediaBox"}} {
		t.Run(strings.Join(boxes, "-"), func(t *testing.T) {
			importer := NewImporter()
			var source io.ReadSeeker = bytes.NewReader(buildImporterFixture(t))
			importer.SetSourceStream(&source)
			ids := make(map[string]int)
			for _, box := range boxes {
				ids[box] = importer.ImportPage(2, box)
			}
			if ids["/MediaBox"] == ids["/CropBox"] {
				t.Fatal("different boxes reused the same template")
			}
			for _, box := range boxes {
				id := ids[box]
				if got := importer.ImportPage(2, box); got != id {
					t.Fatalf("cached %s = %d, want %d", box, got, id)
				}
				wantW, wantH := 595.0, 842.0
				bbox := "/BBox [0.00 0.00 595.00 842.00]"
				if box == "/CropBox" {
					wantW, wantH = 100, 200
					bbox = "/BBox [10.00 20.00 110.00 220.00]"
				}
				w, h, err := importer.GetTemplateSize(id)
				if err != nil || w != wantW || h != wantH {
					t.Fatalf("%s size = %g x %g, %v", box, w, h, err)
				}
				// Check the actual emitted form, not only cached dimensions.
				names := importer.PutFormXobjects()
				objects := importer.GetImportedObjects()
				object := objects[names[fmt.Sprintf("/GOFPDITPL%d", id)]]
				if !strings.Contains(object, bbox) {
					t.Fatalf("%s form missing %s: %s", box, bbox, object)
				}
			}
			// A previous successful import must not bypass validation of another box.
			mustPanic(t, func() { importer.ImportPage(2, "/UnknownBox") })
		})
	}
}
