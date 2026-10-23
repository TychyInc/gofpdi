package gofpdi

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// PDF filters used by the fixtures.
const (
	filterNone    = ""
	filterFlate   = "/FlateDecode"
	filterASCII85 = "/ASCII85Decode"
	filterLZW     = "/LZWDecode"
)

// xrefEntry is one entry of a cross-reference section: the entry type, the
// second field (byte offset or object stream number) and the third field
// (generation number or index within the object stream).
type xrefEntry struct {
	objType int
	field2  int
	field3  int
}

// previousXref tells xrefStream to link the section to the one written before.
const previousXref = -1

// xrefStreamOptions describes the cross-reference stream a test needs. The zero
// value writes the entries as they are, with /W [1 4 2], which is the layout
// qpdf --stream-data=uncompress produces.
type xrefStreamOptions struct {
	// Index overrides the /Index ranges. By default the stream lists object 0
	// for the first revision, the objects changed since the previous
	// cross-reference section otherwise, plus the stream object itself.
	Index [][2]int
	// SkipIndexDict leaves /Index out of the dictionary. The ranges in Index
	// still describe the encoded entries.
	SkipIndexDict bool
	// Size overrides /Size. The default is the highest known object number + 1.
	Size int
	// Widths overrides /W.
	Widths [3]int
	// Filter is the /Filter of the stream. The empty string stores the entries
	// as they are.
	Filter string
	// Predictor adds /DecodeParms and writes the rows with PNG prediction.
	// RowFilters is cycled over the rows and defaults to Up (2). A predictor of
	// 1 (no prediction) stores the rows as they are.
	Predictor  int
	RowFilters []byte
	// DecodeParms overrides the /DecodeParms entry. The rows are still encoded
	// according to Predictor so that array forms can be written.
	DecodeParms string
	// Prev adds /Prev. Use previousXref to link to the previous section of the
	// file being built.
	Prev int
	// Extra is appended to the stream dictionary.
	Extra string
	// NoFooter leaves out startxref and %%EOF so that the caller can append
	// another section, as hybrid-reference files do.
	NoFooter bool
}

// pdfBuilder assembles a PDF for a test. Objects are written in the order the
// tests call object, so the recorded offsets always match the file, and every
// written object updates the cross-reference state used by classicXref and
// xrefStream.
type pdfBuilder struct {
	buf     bytes.Buffer
	entries map[int]xrefEntry
	dirty   map[int]bool
	// lastXref is the offset of the most recently completed cross-reference
	// section, and sections counts them.
	lastXref int
	sections int
}

// newPDF starts a file with the given PDF version, for example "1.5".
func newPDF(version string) *pdfBuilder {
	b := &pdfBuilder{entries: make(map[int]xrefEntry), dirty: make(map[int]bool)}
	fmt.Fprintf(&b.buf, "%%PDF-%s\n", version)
	return b
}

// classicPDF returns a finished PDF with a classic cross-reference table built
// from object number to body.
func classicPDF(objects map[int]string) []byte {
	return newClassicPDF(objects).bytes()
}

// newClassicPDF returns a builder for a PDF with a classic cross-reference
// table. Tests that append another revision after it keep using the builder.
func newClassicPDF(objects map[int]string) *pdfBuilder {
	b := newPDF("1.5")

	ids := make([]int, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		b.object(id, objects[id])
	}

	b.classicXref("")
	return b
}

func (b *pdfBuilder) offset() int {
	return b.buf.Len()
}

func (b *pdfBuilder) bytes() []byte {
	return append([]byte(nil), b.buf.Bytes()...)
}

// raw appends literal bytes without registering an object. It is used for data
// after the last cross-reference section and for deliberately broken files.
func (b *pdfBuilder) raw(format string, args ...any) *pdfBuilder {
	fmt.Fprintf(&b.buf, format, args...)
	return b
}

// object writes a direct object and records its offset.
func (b *pdfBuilder) object(id int, body string) *pdfBuilder {
	b.entries[id] = xrefEntry{objType: 1, field2: b.offset()}
	b.dirty[id] = true
	fmt.Fprintf(&b.buf, "%d 0 obj\n%s\nendobj\n", id, body)
	return b
}

// stream writes a stream object whose /Length is derived from data. dict holds
// extra dictionary entries and is optional.
func (b *pdfBuilder) stream(id int, dict string, data []byte) *pdfBuilder {
	if dict != "" {
		dict = " " + dict
	}
	return b.object(id, fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(data), dict, data))
}

// free records a free entry without writing an object.
func (b *pdfBuilder) free(id int) *pdfBuilder {
	b.entries[id] = xrefEntry{objType: 0}
	b.dirty[id] = true
	return b
}

// compressed records that id is stored in the object stream objectStream at
// the given index.
func (b *pdfBuilder) compressed(id, objectStream, index int) *pdfBuilder {
	b.entries[id] = xrefEntry{objType: 2, field2: objectStream, field3: index}
	b.dirty[id] = true
	return b
}

// setEntry overrides a cross-reference entry without writing an object.
func (b *pdfBuilder) setEntry(id int, entry xrefEntry) *pdfBuilder {
	b.entries[id] = entry
	b.dirty[id] = true
	return b
}

