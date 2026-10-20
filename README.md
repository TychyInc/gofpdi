# gofpdi
[![CI](https://github.com/TychyInc/gofpdi/actions/workflows/ci.yml/badge.svg)](https://github.com/TychyInc/gofpdi/actions/workflows/ci.yml)
[![MIT
licensed](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)
[![Report](https://goreportcard.com/badge/github.com/phpdave11/gofpdi)](https://goreportcard.com/report/github.com/phpdave11/gofpdi)
[![GoDoc](https://img.shields.io/badge/godoc-gofpdi-blue.svg)](https://godoc.org/github.com/phpdave11/gofpdi)

## Go Free PDF Document Importer

gofpdi allows you to import an existing PDF into a new PDF.  The following PDF generation libraries are supported:

- [gopdf](https://github.com/signintech/gopdf)

- [gofpdf](https://github.com/phpdave11/gofpdf)

## About this fork

This is the [Tychy Inc.](https://tychy.jp) fork of
[phpdave11/gofpdi](https://github.com/phpdave11/gofpdi), used in production by our e-signature
service, which has to import arbitrary PDFs uploaded by users. Upstream `v1.0.15` is the last
upstream release; the changes below are what we needed on top of it.

Changes against upstream:

- Cross-reference tables: hybrid-reference files (`/XRefStm`), cross-reference streams without a
  `/Type` field, cross-reference streams without `/Filter`, and compressed objects whose entry
  comes from a newer revision than the classic table.
- Stream decoding: `/ASCIIHexDecode`, `/ASCII85Decode` and `/RunLengthDecode` next to
  `/FlateDecode`, filter chains, streams with no `/Filter`, and zlib data with a truncated trailer;
  a `/Filter` that is neither a name nor an array, an array entry that is not a name, and an
  ASCII85 group that overflows 32 bits are reported as errors, while a null `/Filter` is treated
  as an absent entry.
- `/DecodeParms`: `/Columns` greater than 4, and all PNG row filter types when a cross-reference
  stream uses a predictor.
- Object streams: the token stack is isolated per object, so one object cannot corrupt the next.
- Page tree: the pages are counted by walking the tree, so a `/Count` that disagrees with it is not
  fatal; a missing `/Kids` or `/Type`, a node that is not a dictionary, and an indirect `/Kids` are
  reported or resolved instead of panicking, and a cycle or a tree nested more than 64 levels deep
  is reported instead of overflowing the stack.
- Damaged input: a `startxref` candidate is used only if it really points at a cross-reference
  section, field widths are limited to 8 bytes, and the entry count is taken from the decoded data.
- Import: each page is imported with its own page boxes, only `/MediaBox` and `/CropBox` are
  inherited from the page tree, and `Importer.GetTemplateSize` reports the size of an imported
  template.
- Tests: regression tests for every fix above, a CI job running `go test -race ./...`, and
  [prek](https://github.com/j178/prek) Git hooks.

### Using this fork

The module path stays `github.com/phpdave11/gofpdi` so that a `replace` directive works. Tags
`v1.0.16` and later are cut in this repository (the newest one is `v1.0.25`):

```
require github.com/phpdave11/gofpdi v1.0.15

replace github.com/phpdave11/gofpdi => github.com/TychyInc/gofpdi v1.0.25
```

Because of that module path, `go get github.com/TychyInc/gofpdi` on its own fails with "module
declares its path as: github.com/phpdave11/gofpdi"; import the package through the replaced path
instead.

## Acknowledgments
This package’s code is derived from the [fpdi](https://github.com/Setasign/FPDI/tree/1.6.x-legacy) library created by [Jan Slabon](https://github.com/JanSlabon).
[mrtsbt](https://github.com/mrtsbt) added support for reading a PDF from an `io.ReadSeeker` stream and also added support for using gofpdi concurrently.  [Asher Tuggle](https://github.com/awesomeunleashed) added support for reading PDFs that have split xref tables.

## Examples

### gopdf example

```go
package main

import (
        "github.com/signintech/gopdf"
        "io"
        "net/http"
        "os"
)

func main() {
        var err error

        // Download a Font
        fontUrl := "https://github.com/google/fonts/raw/master/ofl/daysone/DaysOne-Regular.ttf"
        if err = DownloadFile("example-font.ttf", fontUrl); err != nil {
                panic(err)
        }

        // Download a PDF
        fileUrl := "https://tcpdf.org/files/examples/example_012.pdf"
        if err = DownloadFile("example-pdf.pdf", fileUrl); err != nil {
                panic(err)
        }

        pdf := gopdf.GoPdf{}
        pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: 595.28, H: 841.89}}) //595.28, 841.89 = A4

        pdf.AddPage()

        err = pdf.AddTTFFont("daysone", "example-font.ttf")
        if err != nil {
                panic(err)
        }

        err = pdf.SetFont("daysone", "", 20)
        if err != nil {
                panic(err)
        }

        // Color the page
        pdf.SetLineWidth(0.1)
        pdf.SetFillColor(124, 252, 0) //setup fill color
        pdf.RectFromUpperLeftWithStyle(50, 100, 400, 600, "FD")
        pdf.SetFillColor(0, 0, 0)

        pdf.SetX(50)
        pdf.SetY(50)
        pdf.Cell(nil, "Import existing PDF into GoPDF Document")

        // Import page 1
        tpl1 := pdf.ImportPage("example-pdf.pdf", 1, "/MediaBox")

        // Draw pdf onto page
        pdf.UseImportedTemplate(tpl1, 50, 100, 400, 0)

        pdf.WritePdf("example.pdf")

}

// DownloadFile will download a url to a local file. It's efficient because it will
// write as it downloads and not load the whole file into memory.
func DownloadFile(filepath string, url string) error {
        // Get the data
        resp, err := http.Get(url)
        if err != nil {
                return err
        }
        defer resp.Body.Close()

        // Create the file
        out, err := os.Create(filepath)
        if err != nil {
                return err
        }
        defer out.Close()

        // Write the body to file
        _, err = io.Copy(out, resp.Body)
        return err
}
```

Generated PDF: [example.pdf](https://github.com/signintech/gopdf/files/3144466/example.pdf)

Screenshot of PDF:

![example](https://user-images.githubusercontent.com/9421180/57180557-4c1dbd80-6e4f-11e9-8f47-9d40217805be.jpg)

### gofpdf example #1 - import PDF from file

```go
package main

import (
	"github.com/phpdave11/gofpdf"
	"github.com/phpdave11/gofpdf/contrib/gofpdi"
	"io"
	"net/http"
	"os"
)

func main() {
	var err error

	pdf := gofpdf.New("P", "mm", "A4", "")

	// Download a PDF
	fileUrl := "https://tcpdf.org/files/examples/example_026.pdf"
	if err = DownloadFile("example-pdf.pdf", fileUrl); err != nil {
		panic(err)
	}

	// Import example-pdf.pdf with gofpdi free pdf document importer
	tpl1 := gofpdi.ImportPage(pdf, "example-pdf.pdf", 1, "/MediaBox")

	pdf.AddPage()

	pdf.SetFillColor(200, 700, 220)
	pdf.Rect(20, 50, 150, 215, "F")

	// Draw imported template onto page
	gofpdi.UseImportedTemplate(pdf, tpl1, 20, 50, 150, 0)

	pdf.SetFont("Helvetica", "", 20)
	pdf.Cell(0, 0, "Import existing PDF into gofpdf document with gofpdi")

	err = pdf.OutputFileAndClose("example.pdf")
	if err != nil {
		panic(err)
	}
}

// DownloadFile will download a url to a local file. It's efficient because it will
// write as it downloads and not load the whole file into memory.
func DownloadFile(filepath string, url string) error {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}
```

Generated PDF:  [example.pdf](https://github.com/phpdave11/gofpdf/files/3178770/example.pdf)

Screenshot of PDF:
![example](https://user-images.githubusercontent.com/9421180/57713804-ca8d1300-7638-11e9-9f8e-e3f803374803.jpg)



### gofpdf example #2 - import PDF from stream

```go
package main

import (
    "bytes"
    "github.com/phpdave11/gofpdf"
    "github.com/phpdave11/gofpdf/contrib/gofpdi"
    "io"
    "io/ioutil"
    "net/http"
)

func main() {
    var err error

    pdf := gofpdf.New("P", "mm", "A4", "")

    // Download a PDF into memory  
    res, err := http.Get("https://tcpdf.org/files/examples/example_038.pdf")
    if err != nil {
        panic(err)
    }
    pdfBytes, err := ioutil.ReadAll(res.Body)
    res.Body.Close()
    if err != nil {
        panic(err)
    }

    // convert []byte to io.ReadSeeker  
    rs := io.ReadSeeker(bytes.NewReader(pdfBytes))

    // Import in-memory PDF stream with gofpdi free pdf document importer  
    tpl1 := gofpdi.ImportPageFromStream(pdf, &rs, 1, "/TrimBox")

    pdf.AddPage()

    pdf.SetFillColor(200, 700, 220)
    pdf.Rect(20, 50, 150, 215, "F")

    // Draw imported template onto page  
    gofpdi.UseImportedTemplate(pdf, tpl1, 20, 50, 150, 0)

    pdf.SetFont("Helvetica", "", 20)
    pdf.Cell(0, 0, "Import PDF stream into gofpdf document with gofpdi")

    err = pdf.OutputFileAndClose("example.pdf")
    if err != nil {
        panic(err)
    }
}
```

Generated PDF:

[example.pdf](https://github.com/phpdave11/gofpdi/files/3483219/example.pdf)

Screenshot of PDF:

![example.jpg](https://user-images.githubusercontent.com/9421180/62728726-18b87500-b9e2-11e9-885c-7c68b7ac6222.jpg)

## Development

```bash
go test -race ./...
```

Formatting, `go vet` and the tests also run as Git hooks through
[prek](https://github.com/j178/prek):

```bash
prek install            # once per clone
prek run --all-files    # run the same checks as CI
```

The Go version in `go.mod` (currently 1.27) is required; the CI workflow follows the stable
toolchain.

## License

MIT, see [LICENSE](./LICENSE). The original copyright notices are kept: the code derives from
[FPDI](https://github.com/Setasign/FPDI/tree/1.6.x-legacy) by Jan Slabon (Setasign), and the Go
port is by David Barnes. Modifications in this fork are copyright Tychy Inc.
