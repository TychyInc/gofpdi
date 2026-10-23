package gofpdi

import (
	"bytes"
	"fmt"
	"testing"
)

// parserFixtures returns one file per layout the reader has to handle: a classic
// table, a cross-reference stream with and without a filter, a hybrid-reference
// file, an incremental update and an object stream. A real upload can be any of
// them and can arrive cut short, which is what the two tests below exercise.
func parserFixtures() map[string][]byte {
	fixtures := map[string][]byte{
		"classic table": pagesPDF("/MediaBox [0 0 595 842] /Resources << >>",
			[]pageSpec{{}, {}}),
		"xref stream with predictor": streamXrefPDF("0 0 595 842", xrefStreamOptions{
			Filter:    filterFlate,
			Predictor: 12,
		}).bytes(),
		"xref stream without filter": streamXrefPDF("0 0 595 842", xrefStreamOptions{}).bytes(),
		"object stream": objectStreamPDF(
			"/Type /ObjStm /N 2 /First 8", "9 0 4 0 ", 1),
	}

	hybrid := newPDF("1.5")
	hybrid.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	hybrid.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << >> >>")
	hybrid.stream(4, "", []byte("q Q\n"))
	hybrid.object(5, "<< >>")
	hybrid.stream(6, "/Type /ObjStm /N 1 /First 4", []byte("2 0 << /Type /Pages /Kids [3 0 R] /Count 1 >>"))
	hybrid.compressed(2, 6, 0)
	xrefStreamOffset := hybrid.xrefStream(7, xrefStreamOptions{
		Index:    [][2]int{{1, 7}},
		Filter:   filterFlate,
		NoFooter: true,
	})
	hybrid.classicXrefRange(0, 8, fmt.Sprintf(" /XRefStm %d", xrefStreamOffset))
	fixtures["hybrid reference"] = hybrid.bytes()

	update := newPDF("1.5")
	update.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	update.object(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	update.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> >>")
	update.classicXref("")
	update.object(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 419 595] /Resources << >> >>")
	update.classicXrefRange(3, 1, fmt.Sprintf(" /Prev %d", update.lastXref))
	fixtures["incremental update"] = update.bytes()

	return fixtures
}

// readAllPages walks the pages the reader found. A prefix of a valid file can
// parse and still leave a page whose boxes or content cannot be resolved, so the
// accessors are part of the check. The page count is bounded so that a damaged
// file cannot make the test itself expensive.
func readAllPages(reader *PdfReader) {
	pages, err := reader.getNumPages()
	if err != nil {
		return
	}
	for page := 1; page <= pages && page <= 8; page++ {
		reader.getPageBoxes(page, 1)
		reader.getPageRotation(page)
		reader.getPageResources(page)
		reader.getContent(page)
	}
}

// TestTruncatedInputDoesNotPanic cuts every fixture at every byte offset. Each
// prefix must either fail to parse or stay readable; it must never panic, since
// the caller has no way to recover from that.
func TestTruncatedInputDoesNotPanic(t *testing.T) {
	for name, pdf := range parserFixtures() {
		t.Run(name, func(t *testing.T) {
			for length := 0; length < len(pdf); length++ {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("panic at %d of %d bytes: %v", length, len(pdf), r)
						}
					}()

					reader, err := NewPdfReaderFromStream("truncated.pdf", bytes.NewReader(pdf[:length]))
					if err != nil {
						return
					}
					readAllPages(reader)
				}()
			}
		})
	}
}

// FuzzNewPdfReaderFromStream keeps the fixtures above as a seed corpus. CI runs
// the seeds with `go test`; `go test -fuzz=FuzzNewPdfReaderFromStream` explores
// the parser further. Inputs are capped so that a discovered crash is reported
// before the worker can allocate its way out of memory.
func FuzzNewPdfReaderFromStream(f *testing.F) {
	for _, pdf := range parserFixtures() {
		f.Add(pdf)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}

		reader, err := NewPdfReaderFromStream("fuzz.pdf", bytes.NewReader(data))
		if err != nil {
			return
		}
		readAllPages(reader)
	})
}
