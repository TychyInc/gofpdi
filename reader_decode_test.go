package gofpdi

import (
	"bytes"
	"testing"
)

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
}

func TestDecodeASCII85RejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "z inside a group", input: "!z~>"},
		{name: "truncated single character", input: "!~>"},
		{name: "invalid character", input: "v~>"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeASCII85([]byte(test.input)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestDecodeASCII85WithoutEndMarker checks data that ends without "~>", which
// some producers write.
func TestDecodeASCII85WithoutEndMarker(t *testing.T) {
	got, err := decodeASCII85([]byte("87cURD]i,\"Ebo80"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, []byte("Hello World!")) {
		t.Fatalf("decoded = %q", got)
	}
}

func TestDecodeRunLength(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{name: "literal run", input: []byte{3, 'a', 'b', 'c', 'd', 128}, want: "abcd"},
		{name: "repeat run", input: []byte{254, 'x', 128}, want: "xxx"},
		{name: "literal past end", input: []byte{10, 'a'}, want: "a"},
		{name: "no end marker", input: []byte{1, 'a', 'b'}, want: "ab"},
		{name: "truncated repeat", input: []byte{255}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRunLength(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("decoded = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDecodeFlateAcceptsTruncatedChecksum(t *testing.T) {
	content := []byte("BT ET\n")
	compressed := flateBytes(content)
	truncated := compressed[:len(compressed)-4]

	got, err := decodeFlate(truncated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decoded = %q, want %q", got, content)
	}

	if _, err := decodeFlate([]byte{0x78, 0x9c}); err == nil {
		t.Fatal("expected an error for a stream without data")
	}
}

// TestEncodePNGRowRoundTrip guards the encoder used by the predictor fixture, so
// a decode regression cannot be hidden by a matching bug in the encoder.
func TestEncodePNGRowRoundTrip(t *testing.T) {
	rows := [][]byte{
		{1, 0, 0, 0, 10, 0, 0},
		{1, 0, 0, 1, 20, 0, 1},
		{2, 0, 0, 0, 0, 0, 3},
	}

	for filterType := byte(0); filterType <= 4; filterType++ {
		prev := make([]byte, len(rows[0]))
		for i, raw := range rows {
			encoded := encodePNGRow(raw, prev, filterType)
			decoded := decodePNGRow(t, encoded, prev, filterType)

			if !bytes.Equal(decoded, raw) {
				t.Fatalf("filter %d row %d: decoded %v, want %v", filterType, i, decoded, raw)
			}
			copy(prev, raw)
		}
	}
}

// decodePNGRow reverses encodePNGRow with the same rules the reader applies.
func decodePNGRow(t *testing.T, encoded, prev []byte, filterType byte) []byte {
	t.Helper()

	decoded := append([]byte(nil), encoded[1:]...)
	switch filterType {
	case 1: // Sub
		for i := 1; i < len(decoded); i++ {
			decoded[i] += decoded[i-1]
		}
	case 2: // Up
		for i := range decoded {
			decoded[i] += prev[i]
		}
	case 3: // Average
		for i := range decoded {
			var left byte
			if i > 0 {
				left = decoded[i-1]
			}
			decoded[i] += byte((int(left) + int(prev[i])) / 2)
		}
	case 4: // Paeth
		for i := range decoded {
			var left, upperLeft byte
			if i > 0 {
				left = decoded[i-1]
				upperLeft = prev[i-1]
			}
			decoded[i] += paeth(left, prev[i], upperLeft)
		}
	}
	return decoded
}
