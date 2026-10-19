package gofpdi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// contentStreamPDF returns a one-page PDF whose content stream object 4 has the
// given dictionary. Object 5 is available for an indirect /Length.
func contentStreamPDF(streamDict, object5 string) []byte {
	return classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 1 1] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: streamDict + "\nstream\nq Q\nendstream",
		5: object5,
	})
}

// TestInvalidStreamLengthIsAnError checks that a bad /Length is reported as an
// error. Before the fix a missing or non-integer /Length dereferenced nil, a
// negative one panicked in make, and a huge one tried to allocate that many
// bytes, which aborts the process with an out-of-memory error. The huge case
// here uses 1<<62 so that a regression panics in make instead of allocating.
func TestInvalidStreamLengthIsAnError(t *testing.T) {
	tests := []struct {
		name, dict, object5 string
	}{
		{name: "missing", dict: "<< >>", object5: "0"},
		{name: "not a number", dict: "<< /Length /Foo >>", object5: "0"},
		{name: "real", dict: "<< /Length 4.5 >>", object5: "0"},
		{name: "negative", dict: "<< /Length -5 >>", object5: "0"},
		{name: "huge", dict: fmt.Sprintf("<< /Length %d >>", int64(1)<<62), object5: "0"},
		{name: "indirect negative", dict: "<< /Length 5 0 R >>", object5: "-5"},
		{name: "indirect huge", dict: "<< /Length 5 0 R >>", object5: fmt.Sprint(int64(1) << 62)},
		{name: "indirect not a number", dict: "<< /Length 5 0 R >>", object5: "(4)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, contentStreamPDF(test.dict, test.object5))
			if _, err := reader.getContent(1); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestStreamLengthBeyondFileIsAnError checks the bound: one byte more than the
// whole file can never be a valid stream length.
func TestStreamLengthBeyondFileIsAnError(t *testing.T) {
	// The file size depends on the digits of the length, so look for a fixed
	// point.
	size := 0
	var pdf []byte
	for i := 0; i < 5; i++ {
		pdf = contentStreamPDF(fmt.Sprintf("<< /Length %d >>", size+1), "0")
		size = len(pdf)
	}

	_, err := newTestReader(t, pdf).getContent(1)
	if err == nil || !strings.Contains(err.Error(), "outside the file") {
		t.Fatalf("err = %v, want an out-of-file error", err)
	}
}

// TestIndirectStreamLength checks that a valid indirect /Length still works.
func TestIndirectStreamLength(t *testing.T) {
	content := readPageContent(t, contentStreamPDF("<< /Length 5 0 R >>", "4"), 1)
	if content != "q Q\n" {
		t.Fatalf("content = %q", content)
	}
}

// TestInvalidXrefStreamLengthIsAnError covers the same checks for the stream
// read while parsing a cross-reference stream. The later /Length entry
// overrides the correct one written by the builder.
func TestInvalidXrefStreamLengthIsAnError(t *testing.T) {
	for _, length := range []string{"-1", fmt.Sprint(int64(1) << 62), "/Foo"} {
		t.Run(length, func(t *testing.T) {
			pdf := streamXrefPDF("0 0 595 842", xrefStreamOptions{
				Filter: filterFlate,
				Extra:  " /Length " + length,
			}).bytes()

			if _, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
