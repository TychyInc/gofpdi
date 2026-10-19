package gofpdi

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestReadTokenPDFWhitespace(t *testing.T) {
	// ISO 32000 defines exactly these six bytes as whitespace.
	for _, whitespace := range []byte{0, '\t', '\n', '\f', '\r', ' '} {
		t.Run(fmt.Sprintf("0x%02x", whitespace), func(t *testing.T) {
			sep := string(whitespace)
			input := sep + strings.Join([]string{"/Name", "12", "0", "R", "true"}, sep) + sep
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(input))
			for _, want := range []string{"/Name", "12", "0", "R", "true", ""} {
				got, err := reader.readToken(r)
				if err != nil || got != want {
					t.Fatalf("readToken = %q, %v; want %q", got, err, want)
				}
			}
		})
	}
}

func TestReadPDFWithNULAndFormFeedWhitespace(t *testing.T) {
	for _, whitespace := range []byte{0, '\f'} {
		t.Run(fmt.Sprintf("0x%02x", whitespace), func(t *testing.T) {
			sep := string(whitespace)
			objects := map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
			}
			for id, body := range objects {
				objects[id] = strings.ReplaceAll(body, " ", sep)
			}
			// Whitespace in a stream is data, not a token separator.
			content := sep + "q Q\n"
			objects[4] = streamObject([]byte(content), "")
			got := readPageContent(t, classicPDF(objects), 1)
			if got != content {
				t.Fatalf("content = %q, want %q", got, content)
			}
		})
	}
}
