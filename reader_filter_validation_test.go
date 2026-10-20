package gofpdi

import "testing"

func TestGetContentRejectsInvalidFilterTypes(t *testing.T) {
	for _, filter := range []string{"42", "true", "<< >>", "(/FlateDecode)", "[42]", "[null]"} {
		t.Run(filter, func(t *testing.T) {
			for _, indirect := range []bool{false, true} {
				entry := filter
				objects := map[int]string{}
				if indirect {
					entry = "5 0 R"
					objects[5] = filter
				}
				objects[4] = streamObject(flateBytes([]byte(testContent)), entry)
				reader := newTestReader(t, pagePDFWithObjects("/Contents 4 0 R", objects))
				if _, err := reader.getContent(1); err == nil {
					t.Fatalf("expected invalid /Filter to fail (indirect=%v)", indirect)
				}
				if _, err := newTestWriter(t).ImportPage(reader, 1, "/MediaBox"); err == nil {
					t.Fatalf("expected ImportPage to fail (indirect=%v)", indirect)
				}
			}
		})
	}
}

func TestGetContentAllowsNullFilter(t *testing.T) {
	// A null dictionary value is equivalent to an absent entry in PDF.
	for _, filter := range []string{"null", "5 0 R"} {
		pdf := pagePDFWithObjects("/Contents 4 0 R", map[int]string{
			4: streamObject([]byte(testContent), filter),
			5: "null",
		})
		if got := readPageContent(t, pdf, 1); got != testContent {
			t.Fatalf("content = %q, want %q", got, testContent)
		}
	}
}
