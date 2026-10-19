package gofpdi

import (
	"bufio"
	"strings"
	"testing"
)

// parseValue reads one token from input and parses the value that follows it.
func parseValue(t *testing.T, input string) *PdfValue {
	t.Helper()

	reader := &PdfReader{}
	r := bufio.NewReader(strings.NewReader(input))

	token, err := reader.readToken(r)
	if err != nil {
		t.Fatalf("failed to read token: %v", err)
	}
	if token == "" {
		t.Fatalf("no token in %q", input)
	}

	value, err := reader.readValue(r, token)
	if err != nil {
		t.Fatalf("failed to read value: %v", err)
	}
	return value
}

func TestReadValueParsesDictionaryTypes(t *testing.T) {
	value := parseValue(t, `<<
		/Type /Font
		/Name (Helvetica \(Bold\))
		/Nested (a(b)c)
		/Hex <48656C6C6F>
		/Size 12
		/Scale -1.25
		/Flag true
		/Empty null
		/Ref 7 0 R
		/Items [1 (two) /Three]
		/Inner << /A 1 >>
	>>`)

	if value.Type != PDF_TYPE_DICTIONARY {
		t.Fatalf("type = %d, want PDF_TYPE_DICTIONARY", value.Type)
	}

	dict := value.Dictionary
	if got := dict["/Type"].Token; got != "/Font" {
		t.Fatalf("/Type = %q, want /Font", got)
	}
	if got := dict["/Name"]; got.Type != PDF_TYPE_STRING || got.String != `Helvetica \(Bold\)` {
		t.Fatalf("/Name = %#v, want the escaped literal string", got)
	}
	if got := dict["/Nested"]; got.Type != PDF_TYPE_STRING || got.String != "a(b)c" {
		t.Fatalf("/Nested = %#v, want nested parentheses preserved", got)
	}
	if got := dict["/Hex"]; got.Type != PDF_TYPE_HEX || got.String != "48656C6C6F" {
		t.Fatalf("/Hex = %#v, want a hex string", got)
	}
	if got := dict["/Size"]; got.Type != PDF_TYPE_NUMERIC || got.Int != 12 || got.Real != 12 {
		t.Fatalf("/Size = %#v, want the integer 12", got)
	}
	if got := dict["/Scale"]; got.Type != PDF_TYPE_REAL || got.Real != -1.25 {
		t.Fatalf("/Scale = %#v, want the real -1.25", got)
	}
	if got := dict["/Flag"]; got.Type != PDF_TYPE_BOOLEAN || !got.Bool {
		t.Fatalf("/Flag = %#v, want true", got)
	}
	if got := dict["/Empty"]; got.Type != PDF_TYPE_NULL {
		t.Fatalf("/Empty = %#v, want null", got)
	}
	if got := dict["/Ref"]; got.Type != PDF_TYPE_OBJREF || got.Id != 7 || got.Gen != 0 {
		t.Fatalf("/Ref = %#v, want the reference 7 0 R", got)
	}

	items := dict["/Items"]
	if items.Type != PDF_TYPE_ARRAY || len(items.Array) != 3 {
		t.Fatalf("/Items = %#v, want a three element array", items)
	}
	if got := items.Array[0]; got.Type != PDF_TYPE_NUMERIC || got.Int != 1 {
		t.Fatalf("/Items[0] = %#v, want 1", got)
	}
	if got := items.Array[1]; got.Type != PDF_TYPE_STRING || got.String != "two" {
		t.Fatalf("/Items[1] = %#v, want (two)", got)
	}
	if got := items.Array[2].Token; got != "/Three" {
		t.Fatalf("/Items[2] = %q, want /Three", got)
	}

	inner := dict["/Inner"]
	if inner.Type != PDF_TYPE_DICTIONARY || inner.Dictionary["/A"].Int != 1 {
		t.Fatalf("/Inner = %#v, want a nested dictionary", inner)
	}
}

func TestReadValueParsesDecimalNumbers(t *testing.T) {
	value := parseValue(t, "[0. +612. -792. .5 -.25]")
	want := []float64{0, 612, -792, 0.5, -0.25}
	if len(value.Array) != len(want) {
		t.Fatalf("array length = %d, want %d", len(value.Array), len(want))
	}
	for i, number := range want {
		if got := value.Array[i]; got.Type != PDF_TYPE_REAL || got.Real != number {
			t.Errorf("value[%d] = %#v, want real %g", i, got, number)
		}
	}
}

func TestReadValueParsesEmptyContainers(t *testing.T) {
	dict := parseValue(t, "<< >>")
	if dict.Type != PDF_TYPE_DICTIONARY || len(dict.Dictionary) != 0 {
		t.Fatalf("empty dictionary = %#v", dict)
	}

	array := parseValue(t, "[]")
	if array.Type != PDF_TYPE_ARRAY || len(array.Array) != 0 {
		t.Fatalf("empty array = %#v", array)
	}
}

