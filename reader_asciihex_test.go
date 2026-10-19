package gofpdi

import (
	"encoding/hex"
	"testing"
)

func TestASCIIHexPageContent(t *testing.T) {
	for _, filter := range []string{"/ASCIIHexDecode", "/AHx", "[/ASCIIHexDecode /FlateDecode]"} {
		t.Run(filter, func(t *testing.T) {
			data := []byte(testContent)
			if filter[0] == '[' {
				data = flateBytes(data)
			}
			encoded := []byte(hex.EncodeToString(data) + ">")
			if got := readPageContent(t, filteredPagePDF(filter, encoded), 1); got != testContent {
				t.Fatalf("got %q, want %q", got, testContent)
			}
		})
	}
}

func TestASCIIHexDecode(t *testing.T) {
	for _, tt := range []struct {
		input, want string
		wantErr     bool
	}{
		{input: "61 6\t2\r6\n3\f\x00>", want: "abc"},
		{input: "aB C>", want: "\xab\xc0"},
		{input: ">ignored", want: ""},
		{input: "6g>", wantErr: true},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := decodeStreamFilter("/ASCIIHexDecode", []byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected invalid hex error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
