package gofpdi

const (
	PDF_TYPE_NULL = iota
	PDF_TYPE_NUMERIC
	PDF_TYPE_TOKEN
	PDF_TYPE_HEX
	PDF_TYPE_STRING
	PDF_TYPE_DICTIONARY
	PDF_TYPE_ARRAY
	PDF_TYPE_OBJDEC
	PDF_TYPE_OBJREF
	PDF_TYPE_OBJECT
	PDF_TYPE_STREAM
	PDF_TYPE_BOOLEAN
	PDF_TYPE_REAL
)

// maxPageTreeDepth limits how deep a /Pages node may be nested. The tree is
// walked recursively, so a cycle (/Kids that points back at an ancestor) or a
// pathologically deep tree would recurse until the goroutine stack overflows,
// which is a fatal error that recover() cannot catch. 64 levels is far more
// than any real document uses.
const maxPageTreeDepth = 64