func TestReadValueParsesObjectDeclaration(t *testing.T) {
	declaration := parseValue(t, "12 0 obj << /Type /Page >>")
	if declaration.Type != PDF_TYPE_OBJDEC || declaration.Id != 12 || declaration.Gen != 0 {
		t.Fatalf("declaration = %#v, want 12 0 obj", declaration)
	}

	reference := parseValue(t, "7 3 R")
	if reference.Type != PDF_TYPE_OBJREF || reference.Id != 7 || reference.Gen != 3 {
		t.Fatalf("reference = %#v, want 7 3 R", reference)
	}
}

func TestReadValueRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unterminated literal string", input: "("},
		{name: "unterminated hex string", input: "<41"},
		{name: "dictionary without value", input: "<< /Key >>"},
		{name: "unterminated dictionary", input: "<< /Key 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(test.input))

			token, err := reader.readToken(r)
			if err != nil {
				t.Fatalf("failed to read token: %v", err)
			}
			if _, err := reader.readValue(r, token); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestReadValueRejectsStreamKeyword(t *testing.T) {
	reader := &PdfReader{}
	r := bufio.NewReader(strings.NewReader("data"))

	if _, err := reader.readValue(r, "stream"); err == nil {
		t.Fatal("expected an error for the stream keyword")
	}
}

func TestReadTokenDistinguishesHexAndDictionary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "hex string", input: "<41>", want: "<"},
		{name: "dictionary", input: "<< /A 1 >>", want: "<<"},
		{name: "array", input: "[1]", want: "["},
		{name: "name", input: "/Font 12 Tf", want: "/Font"},
		{name: "delimiter stays for the next token", input: "1]", want: "1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(test.input))

			got, err := reader.readToken(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("token = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadTokenAtEOF(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty input", input: "", want: ""},
		// A delimiter is not required after the last token in a file.
		{name: "number", input: "12", want: "12"},
		{name: "name", input: "/Name", want: "/Name"},
		{name: "keyword", input: "R", want: "R"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(test.input))

			token, err := reader.readToken(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token != test.want {
				t.Fatalf("token = %q, want %q", token, test.want)
			}
		})
	}
}

func TestReadTokenSkipsComments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// A lone carriage return ends the comment; the following byte is not
		// part of the comment even though it is not a line feed.
		{name: "carriage return", input: "%note\rTOKEN", want: "TOKEN"},
		{name: "carriage return line feed", input: "%note\r\n[", want: "["},
		{name: "line feed", input: "%note\nTOKEN", want: "TOKEN"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(test.input))

			got, err := reader.readToken(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("token = %q, want %q", got, test.want)
			}
		})
	}

	for _, input := range []string{"%note", "%note\r"} {
		t.Run("comment ends at EOF "+input, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(input))

			token, err := reader.readToken(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token != "" {
				t.Fatalf("token = %q, want an empty token after a trailing comment", token)
			}
		})
	}
}

func TestSkipStreamWhitespaceKeepsData(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Without an end-of-line marker the byte belongs to the stream data and
		// must not be consumed.
		{name: "no end of line", input: "abc", want: "abc"},
		{name: "line feed", input: "\nabc", want: "abc"},
		{name: "carriage return line feed", input: "\r\nabc", want: "abc"},
		{name: "lone carriage return", input: "\rabc", want: "abc"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &PdfReader{}
			r := bufio.NewReader(strings.NewReader(test.input))

			if err := reader.skipStreamWhitespace(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := r.Peek(len(test.want))
			if err != nil {
				t.Fatalf("failed to peek: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("remaining data = %q, want %q", got, test.want)
			}
		})
	}

	t.Run("empty input", func(t *testing.T) {
		reader := &PdfReader{}
		r := bufio.NewReader(strings.NewReader(""))

		if err := reader.skipStreamWhitespace(r); err == nil {
			t.Fatal("expected an error at EOF")
		}
	})
}

// TestReadValuePushesLookaheadTokensBack guards the token stack. A numeric value
// looks ahead at the two following tokens to recognise an object declaration or
// a reference; anything else has to be returned by the next readToken calls in
// source order, otherwise the rest of the object is parsed in the wrong place.
func TestReadValuePushesLookaheadTokensBack(t *testing.T) {
	reader := &PdfReader{}
	r := bufio.NewReader(strings.NewReader("1 2 3 /Name"))

	token, err := reader.readToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	value, err := reader.readValue(r, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value.Type != PDF_TYPE_NUMERIC || value.Int != 1 {
		t.Fatalf("value = %#v, want the number 1", value)
	}

	for _, want := range []string{"2", "3", "/Name", ""} {
		got, err := reader.readToken(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Fatalf("token = %q, want %q", got, want)
		}
	}
}

// TestReadTokenRejectsLoneHexStart checks the incomplete delimiter case: "<"
// needs a second byte to decide between a hex string and a dictionary.
func TestReadTokenRejectsLoneHexStart(t *testing.T) {
	reader := &PdfReader{}
	r := bufio.NewReader(strings.NewReader("<"))

	if _, err := reader.readToken(r); err == nil {
		t.Fatal("expected an error")
	}
}
