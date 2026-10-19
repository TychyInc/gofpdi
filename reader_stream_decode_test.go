package gofpdi

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"strings"
	"testing"
)

// buildStreamPDF builds a single-page PDF whose page content stream is declared
// with the given /Filter entry (for example "/ASCII85Decode" or
// "[/ASCII85Decode /FlateDecode]"). The stream bytes are passed through as-is.
func buildStreamPDF(t *testing.T, filter string, stream []byte, compression string) []byte {
	t.Helper()

	content := fmt.Sprintf("<< /Length %d /Filter %s >>\nstream\n%s\nendstream", len(stream), filter, stream)
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: content,
	}
	return buildPDF(t, objects, 1, compression)
}

// buildPDF writes a PDF with a classic cross-reference table.
func buildPDF(t *testing.T, objects map[int]string, root int, _ string) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	offsets := make(map[int]int, len(objects))
	for num := 1; num <= len(objects); num++ {
		body, ok := objects[num]
		if !ok {
			t.Fatalf("object %d is missing", num)
		}
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for num := 1; num <= len(objects); num++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[num])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, root, xrefOffset)

	return buf.Bytes()
}

func flate(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	return buf.Bytes()
}

func ascii85Encode(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	encoder := ascii85.NewEncoder(&buf)
	if _, err := encoder.Write(data); err != nil {
		t.Fatalf("failed to encode ASCII85: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("failed to encode ASCII85: %v", err)
	}
	return buf.Bytes()
}

func runLengthEncode(data []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(data); {
		// Emit literal runs of at most 128 bytes
		length := len(data) - i
		if length > 128 {
			length = 128
		}
		out.WriteByte(byte(length - 1))
		out.Write(data[i : i+length])
		i += length
	}
	out.WriteByte(128)
	return out.Bytes()
}

func readPageContent(t *testing.T, pdf []byte, page int) string {
	t.Helper()

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	content, err := reader.getContent(page)
	if err != nil {
		t.Fatalf("failed to read page content: %v", err)
	}
	return content
}

const testContent = "BT /F1 12 Tf 100 700 Td (hello) Tj ET\n"

func TestGetContentDecodesASCII85Stream(t *testing.T) {
	stream := ascii85Encode(t, []byte(testContent))
	pdf := buildStreamPDF(t, "/ASCII85Decode", stream, "")

	got := readPageContent(t, pdf, 1)
	if got != testContent {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestGetContentDecodesASCII85ThenFlate(t *testing.T) {
	// Filters are applied in the order they are listed, so the data is
	// encoded with Flate first and then with ASCII85.
	stream := ascii85Encode(t, flate(t, []byte(testContent)))
	pdf := buildStreamPDF(t, "[/ASCII85Decode /FlateDecode]", stream, "")

	got := readPageContent(t, pdf, 1)
	if got != testContent {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestGetContentDecodesRunLengthStream(t *testing.T) {
	stream := runLengthEncode([]byte(testContent))
	pdf := buildStreamPDF(t, "/RunLengthDecode", stream, "")

	got := readPageContent(t, pdf, 1)
	if got != testContent {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestGetContentDecodesFlateStream(t *testing.T) {
	stream := flate(t, []byte(testContent))
	pdf := buildStreamPDF(t, "/FlateDecode", stream, "")

	got := readPageContent(t, pdf, 1)
	if got != testContent {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestGetContentReturnsErrorForUnsupportedFilter(t *testing.T) {
	pdf := buildStreamPDF(t, "/LZWDecode", []byte("garbage"), "")

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	_, err = reader.getContent(1)
	if err == nil {
		t.Fatal("expected an error for an unsupported filter")
	}
	if !strings.Contains(err.Error(), "/LZWDecode") {
		t.Fatalf("error should name the filter: %v", err)
	}
}

func TestGetContentReturnsErrorForBrokenFlateData(t *testing.T) {
	// Invalid zlib data used to panic with a nil pointer dereference because the
	// zlib reader error was ignored.
	pdf := buildStreamPDF(t, "/FlateDecode", []byte("this is not zlib data"), "")

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	if _, err := reader.getContent(1); err == nil {
		t.Fatal("expected an error for broken Flate data")
	}
}

func TestDecodeASCII85(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "87cURD]i,\"Ebo80~>", want: "Hello World!"},
		{name: "zero shortcut", input: "z~>", want: "\x00\x00\x00\x00"},
		{name: "whitespace", input: "87cUR\nD]i,\"E\tbo80~>", want: "Hello World!"},
		{name: "partial group", input: "87cUR~>", want: "Hell"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeASCII85([]byte(test.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}

	t.Run("invalid character", func(t *testing.T) {
		if _, err := decodeASCII85([]byte("v~>")); err == nil {
			t.Fatal("expected an error for an invalid character")
		}
	})
}