// size returns the highest known object number plus one.
func (b *pdfBuilder) size() int {
	maxID := 0
	for id := range b.entries {
		if id > maxID {
			maxID = id
		}
	}
	return maxID + 1
}

// classicXref writes a classic cross-reference table for every object, followed
// by the trailer and the file trailer. Compressed objects cannot be listed in a
// classic table and are written as free entries, like hybrid-reference files do.
func (b *pdfBuilder) classicXref(trailerExtra string) int {
	return b.classicXrefRange(0, b.size(), trailerExtra)
}

// classicXrefRange writes a classic table for one object range, which is how
// incremental updates with a classic table describe only the changed objects.
func (b *pdfBuilder) classicXrefRange(first, count int, trailerExtra string) int {
	offset := b.offset()

	fmt.Fprintf(&b.buf, "xref\n%d %d\n", first, count)
	for id := first; id < first+count; id++ {
		entry := b.entries[id]
		if entry.objType != 1 {
			fmt.Fprintf(&b.buf, "%010d %05d f \n", entry.field2, entry.field3)
			continue
		}
		fmt.Fprintf(&b.buf, "%010d %05d n \n", entry.field2, entry.field3)
	}
	fmt.Fprintf(&b.buf, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		b.size(), trailerExtra, offset)

	b.completeSection(offset)
	return offset
}

// xrefStream writes a cross-reference stream object and the file trailer, and
// returns the offset of the stream object.
func (b *pdfBuilder) xrefStream(id int, opts xrefStreamOptions) int {
	widths := opts.Widths
	if widths == ([3]int{}) {
		widths = [3]int{1, 4, 2}
	}

	ranges := opts.Index
	if ranges == nil {
		ranges = b.changedRanges(id)
	}

	// Register the stream object before encoding the entries so that its own
	// entry points at the right offset and /Size covers it.
	offset := b.offset()
	b.entries[id] = xrefEntry{objType: 1, field2: offset}
	b.dirty[id] = true

	size := opts.Size
	if size == 0 {
		size = b.size()
	}

	data, index := b.encodeXrefStream(ranges, widths, opts)

	dict := fmt.Sprintf("<< /Type /XRef /Size %d /W [%d %d %d] /Root 1 0 R",
		size, widths[0], widths[1], widths[2])
	if !opts.SkipIndexDict {
		dict += " /Index [" + index + "]"
	}
	dict += fmt.Sprintf(" /Length %d", len(data))
	if opts.Filter != filterNone {
		dict += " /Filter " + opts.Filter
	}
	if opts.DecodeParms != "" {
		dict += " /DecodeParms " + opts.DecodeParms
	} else if opts.Predictor != 0 {
		dict += fmt.Sprintf(" /DecodeParms << /Predictor %d /Columns %d >>",
			opts.Predictor, widths[0]+widths[1]+widths[2])
	}
	prev := opts.Prev
	if prev == previousXref {
		prev = b.lastXref
	}
	if prev != 0 {
		dict += fmt.Sprintf(" /Prev %d", prev)
	}
	dict += opts.Extra
	dict += " >>"

	fmt.Fprintf(&b.buf, "%d 0 obj\n%s\nstream\n%s\nendstream\nendobj\n", id, dict, data)
	if !opts.NoFooter {
		fmt.Fprintf(&b.buf, "startxref\n%d\n%%%%EOF\n", offset)
		b.completeSection(offset)
	} else {
		b.completeSection(0)
	}

	return offset
}

// completeSection ends a revision: the next xrefStream only describes objects
// written afterwards.
func (b *pdfBuilder) completeSection(offset int) {
	b.sections++
	b.dirty = make(map[int]bool)
	if offset != 0 {
		b.lastXref = offset
	}
}

// changedRanges groups the objects written since the previous cross-reference
// section into (first object number, count) pairs. The first section also lists
// object 0, like producers normally do.
func (b *pdfBuilder) changedRanges(self int) [][2]int {
	ids := make([]int, 0, len(b.dirty)+2)
	seen := make(map[int]bool, len(b.dirty)+2)
	add := func(id int) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for id := range b.dirty {
		add(id)
	}
	if b.sections == 0 {
		add(0)
	}
	add(self)
	sort.Ints(ids)

	ranges := make([][2]int, 0, len(ids))
	for _, id := range ids {
		last := len(ranges) - 1
		if last >= 0 && ranges[last][0]+ranges[last][1] == id {
			ranges[last][1]++
			continue
		}
		ranges = append(ranges, [2]int{id, 1})
	}
	return ranges
}

