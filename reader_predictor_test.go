package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

// encodePNGRow applies one PNG filter type to a cross-reference stream row. The
// result starts with the filter byte and is decoded by the reader's predictor
// implementation.
func encodePNGRow(raw, prev []byte, filterType byte) []byte {
	out := make([]byte, len(raw)+1)
	out[0] = filterType

	for i := range raw {
		var left, upperLeft byte
		if i > 0 {
			left = raw[i-1]
			upperLeft = prev[i-1]
		}

		var predictor byte
		switch filterType {
		case 0: // None
		case 1: // Sub
			predictor = left
		case 2: // Up
			predictor = prev[i]
		case 3: // Average
			predictor = byte((int(left) + int(prev[i])) / 2)
		case 4: // Paeth
			predictor = paeth(left, prev[i], upperLeft)
		default:
			predictor = 0
		}

		out[i+1] = raw[i] - predictor
	}

	return out
}

// appendPredictedXrefStream writes a cross-reference stream with /W [1 4 2]
// whose rows are PNG-predicted. filterTypes is cycled over the rows.
func appendPredictedXrefStream(t *testing.T, buf *bytes.Buffer, xrefNum int, entries []xrefEntry, size int, ranges [][2]int, filterTypes []byte) {
	t.Helper()

	var raw bytes.Buffer
	index := make([]string, 0, len(ranges)*2)
	prev := make([]byte, 7)
	row := 0

	for _, r := range ranges {
		index = append(index, fmt.Sprintf("%d %d", r[0], r[1]))
		for _, entry := range entries[r[0] : r[0]+r[1]] {
			data := []byte{
				byte(entry.objType),
				byte(entry.field2 >> 24), byte(entry.field2 >> 16), byte(entry.field2 >> 8), byte(entry.field2),
				byte(entry.field3 >> 8), byte(entry.field3),
			}
			raw.Write(encodePNGRow(data, prev, filterTypes[row%len(filterTypes)]))
			copy(prev, data)
			row++
		}
	}

	compressed := flate(t, raw.Bytes())

	dict := fmt.Sprintf("<< /Type /XRef /Size %d /W [1 4 2] /DecodeParms << /Predictor 12 /Columns 7 >> /Root 1 0 R /Index [%s] /Length %d /Filter /FlateDecode >>",
		size, joinStrings(index), len(compressed))

	fmt.Fprintf(buf, "%d 0 obj\n%s\nstream\n%s\nendstream\nendobj\n", xrefNum, dict, compressed)
}

// buildPredictedXrefPDF writes a PDF whose cross-reference stream uses PNG
// predictors. Every filter type appears at least once.
func buildPredictedXrefPDF(t *testing.T) []byte {
	t.Helper()

	content := "q Q\n"
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")

	entries := newEntries(6)
	for num := 1; num <= 4; num++ {
		writeBody(&buf, entries, num, bodies[num])
	}

	xrefOffset := buf.Len()
	entries[5] = xrefEntry{objType: 1, field2: xrefOffset}

	appendPredictedXrefStream(t, &buf, 5, entries, 6, [][2]int{{0, 6}}, []byte{0, 1, 2, 3, 4, 0})
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes()
}

// TestReadXrefStreamWithPredictors checks the PNG predictor types used by
// cross-reference streams. Encoders commonly write predictor 12 (Up), but the
// decoder has to honour whichever filter byte each row carries.
func TestReadXrefStreamWithPredictors(t *testing.T) {
	pdf := buildPredictedXrefPDF(t)

	reader, err := NewPdfReaderFromStream("predictor.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}

	boxes, err := reader.getAllPageBoxes(1.0)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if got := boxes[1]["/MediaBox"]; got["w"] != 595 || got["h"] != 842 {
		t.Fatalf("unexpected page size: %+v", got)
	}

	content, err := reader.getContent(1)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}
	if content != "q Q\n" {
		t.Fatalf("unexpected content: %q", content)
	}

	if offset, ok := reader.xref[1][0]; !ok || offset <= 0 {
		t.Fatalf("object 1 was not registered: %+v", reader.xref[1])
	}
}

func TestReadXrefStreamRejectsInvalidPredictorData(t *testing.T) {
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>",
	}
	base := buildPDFWithXrefStream(t, bodies, 5, nil)

	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{
			name:    "predictor out of range",
			old:     "/Filter /FlateDecode",
			new:     "/DecodeParms << /Predictor 16 /Columns 7 >> /Filter /FlateDecode",
			wantErr: "Predictor",
		},
		{
			name:    "index without pairs",
			old:     "/Index [0 6]",
			new:     "/Index [0 6 1]",
			wantErr: "pairs",
		},
		{
			name:    "field width too large",
			old:     "/W [1 4 2]",
			new:     "/W [9 4 2]",
			wantErr: "field widths",
		},
		{
			name:    "entry does not fill the row width",
			old:     "/W [1 4 2]",
			new:     "/W [1 5 2]",
			wantErr: "Incomplete",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pdf := bytes.Replace(base, []byte(test.old), []byte(test.new), 1)
			if bytes.Equal(pdf, base) {
				t.Fatalf("%q was not found in the fixture", test.old)
			}

			_, err := NewPdfReaderFromStream("predictor.pdf", bytes.NewReader(pdf))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error %q should mention %q", err, test.wantErr)
			}
		})
	}
}

// TestEncodePNGRowRoundTrip guards the encoder used by the predictor test
// itself, so a decode regression cannot be hidden by a matching bug here.
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

			// Decode with the same rules the reader applies.
			decoded := append([]byte(nil), encoded[1:]...)
			switch filterType {
			case 1:
				for si := 1; si < len(decoded); si++ {
					decoded[si] += decoded[si-1]
				}
			case 2:
				for si := range decoded {
					decoded[si] += prev[si]
				}
			case 3:
				for si := range decoded {
					var left byte
					if si > 0 {
						left = decoded[si-1]
					}
					decoded[si] += byte((int(left) + int(prev[si])) / 2)
				}
			case 4:
				for si := range decoded {
					var left, upperLeft byte
					if si > 0 {
						left = decoded[si-1]
						upperLeft = prev[si-1]
					}
					decoded[si] += paeth(left, prev[si], upperLeft)
				}
			}

			if !bytes.Equal(decoded, raw) {
				t.Fatalf("filter %d row %d: decoded %v, want %v", filterType, i, decoded, raw)
			}
			copy(prev, raw)
		}
	}
}
