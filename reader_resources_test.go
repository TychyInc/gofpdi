package gofpdi

import (
	"strings"
	"testing"
)

func TestGetPageResourcesRejectsParentCycle(t *testing.T) {
	reader := newTestReader(t, classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Parent 2 0 R /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}))
	if _, err := reader.getPageResources(1); err == nil || !strings.Contains(err.Error(), "Cycle") {
		t.Fatalf("error = %v, want a resource inheritance cycle error", err)
	}
}

func TestImportPageInheritsNearestResources(t *testing.T) {
	for _, test := range []struct {
		name            string
		pageResources   string
		parentResources string
		wantFont        string
	}{
		{name: "grandparent indirect resources", wantFont: "/RootFont"},
		{name: "nearest parent overrides grandparent", parentResources: "/Resources << /Font << /ParentFont 6 0 R >> >>", wantFont: "/ParentFont"},
		{name: "page overrides parents", pageResources: "/Resources << /Font << /PageFont 6 0 R >> >>", parentResources: "/Resources << /Font << /ParentFont 6 0 R >> >>", wantFont: "/PageFont"},
		{name: "empty page resources stop inheritance", pageResources: "/Resources << >>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, classicPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] /Resources 5 0 R >>",
				3: "<< /Type /Pages /Parent 2 0 R /Kids [4 0 R] /Count 1 " + test.parentResources + " >>",
				4: "<< /Type /Page /Parent 3 0 R " + test.pageResources + " >>",
				5: "<< /Font << /RootFont 6 0 R >> >>",
				6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			}))
			writer, err := NewPdfWriter("")
			if err != nil {
				t.Fatal(err)
			}
			id, err := writer.ImportPage(reader, 1, "/MediaBox")
			if err != nil {
				t.Fatal(err)
			}
			resources := writer.tpls[id].Resources
			if resources.Type != PDF_TYPE_DICTIONARY {
				t.Fatalf("resources = %#v, want dictionary", resources)
			}
			if test.wantFont == "" {
				if len(resources.Dictionary) != 0 {
					t.Fatalf("resources = %v, want empty dictionary", resources.Dictionary)
				}
				return
			}
			fonts := resources.Dictionary["/Font"]
			if fonts == nil || len(fonts.Dictionary) != 1 || fonts.Dictionary[test.wantFont] == nil {
				t.Fatalf("resources = %#v, want only %s in /Font", resources, test.wantFont)
			}
			names, err := writer.PutFormXobjects(reader)
			if err != nil {
				t.Fatal(err)
			}
			body := formXObjectBody(t, writer, names, "/GOFPDITPL0")
			if !strings.Contains(body, test.wantFont) || strings.Contains(body, "/Kids") {
				t.Fatalf("form XObject must contain inherited font resources, not the page tree:\n%s", body)
			}
		})
	}
}