// encodeXrefStream serialises the entries of ranges and applies the requested
// filter. It also returns the /Index string that describes the ranges.
func (b *pdfBuilder) encodeXrefStream(ranges [][2]int, widths [3]int, opts xrefStreamOptions) ([]byte, string) {
	index := make([]string, 0, len(ranges))
	rowSize := widths[0] + widths[1] + widths[2]

	var rows bytes.Buffer
	row := make([]byte, rowSize)
	prev := make([]byte, rowSize)
	rowNum := 0

	for _, r := range ranges {
		index = append(index, fmt.Sprintf("%d %d", r[0], r[1]))
		for id := r[0]; id < r[0]+r[1]; id++ {
			entry := b.entries[id]

			pos := 0
			if widths[0] != 0 {
				putBigEndian(row[pos:pos+widths[0]], entry.objType, widths[0])
				pos += widths[0]
			}
			putBigEndian(row[pos:pos+widths[1]], entry.field2, widths[1])
			pos += widths[1]
			putBigEndian(row[pos:], entry.field3, widths[2])

			if opts.Predictor <= 1 {
				rows.Write(row)
				rowNum++
				continue
			}

			filterType := byte(2) // Up
			if len(opts.RowFilters) > 0 {
				filterType = opts.RowFilters[rowNum%len(opts.RowFilters)]
			}
			rows.Write(encodePNGRow(row, prev, filterType))
			copy(prev, row)
			rowNum++
		}
	}

	// An unknown filter is written as it is: the reader has to reject it.
	// Brackets in /Filter ("[/FlateDecode]") are accepted so that tests can pair
	// the array form with an array of /DecodeParms.
	var encoded []byte
	switch strings.Trim(opts.Filter, "[]") {
	case filterFlate:
		encoded = flateBytes(rows.Bytes())
	case filterASCII85:
		encoded = ascii85Bytes(rows.Bytes())
	default:
		encoded = rows.Bytes()
	}

	return encoded, strings.Join(index, " ")
}

func putBigEndian(dst []byte, value, width int) {
	for i := width - 1; i >= 0; i-- {
		dst[i] = byte(value)
		value >>= 8
	}
}

// pageSpec describes a page for pagesPDF.
type pageSpec struct {
	// dict holds extra page dictionary entries, for example "/Rotate 90".
	dict string
	// content is the page content stream. An empty value means "q Q\n".
	content string
}

// pagesPDF builds a finished PDF with the given pages. Object 1 is the catalog
// and object 2 the page tree; pagesDict is appended to the page tree dictionary
// so tests can put inherited entries there.
func pagesPDF(pagesDict string, pages []pageSpec) []byte {
	kids := make([]string, 0, len(pages))
	bodies := make(map[int]string, 2+2*len(pages))

	next := 3
	for _, page := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", next))
		content := page.content
		if content == "" {
			content = "q Q\n"
		}
		bodies[next] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R %s /Contents %d 0 R >>",
			page.dict, next+1)
		bodies[next+1] = streamObject([]byte(content), "")
		next += 2
	}

	bodies[1] = "<< /Type /Catalog /Pages 2 0 R >>"
	bodies[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d %s >>",
		strings.Join(kids, " "), len(pages), pagesDict)

	b := newPDF("1.5")
	for id := 1; id < next; id++ {
		if body, ok := bodies[id]; ok {
			b.object(id, body)
		}
	}
	b.classicXref("")
	return b.bytes()
}

// singlePagePDF returns a one-page PDF whose content stream is body.
func singlePagePDF(body string) []byte {
	b := newPDF("1.5")
	b.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	b.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>")
	b.object(3, "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>")
	b.stream(4, "", []byte(body))
	b.classicXref("")
	return b.bytes()
}

// writeTempPDF writes pdf to a temporary file and returns its path.
func writeTempPDF(t *testing.T, pdf []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, pdf, 0o600); err != nil {
		t.Fatalf("failed to write PDF: %v", err)
	}
	return path
}

// streamObject renders an indirect stream object with the given bytes and an
// optional /Filter entry.
func streamObject(data []byte, filter string) string {
	filterPart := ""
	if filter != "" {
		filterPart = " /Filter " + filter
	}
	return fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(data), filterPart, data)
}

func newTestReader(t *testing.T, pdf []byte) *PdfReader {
	t.Helper()

	reader, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("failed to parse PDF: %v", err)
	}
	return reader
}

func newTestWriter(t *testing.T) *PdfWriter {
	t.Helper()

	writer, err := NewPdfWriter("")
	if err != nil {
		t.Fatalf("failed to create writer: %v", err)
	}
	return writer
}

// readPageContent returns the decoded content stream of a page.
func readPageContent(t *testing.T, pdf []byte, page int) string {
	t.Helper()

	content, err := newTestReader(t, pdf).getContent(page)
	if err != nil {
		t.Fatalf("failed to read page content: %v", err)
	}
	return content
}

func flateBytes(data []byte) []byte {
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func ascii85Bytes(data []byte) []byte {
	var buf bytes.Buffer
	encoder := ascii85.NewEncoder(&buf)
	if _, err := encoder.Write(data); err != nil {
		panic(err)
	}
	if err := encoder.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// runLengthEncode encodes data with RunLengthDecode, using literal runs only.
func runLengthEncode(data []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(data); {
		length := min(len(data)-i, 128)
		out.WriteByte(byte(length - 1))
		out.Write(data[i : i+length])
		i += length
	}
	out.WriteByte(128)
	return out.Bytes()
}

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
		}

		out[i+1] = raw[i] - predictor
	}

	return out
}
