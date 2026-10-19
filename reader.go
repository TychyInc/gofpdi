package gofpdi

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/pkg/errors"
)

type PdfReader struct {
	availableBoxes []string
	stack          []string
	trailer        *PdfValue
	catalog        *PdfValue
	pages          []*PdfValue
	xrefPos        int
	xref           map[int]map[int]int
	xrefStream     map[int][2]int
	// overrideFreeEntries allows a cross-reference stream that belongs to the
	// same revision as the classic table (/XRefStm in a hybrid-reference file)
	// to replace the free entries the table records for compressed objects.
	overrideFreeEntries bool
	f                   io.ReadSeeker
	nBytes              int64
	sourceFile          string
	curPage             int
	alreadyRead         bool
	pageCount           int
}

func NewPdfReaderFromStream(sourceFile string, rs io.ReadSeeker) (*PdfReader, error) {
	length, err := rs.Seek(0, 2)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to determine stream length")
	}
	parser := &PdfReader{f: rs, sourceFile: sourceFile, nBytes: length}
	if err := parser.init(); err != nil {
		return nil, errors.Wrap(err, "Failed to initialize parser")
	}
	if err := parser.read(); err != nil {
		return nil, errors.Wrap(err, "Failed to read pdf from stream")
	}
	return parser, nil
}

func NewPdfReader(filename string) (*PdfReader, error) {
	var err error
	f, err := os.Open(filename)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to open file")
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to obtain file information")
	}

	parser := &PdfReader{f: f, sourceFile: filename, nBytes: info.Size()}
	if err = parser.init(); err != nil {
		return nil, errors.Wrap(err, "Failed to initialize parser")
	}
	if err = parser.read(); err != nil {
		return nil, errors.Wrap(err, "Failed to read pdf")
	}

	return parser, nil
}

func (this *PdfReader) init() error {
	this.availableBoxes = []string{"/MediaBox", "/CropBox", "/BleedBox", "/TrimBox", "/ArtBox"}
	this.xref = make(map[int]map[int]int, 0)
	this.xrefStream = make(map[int][2]int, 0)
	err := this.read()
	if err != nil {
		return errors.Wrap(err, "Failed to read pdf")
	}
	return nil
}

type PdfValue struct {
	Type       int
	String     string
	Token      string
	Int        int
	Real       float64
	Bool       bool
	Dictionary map[string]*PdfValue
	Array      []*PdfValue
	Id         int
	NewId      int
	Gen        int
	Value      *PdfValue
	Stream     *PdfValue
	Bytes      []byte
}

// Jump over comments
func (this *PdfReader) skipComments(r *bufio.Reader) error {
	var err error
	var b byte

	for {
		b, err = r.ReadByte()
		if err != nil {
			if err == io.EOF {
				// A comment may end at EOF without a line ending; PDFs
				// commonly end with "%%EOF" and no trailing newline.
				return nil
			}
			return errors.Wrap(err, "Failed to ReadByte while skipping comments")
		}

		if b == '\n' || b == '\r' {
			if b == '\r' {
				// Peek and see if next char is \n
				b2, err := r.ReadByte()
				if err != nil {
					if err == io.EOF {
						break
					}
					return errors.Wrap(err, "Failed to read byte")
				}
				if b2 != '\n' {
					r.UnreadByte()
				}
			}
			break
		}
	}

	return nil
}

// Advance reader so that whitespace is ignored
func (this *PdfReader) skipWhitespace(r *bufio.Reader) error {
	var err error
	var b byte

	for {
		b, err = r.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return errors.Wrap(err, "Failed to read byte")
		}

		if b == 0 || b == '\t' || b == '\n' || b == '\f' || b == '\r' || b == ' ' {
			continue
		} else {
			r.UnreadByte()
			break
		}
	}

	return nil
}

// skipStreamWhitespace consumes the end-of-line marker that follows the
// "stream" keyword. Unlike skipWhitespace it never skips further, because the
// first byte of the stream data may itself be whitespace.
func (this *PdfReader) skipStreamWhitespace(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return errors.Wrap(err, "Failed to read byte")
	}
	if b == '\r' {
		if b2, err := r.ReadByte(); err == nil && b2 != '\n' {
			r.UnreadByte()
		}
	} else if b != '\n' {
		// No EOL where one was expected; keep the byte for the stream data.
		r.UnreadByte()
	}
	return nil
}

// Read a token
func (this *PdfReader) readToken(r *bufio.Reader) (string, error) {
	var err error

	// If there is a token available on the stack, pop it out and return it.
	if len(this.stack) > 0 {
		var popped string
		popped, this.stack = this.stack[len(this.stack)-1], this.stack[:len(this.stack)-1]
		return popped, nil
	}

	err = this.skipWhitespace(r)
	if err != nil {
		return "", errors.Wrap(err, "Failed to skip whitespace")
	}

	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return "", nil
		}
		return "", errors.Wrap(err, "Failed to read byte")
	}

	switch b {
	case '[', ']', '(', ')':
		// This is either an array or literal string delimeter, return it.
		return string(b), nil

	case '<', '>':
		// This could either be a hex string or a dictionary delimiter.
		// Determine the appropriate case and return the token.
		nb, err := r.ReadByte()
		if err != nil {
			return "", errors.Wrap(err, "Failed to read byte")
		}
		if nb == b {
			return string(b) + string(nb), nil
		} else {
			r.UnreadByte()
			return string(b), nil
		}

	case '%':
		err = this.skipComments(r)
		if err != nil {
			return "", errors.Wrap(err, "Failed to skip comments")
		}
		return this.readToken(r)

	default:
		// FIXME this may not be performant to create new strings for each byte
		// Is it probably better to create a buffer and then convert to a string at the end.
		str := string(b)

	loop:
		for {
			b, err := r.ReadByte()
			if err != nil {
				if err == io.EOF {
					// A token at EOF is complete: the PDF format does not
					// require a delimiter after the last token in a file.
					break loop
				}
				return "", errors.Wrap(err, "Failed to read byte")
			}
			switch b {
			case 0, '\t', '\n', '\f', '\r', ' ', '%', '[', ']', '<', '>', '(', ')', '/':
				r.UnreadByte()
				break loop
			default:
				str += string(b)
			}
		}
		return str, nil
	}
}

// Read a value based on a token
func (this *PdfReader) readValue(r *bufio.Reader, t string) (*PdfValue, error) {
	var err error
	var b byte

	result := &PdfValue{}
	result.Type = -1
	result.Token = t
	result.Dictionary = make(map[string]*PdfValue, 0)
	result.Array = make([]*PdfValue, 0)

	switch t {
	case "<":
		// This is a hex string

		// Read bytes until '>' is found
		var s string
		for {
			b, err = r.ReadByte()
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read byte")
			}
			if b != '>' {
				s += string(b)
			} else {
				break
			}
		}

		result.Type = PDF_TYPE_HEX
		result.String = s

	case "<<":
		// This is a dictionary

		// Recurse into this function until we reach the end of the dictionary.
		for {
			key, err := this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}
			if key == "" {
				return nil, errors.New("Token is empty")
			}

			if key == ">>" {
				break
			}

			// read next token
			newKey, err := this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}

			value, err := this.readValue(r, newKey)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read value for token: "+newKey)
			}

			if value.Type == -1 {
				return result, nil
			}

			// Catch missing value
			if value.Type == PDF_TYPE_TOKEN && value.String == ">>" {
				result.Type = PDF_TYPE_NULL
				result.Dictionary[key] = value
				break
			}

			// Set value in dictionary
			result.Dictionary[key] = value
		}

		result.Type = PDF_TYPE_DICTIONARY
		return result, nil

	case "[":
		// This is an array

		tmpResult := make([]*PdfValue, 0)

		// Recurse into this function until we reach the end of the array
		for {
			key, err := this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}
			if key == "" {
				return nil, errors.New("Token is empty")
			}

			if key == "]" {
				break
			}

			value, err := this.readValue(r, key)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read value for token: "+key)
			}

			if value.Type == -1 {
				return result, nil
			}

			tmpResult = append(tmpResult, value)
		}

		result.Type = PDF_TYPE_ARRAY
		result.Array = tmpResult

	case "(":
		// This is a string

		openBrackets := 1

		// Create new buffer
		var buf bytes.Buffer

		// Read bytes until brackets are balanced
		for openBrackets > 0 {
			b, err := r.ReadByte()

			if err != nil {
				return nil, errors.Wrap(err, "Failed to read byte")
			}

			switch b {
			case '(':
				openBrackets++

			case ')':
				openBrackets--

			case '\\':
				nb, err := r.ReadByte()
				if err != nil {
					return nil, errors.Wrap(err, "Failed to read byte")
				}

				buf.WriteByte(b)
				buf.WriteByte(nb)

				continue
			}

			if openBrackets > 0 {
				buf.WriteByte(b)
			}
		}

		result.Type = PDF_TYPE_STRING
		result.String = buf.String()

	case "stream":
		return nil, errors.New("Stream not implemented")

	default:
		result.Type = PDF_TYPE_TOKEN
		result.Token = t

		if is_numeric(t) {
			// A numeric token.  Make sure that it is not part of something else
			t2, err := this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}
			if t2 != "" {
				if is_numeric(t2) {
					// Two numeric tokens in a row.
					// In this case, we're probably in front of either an object reference
					// or an object specification.
					// Determine the case and return the data.
					t3, err := this.readToken(r)
					if err != nil {
						return nil, errors.Wrap(err, "Failed to read token")
					}

					if t3 != "" {
						switch t3 {
						case "obj":
							result.Type = PDF_TYPE_OBJDEC
							result.Id, _ = strconv.Atoi(t)
							result.Gen, _ = strconv.Atoi(t2)
							return result, nil

						case "R":
							result.Type = PDF_TYPE_OBJREF
							result.Id, _ = strconv.Atoi(t)
							result.Gen, _ = strconv.Atoi(t2)
							return result, nil
						}

						// If we get to this point, that numeric value up there was just a numeric value.
						// Push the extra tokens back into the stack and return the value.
						this.stack = append(this.stack, t3)
					}
				}

				this.stack = append(this.stack, t2)
			}

			if n, err := strconv.Atoi(t); err == nil {
				result.Type = PDF_TYPE_NUMERIC
				result.Int = n
				result.Real = float64(n) // Also assign Real value here to fix page box parsing bugs
			} else {
				result.Type = PDF_TYPE_REAL
				result.Real, _ = strconv.ParseFloat(t, 64)
			}
		} else if t == "true" || t == "false" {
			result.Type = PDF_TYPE_BOOLEAN
			result.Bool = t == "true"
		} else if t == "null" {
			result.Type = PDF_TYPE_NULL
		} else {
			result.Type = PDF_TYPE_TOKEN
			result.Token = t
		}
	}

	return result, nil
}

// Resolve a compressed object (PDF 1.5)
func (this *PdfReader) resolveCompressedObject(objSpec *PdfValue) (*PdfValue, error) {
	var err error

	// Make sure object reference exists in xrefStream
	if _, ok := this.xrefStream[objSpec.Id]; !ok {
		return nil, errors.New(fmt.Sprintf("Could not find object ID %d in xref stream or xref table.", objSpec.Id))
	}

	// Get object id and index
	objectId := this.xrefStream[objSpec.Id][0]
	objectIndex := this.xrefStream[objSpec.Id][1]

	// An object stream is never itself stored in an object stream and always
	// has generation 0. Requiring a regular xref entry also keeps an entry that
	// points at its own object stream from recursing until the stack overflows.
	if _, ok := this.xref[objectId][0]; !ok {
		return nil, errors.Errorf("Object stream %d of object %d is not an uncompressed object", objectId, objSpec.Id)
	}

	// Read compressed object
	compressedObjSpec := &PdfValue{Type: PDF_TYPE_OBJREF, Id: objectId, Gen: 0}

	// Resolve compressed object
	compressedObj, err := this.resolveObject(compressedObjSpec)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to resolve compressed object")
	}

	// Verify object type is /ObjStm
	if _, ok := compressedObj.Value.Dictionary["/Type"]; ok {
		if compressedObj.Value.Dictionary["/Type"].Token != "/ObjStm" {
			return nil, errors.New("Expected compressed object type to be /ObjStm")
		}
	} else {
		return nil, errors.New("Could not determine compressed object type.")
	}

	// Get number of sub-objects in compressed object
	nValue, ok := compressedObj.Value.Dictionary["/N"]
	if !ok || nValue.Type != PDF_TYPE_NUMERIC {
		return nil, errors.New("Object stream /N is not an integer")
	}
	n := nValue.Int
	if n <= 0 {
		return nil, errors.New("No sub objects in compressed object")
	}
	if objectIndex < 0 || objectIndex >= n {
		return nil, errors.Errorf("Object %d has index %d in an object stream of %d objects", objSpec.Id, objectIndex, n)
	}

	if compressedObj.Stream == nil {
		return nil, errors.New("Object stream has no stream data")
	}

	// Get offset of first object
	firstValue, ok := compressedObj.Value.Dictionary["/First"]
	if !ok || firstValue.Type != PDF_TYPE_NUMERIC || firstValue.Int < 0 {
		return nil, errors.New("Object stream /First is not a non-negative integer")
	}
	first := firstValue.Int

	// Check for filter
	filters, err := this.streamFilters(compressedObj)
	if err != nil {
		return nil, err
	}

	for _, filter := range filters {
		decoded, err := decodeStreamFilter(filter.Token, compressedObj.Stream.Bytes)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to decode object stream")
		}
		compressedObj.Stream.Bytes = decoded
	}

	// The sub-objects are parsed from an in-memory buffer, but readToken and
	// readValue share the reader's token stack with the file stream the caller
	// is parsing (for example the /Length of an outer stream). Keep the stack
	// isolated for the header as well as for the object itself.
	savedStack := this.stack
	this.stack = nil
	defer func() { this.stack = savedStack }()

	// Get io.Reader for bytes
	r := bufio.NewReader(bytes.NewBuffer(compressedObj.Stream.Bytes))

	subObjId := 0
	subObjPos := 0

	// Read sub-object indeces and their positions within the (un)compressed
	// object. Only the header up to the requested entry is needed.
	for i := 0; i <= objectIndex; i++ {
		var token string
		var _objidx int
		var _objpos int

		// Read first token (object index)
		token, err = this.readToken(r)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read token")
		}

		// Convert line (string) into int
		_objidx, err = strconv.Atoi(token)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to convert token into integer: "+token)
		}

		// Read first token (object index)
		token, err = this.readToken(r)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read token")
		}

		// Convert line (string) into int
		_objpos, err = strconv.Atoi(token)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to convert token into integer: "+token)
		}

		if i == objectIndex {
			subObjId = _objidx
			subObjPos = _objpos
		}
	}

	// The xref entry and the object stream header must agree; otherwise a
	// different object would be returned in place of the requested one.
	if subObjId != objSpec.Id {
		return nil, errors.Errorf("Object stream %d holds object %d at index %d, expected %d", objectId, subObjId, objectIndex, objSpec.Id)
	}

	// Determine where to seek to (sub-object position + /First)
	seekTo := int64(subObjPos) + int64(first)
	if subObjPos < 0 || seekTo > int64(len(compressedObj.Stream.Bytes)) {
		return nil, errors.Errorf("Object %d lies outside its object stream", objSpec.Id)
	}

	// Now create an io.ReadSeeker and fast forward to the object
	rs := io.ReadSeeker(bytes.NewReader(compressedObj.Stream.Bytes))
	if _, err := rs.Seek(seekTo, io.SeekStart); err != nil {
		return nil, errors.Wrap(err, "Failed to seek in object stream")
	}

	// Create a new io.Reader
	r = bufio.NewReader(rs)

	// Read token
	token, err := this.readToken(r)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read token")
	}

	// Read object
	obj, err := this.readValue(r, token)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read value for token: "+token)
	}

	result := &PdfValue{}
	result.Id = subObjId
	result.Gen = 0
	result.Type = PDF_TYPE_OBJECT
	result.Value = obj

	return result, nil
}

func (this *PdfReader) resolveObject(objSpec *PdfValue) (*PdfValue, error) {
	var err error
	var old_pos int64

	// Create new bufio.Reader
	r := bufio.NewReader(this.f)

	if objSpec.Type == PDF_TYPE_OBJREF {
		// This is a reference, resolve it.
		generations, ok := this.xref[objSpec.Id]
		if !ok {
			// This may be a compressed object
			return this.resolveCompressedObject(objSpec)
		}

		offset, ok := generations[objSpec.Gen]
		if !ok {
			// The generation differs from the one in the xref tables, so the
			// object can only be stored in an object stream.
			return this.resolveCompressedObject(objSpec)
		}

		// Save current file position
		// This is needed if you want to resolve reference while you're reading another object.
		// (e.g.: if you need to determine the length of a stream)
		old_pos, err = this.f.Seek(0, os.SEEK_CUR)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to get current position of file")
		}

		// Reposition the file pointer and load the object header
		_, err = this.f.Seek(int64(offset), 0)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to set position of file")
		}

		token, err := this.readToken(r)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read token")
		}

		obj, err := this.readValue(r, token)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read value for token: "+token)
		}

		if obj.Type != PDF_TYPE_OBJDEC {
			return nil, errors.New(fmt.Sprintf("Expected type to be PDF_TYPE_OBJDEC, got: %d", obj.Type))
		}

		if obj.Id != objSpec.Id {
			return nil, errors.New(fmt.Sprintf("Object ID (%d) does not match ObjSpec ID (%d)", obj.Id, objSpec.Id))
		}

		if obj.Gen != objSpec.Gen {
			return nil, errors.New("Object Gen does not match ObjSpec Gen")
		}

		// Read next token
		token, err = this.readToken(r)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read token")
		}

		// Read actual object value
		value, err := this.readValue(r, token)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read value for token: "+token)
		}

		// Read next token
		token, err = this.readToken(r)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to read token")
		}

		result := &PdfValue{}
		result.Id = obj.Id
		result.Gen = obj.Gen
		result.Type = PDF_TYPE_OBJECT
		result.Value = value

		if token == "stream" {
			result.Type = PDF_TYPE_STREAM

			err = this.skipStreamWhitespace(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to skip whitespace")
			}

			// Get number of bytes of stream
			length, err := this.streamLength(value)
			if err != nil {
				return nil, err
			}

			// Read length bytes
			bytes := make([]byte, length)

			// Cannot use reader.Read() because that may not read all the bytes
			_, err = io.ReadFull(r, bytes)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read bytes from buffer")
			}

			token, err = this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}
			if token != "endstream" {
				return nil, errors.New("Expected next token to be: endstream, got: " + token)
			}

			token, err = this.readToken(r)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to read token")
			}

			streamObj := &PdfValue{}
			streamObj.Type = PDF_TYPE_STREAM
			streamObj.Bytes = bytes

			result.Stream = streamObj
		}

		if token != "endobj" {
			return nil, errors.New("Expected next token to be: endobj, got: " + token)
		}

		// Reposition the file pointer to previous position
		_, err = this.f.Seek(old_pos, 0)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to set position of file")
		}

		return result, nil

	} else {
		return objSpec, nil
	}
}

// streamLength returns the /Length of a stream dictionary, resolving an
// indirect length. The length is checked against the file size before it is
// used to allocate the stream buffer: a missing, negative or absurd /Length
// used to panic or abort the process with an out-of-memory error.
func (this *PdfReader) streamLength(dict *PdfValue) (int, error) {
	lengthValue, ok := dict.Dictionary["/Length"]
	if !ok || lengthValue == nil {
		return 0, errors.New("Stream dictionary has no /Length")
	}

	if lengthValue.Type == PDF_TYPE_OBJREF {
		resolved, err := this.resolveObject(lengthValue)
		if err != nil {
			return 0, errors.Wrap(err, "Failed to resolve length object of stream")
		}
		lengthValue = resolved
		if resolved.Type == PDF_TYPE_OBJECT {
			lengthValue = resolved.Value
		}
	}

	if lengthValue == nil || lengthValue.Type != PDF_TYPE_NUMERIC {
		return 0, errors.New("Stream /Length is not an integer")
	}
	if lengthValue.Int < 0 || int64(lengthValue.Int) > this.nBytes {
		return 0, errors.Errorf("Stream /Length %d is outside the file", lengthValue.Int)
	}

	return lengthValue.Int, nil
}

// Find the xref offset (should be at the end of the PDF)
func (this *PdfReader) findXref() error {
	var result int
	var err error
	var toRead int64

	toRead = 1500

	// If PDF is smaller than 1500 bytes, be sure to only read the number of bytes that are in the file
	fileSize := this.nBytes
	if fileSize < toRead {
		toRead = fileSize
	}

	// 0 means relative to the origin of the file,
	// 1 means relative to the current offset,
	// and 2 means relative to the end.
	whence := 2

	// Perform seek operation
	_, err = this.f.Seek(-toRead, whence)
	if err != nil {
		return errors.Wrap(err, "Failed to set position of file")
	}

	// Create new bufio.Reader
	r := bufio.NewReader(this.f)
	candidates := make([]int, 0, 2)
	for {
		// Read all tokens until the end of the file and keep every
		// "startxref" position. A file with incremental updates contains one
		// startxref per revision; only the newest one points at the current
		// cross-reference section. Using the first one in the trailer window
		// would parse an older revision and resolve objects to stale offsets.
		token, err := this.readToken(r)
		if err != nil {
			return errors.Wrap(err, "Failed to read token")
		}

		// An empty token means the end of the file was reached
		if token == "" {
			break
		}

		if token != "startxref" {
			continue
		}

		token, err = this.readToken(r)
		if err != nil {
			return errors.Wrap(err, "Failed to find startxref token")
		}
		if token == "" {
			break
		}

		// Convert line (string) into int
		result, err = strconv.Atoi(token)
		if err != nil {
			return errors.Wrap(err, "Failed to convert xref position into integer: "+token)
		}

		// Successfully read the xref position
		candidates = append(candidates, result)
	}

	// Rewind file pointer
	whence = 0
	_, err = this.f.Seek(0, whence)
	if err != nil {
		return errors.Wrap(err, "Failed to set position of file")
	}

	// Take the newest position that really points at a cross-reference section.
	// Files that were passed through a tool which appended data after %%EOF can
	// end with a startxref that does not point at anything; falling back to the
	// previous revision keeps them readable.
	for i := len(candidates) - 1; i >= 0; i-- {
		if this.looksLikeXref(candidates[i]) {
			this.xrefPos = candidates[i]
			return nil
		}
	}

	return errors.New("Failed to find startxref token")
}

// looksLikeXref reports whether the offset points at a cross-reference table or
// cross-reference stream object.
func (this *PdfReader) looksLikeXref(offset int) bool {
	if offset <= 0 || int64(offset) >= this.nBytes {
		return false
	}

	if _, err := this.f.Seek(int64(offset), 0); err != nil {
		return false
	}

	r := bufio.NewReader(this.f)
	token, err := this.readToken(r)
	if err != nil || token == "" {
		return false
	}
	if token == "xref" {
		return true
	}

	// A cross-reference stream is an indirect object: "<num> <gen> obj"
	if _, err := strconv.Atoi(token); err != nil {
		return false
	}
	if token, err = this.readToken(r); err != nil {
		return false
	}
	if _, err := strconv.Atoi(token); err != nil {
		return false
	}
	token, err = this.readToken(r)
	return err == nil && token == "obj"
}

// isFreeEntry reports whether an object is only known as a free entry.
func (this *PdfReader) isFreeEntry(id int) bool {
	generations, ok := this.xref[id]
	if !ok {
		return false
	}
	if _, compressed := this.xrefStream[id]; compressed {
		return false
	}
	return len(generations) == 0
}

// hasXrefEntry includes free entries and both object storage formats.
func (this *PdfReader) hasXrefEntry(id int) bool {
	_, regular := this.xref[id]
	_, compressed := this.xrefStream[id]
	return regular || compressed
}

// Read and parse the xref table
func (this *PdfReader) readXref() error {
	var err error

	// Create new bufio.Reader
	r := bufio.NewReader(this.f)

	// Set file pointer to xref start
	_, err = this.f.Seek(int64(this.xrefPos), 0)
	if err != nil {
		return errors.Wrap(err, "Failed to set position of file")
	}

	// Xref should start with 'xref'
	t, err := this.readToken(r)
	if err != nil {
		return errors.Wrap(err, "Failed to read token")
	}
	if t != "xref" {
		// Maybe this is an XRef stream ...
		v, err := this.readValue(r, t)
		if err != nil {
			return errors.Wrap(err, "Failed to read XRef stream")
		}

		if v.Type == PDF_TYPE_OBJDEC {
			// Read next token
			t, err = this.readToken(r)
			if err != nil {
				return errors.Wrap(err, "Failed to read token")
			}

			// Read actual object value
			v, err := this.readValue(r, t)
			if err != nil {
				return errors.Wrap(err, "Failed to read value for token: "+t)
			}

			// If /Type is set, check to see if it is XRef
			if _, ok := v.Dictionary["/Type"]; ok {
				if v.Dictionary["/Type"].Token == "/XRef" {
					// Continue reading xref stream data now that it is confirmed that it is an xref stream

					// Check for /DecodeParms
					paethDecode := false
					if _, ok := v.Dictionary["/DecodeParms"]; ok {
						columns := 0
						predictor := 0

						if _, ok2 := v.Dictionary["/DecodeParms"].Dictionary["/Columns"]; ok2 {
							columns = v.Dictionary["/DecodeParms"].Dictionary["/Columns"].Int
						}
						if _, ok2 := v.Dictionary["/DecodeParms"].Dictionary["/Predictor"]; ok2 {
							predictor = v.Dictionary["/DecodeParms"].Dictionary["/Predictor"].Int
						}

						if predictor > 15 {
							return errors.New("Unsupported /DecodeParms - /Predictor must be <= 15")
						}
						_ = columns
						paethDecode = true
					}

					/*
						// Check to make sure field size is [1 2 1] - not yet tested with other field sizes
						if v.Dictionary["/W"].Array[0].Int != 1 || v.Dictionary["/W"].Array[1].Int > 4 || v.Dictionary["/W"].Array[2].Int != 1 {
							return errors.New(fmt.Sprintf("Unsupported field sizes in cross-reference stream dictionary: /W [%d %d %d]",
								v.Dictionary["/W"].Array[0].Int,
								v.Dictionary["/W"].Array[1].Int,
								v.Dictionary["/W"].Array[2].Int))
						}
					*/

					// /W gives the byte widths of the three fields of every entry
					// and is required. It used to be indexed without checking.
					if w, ok := v.Dictionary["/W"]; !ok || w.Type != PDF_TYPE_ARRAY || len(w.Array) != 3 {
						return errors.New("Cross-reference stream /W is not an array of three widths")
					}
					for _, width := range v.Dictionary["/W"].Array {
						if width.Type != PDF_TYPE_NUMERIC {
							return errors.New("Cross-reference stream /W contains a non-integer width")
						}
					}

					// /Index holds (first object number, entry count) pairs. A
					// cross-reference stream may split its entries into several
					// ranges, which incremental updates do often. Numbering the
					// entries by position only works for the first range; the
					// remaining entries would be attributed to the wrong objects
					// and their offsets resolve to a different object.
					ranges := make([][2]int, 0, 1)
					if indexValue, ok := v.Dictionary["/Index"]; ok {
						if len(indexValue.Array) < 2 || len(indexValue.Array)%2 != 0 {
							return errors.New("Index array does not contain pairs of elements")
						}

						for pair := 0; pair < len(indexValue.Array); pair += 2 {
							first := indexValue.Array[pair].Int
							count := indexValue.Array[pair+1].Int
							ranges = append(ranges, [2]int{first, count})
						}
					} else {
						size, ok := v.Dictionary["/Size"]
						if !ok || size.Type != PDF_TYPE_NUMERIC {
							return errors.New("Cross-reference stream has neither /Index nor an integer /Size")
						}
						count := size.Int
						ranges = append(ranges, [2]int{0, count})
					}

					prevXref := 0

					// Check for previous xref stream
					if _, ok := v.Dictionary["/Prev"]; ok {
						prevXref = v.Dictionary["/Prev"].Int
					}

					// Set root object
					if _, ok := v.Dictionary["/Root"]; ok {
						// Just set the whole dictionary with /Root key to keep compatibiltiy with existing code
						this.trailer = v
					} else {
						// Don't return an error here.  The trailer could be in another XRef stream.
						//return errors.New("Did not set root object")
					}

					err = this.skipWhitespace(r)
					if err != nil {
						return errors.Wrap(err, "Failed to skip whitespace")
					}

					// Get number of bytes of stream
					length, err := this.streamLength(v)
					if err != nil {
						return err
					}

					t, err = this.readToken(r)
					if err != nil {
						return errors.Wrap(err, "Failed to read token")
					}
					if t != "stream" {
						return errors.New("Expected next token to be: stream, got: " + t)
					}

					err = this.skipStreamWhitespace(r)
					if err != nil {
						return errors.Wrap(err, "Failed to skip whitespace")
					}

					// Read length bytes
					data := make([]byte, length)

					// Cannot use reader.Read() because that may not read all the bytes
					_, err = io.ReadFull(r, data)
					if err != nil {
						return errors.Wrap(err, "Failed to read bytes from buffer")
					}

					// Look for endstream token
					t, err = this.readToken(r)
					if err != nil {
						return errors.Wrap(err, "Failed to read token")
					}
					if t != "endstream" {
						return errors.New("Expected next token to be: endstream, got: " + t)
					}

					// Look for endobj token
					t, err = this.readToken(r)
					if err != nil {
						return errors.Wrap(err, "Failed to read token")
					}
					if t != "endobj" {
						return errors.New("Expected next token to be: endobj, got: " + t)
					}

					// Decode the stream with its /Filter chain. /Filter is optional
					// for cross-reference streams as well: qpdf with
					// --stream-data=uncompress writes the entries as they are and
					// drops the filter, which other readers accept.
					filters, err := this.dictionaryFilters(v.Dictionary)
					if err != nil {
						return errors.Wrap(err, "Failed to read xref stream filter")
					}

					p := data
					for _, filter := range filters {
						decoded, err := decodeStreamFilter(filter.Token, p)
						if err != nil {
							return errors.Wrap(err, "Failed to decode xref stream")
						}
						p = decoded
					}

					objPos := 0
					objGen := 0
					rangeIndex, rangeEntry := 0, 0

					// Decode result with paeth algorithm
					var result []byte
					b := bytes.NewReader(p)

					firstFieldSize := v.Dictionary["/W"].Array[0].Int
					middleFieldSize := v.Dictionary["/W"].Array[1].Int
					lastFieldSize := v.Dictionary["/W"].Array[2].Int

					// /W [0 n m] omits the entry type field; every entry is a
					// regular object in that case.
					typeFieldOmitted := firstFieldSize == 0

					fieldSize := firstFieldSize + middleFieldSize + lastFieldSize
					if paethDecode {
						fieldSize++
					}

					dataSize := firstFieldSize + middleFieldSize + lastFieldSize
					if firstFieldSize < 0 || firstFieldSize > 8 || middleFieldSize < 0 || middleFieldSize > 8 || lastFieldSize < 0 || lastFieldSize > 8 || dataSize == 0 {
						return errors.New("Invalid xref field widths")
					}
					if len(p)%fieldSize != 0 {
						return errors.New("Incomplete xref stream entry")
					}
					remaining := len(p) / fieldSize
					for _, span := range ranges {
						if span[0] < 0 || span[1] < 0 || span[1] > remaining || span[0] > int(^uint(0)>>1)-span[1] {
							return errors.New("Invalid xref Index range")
						}
						remaining -= span[1]
					}
					if remaining != 0 {
						return errors.New("Xref entry count does not match Index")
					}
					prevRow := make([]byte, dataSize)
					for {
						result = make([]byte, fieldSize)
						_, err := io.ReadFull(b, result)
						if err != nil {
							if err == io.EOF {
								break
							} else {
								return errors.Wrap(err, "io.ReadFull error")
							}
						}

						for rangeIndex < len(ranges) && rangeEntry == ranges[rangeIndex][1] {
							rangeIndex++
							rangeEntry = 0
						}
						i := ranges[rangeIndex][0] + rangeEntry
						rangeEntry++

						var objectData []byte
						if paethDecode {
							filterType := result[0]
							rowData := result[1:fieldSize] // data portion without filter byte

							switch filterType {
							case 0: // None
							case 1: // Sub
								for si := 1; si < len(rowData); si++ {
									rowData[si] += rowData[si-1]
								}
							case 2: // Up
								for si := 0; si < len(rowData); si++ {
									rowData[si] += prevRow[si]
								}
							case 3: // Average
								for si := 0; si < len(rowData); si++ {
									var left byte
									if si > 0 {
										left = rowData[si-1]
									}
									rowData[si] += byte((int(left) + int(prevRow[si])) / 2)
								}
							case 4: // Paeth
								for si := 0; si < len(rowData); si++ {
									var left, upperLeft byte
									if si > 0 {
										left = rowData[si-1]
										upperLeft = prevRow[si-1]
									}
									rowData[si] += paeth(left, prevRow[si], upperLeft)
								}
							}
							copy(prevRow, rowData)
							objectData = rowData
						} else {
							objectData = result[0:fieldSize]
						}

						entryType := 1
						if !typeFieldOmitted {
							entryType = int(objectData[0])
						}

						// A newer entry wins regardless of storage type or generation.
						// The /XRefStm of a hybrid-reference file is part of the same
						// revision as the classic table, which recorded its compressed
						// objects as free entries; that stream may replace them.
						if this.hasXrefEntry(i) {
							if !this.overrideFreeEntries || !this.isFreeEntry(i) {
								continue
							}
							delete(this.xref, i)
						}
						if entryType == 0 {
							// Keep a tombstone so older revisions cannot revive free objects.
							this.xref[i] = make(map[int]int)
						} else if entryType == 1 {
							// Regular objects
							objPos = readUintBE(objectData, firstFieldSize, middleFieldSize)
							objGen = readUintBE(objectData, firstFieldSize+middleFieldSize, lastFieldSize)

							this.xref[i] = map[int]int{objGen: objPos}
						} else if entryType == 2 {
							// Compressed objects
							objId := readUintBE(objectData, firstFieldSize, middleFieldSize)
							objIdx := readUintBE(objectData, firstFieldSize+middleFieldSize, lastFieldSize)

							// object id (i) is located in StmObj (objId) at index (objIdx)
							this.xrefStream[i] = [2]int{objId, objIdx}
						}
					}

					// Check for previous xref stream
					if prevXref > 0 {
						// Set xrefPos to /Prev xref
						this.overrideFreeEntries = false
						this.xrefPos = prevXref

						// Read preivous xref
						xrefErr := this.readXref()
						if xrefErr != nil {
							return errors.Wrap(xrefErr, "Failed to read prev xref")
						}
					}
				}
			}

			return nil
		}

		return errors.New("Expected xref to start with 'xref'.  Got: " + t)
	}

	for {
		// Next value will be the starting object id (usually 0, but not always) or the trailer
		t, err = this.readToken(r)
		if err != nil {
			return errors.Wrap(err, "Failed to read token")
		}

		// Check for trailer
		if t == "trailer" {
			break
		}

		// Convert token to int
		startObject, err := strconv.Atoi(t)
		if err != nil {
			return errors.Wrap(err, "Failed to convert start object to integer: "+t)
		}

		// Determine how many objects there are
		t, err = this.readToken(r)
		if err != nil {
			return errors.Wrap(err, "Failed to read token")
		}

		// Convert token to int
		numObject, err := strconv.Atoi(t)
		if err != nil {
			return errors.Wrap(err, "Failed to convert num object to integer: "+t)
		}

		// For all objects in xref, read object position, object generation, and status (free or new)
		for i := startObject; i < startObject+numObject; i++ {
			t, err = this.readToken(r)
			if err != nil {
				return errors.Wrap(err, "Failed to read token")
			}

			// Get object position as int
			objPos, err := strconv.Atoi(t)
			if err != nil {
				return errors.Wrap(err, "Failed to convert object position to integer: "+t)
			}

			t, err = this.readToken(r)
			if err != nil {
				return errors.Wrap(err, "Failed to read token")
			}

			// Get object generation as int
			objGen, err := strconv.Atoi(t)
			if err != nil {
				return errors.Wrap(err, "Failed to convert object generation to integer: "+t)
			}

			// Get object status (free or new)
			objStatus, err := this.readToken(r)
			if err != nil {
				return errors.Wrap(err, "Failed to read token")
			}
			if objStatus != "f" && objStatus != "n" {
				return errors.New("Expected objStatus to be 'n' or 'f', got: " + objStatus)
			}

			// Since the XREF table is read from newest to oldest, make sure an xref entry does not already exist for an object.
			// If it already exists, that means a newer version of the object has already been added to the table.
			// Replacing it would be using the old version of the object.
			// https://github.com/phpdave11/gofpdi/issues/71
			if !this.hasXrefEntry(i) {
				// Append map[int]int
				this.xref[i] = make(map[int]int, 1)

				// Set object id, generation, and position
				if objStatus == "n" {
					this.xref[i][objGen] = objPos
				}
			}
		}
	}

	// Read trailer dictionary
	t, err = this.readToken(r)
	if err != nil {
		return errors.Wrap(err, "Failed to read token")
	}

	trailer, err := this.readValue(r, t)
	if err != nil {
		return errors.Wrap(err, "Failed to read value for token: "+t)
	}

	// If /Root is set, then set trailer object so that /Root can be read later
	if _, ok := trailer.Dictionary["/Root"]; ok {
		this.trailer = trailer
	}

	// A hybrid-reference file keeps a classic table and stores compressed objects
	// in a cross-reference stream referenced by /XRefStm. The classic table lists
	// those objects as free entries, so the stream has to be read as part of this
	// revision before older revisions are merged.
	if xrefStm, ok := trailer.Dictionary["/XRefStm"]; ok {
		previousPos := this.xrefPos
		this.xrefPos = xrefStm.Int
		this.overrideFreeEntries = true
		xrefErr := this.readXref()
		this.overrideFreeEntries = false
		this.xrefPos = previousPos
		if xrefErr != nil {
			return errors.Wrap(xrefErr, "Failed to read /XRefStm")
		}
	}

	// If a /Prev xref trailer is specified, parse that
	if tr, ok := trailer.Dictionary["/Prev"]; ok {
		// Resolve parent xref table
		this.overrideFreeEntries = false
		this.xrefPos = tr.Int
		return this.readXref()
	}

	return nil
}

// Read root (catalog object)
func (this *PdfReader) readRoot() error {
	var err error

	// The trailer is only recorded when it has /Root, so a file whose trailers
	// all lack it leaves this.trailer nil.
	if this.trailer == nil {
		return errors.New("Trailer has no /Root")
	}
	rootObjSpec := this.trailer.Dictionary["/Root"]
	if rootObjSpec == nil || rootObjSpec.Type != PDF_TYPE_OBJREF {
		return errors.New("Trailer /Root is not an object reference")
	}

	// Read root (catalog)
	this.catalog, err = this.resolveObject(rootObjSpec)
	if err != nil {
		return errors.Wrap(err, "Failed to resolve root object")
	}
	if this.catalog.Value == nil || this.catalog.Value.Type != PDF_TYPE_DICTIONARY {
		return errors.New("Document catalog is not a dictionary")
	}
	if _, ok := this.catalog.Value.Dictionary["/Pages"]; !ok {
		return errors.New("Document catalog has no /Pages")
	}

	return nil
}

// Read kids (pages inside a page tree)
func (this *PdfReader) readKids(kids *PdfValue, r int) error {
	// Loop through pages and add to result
	for i := 0; i < len(kids.Array); i++ {
		page, err := this.resolveObject(kids.Array[i])
		if err != nil {
			return errors.Wrap(err, "Failed to resolve page/pages object")
		}

		objType := page.Value.Dictionary["/Type"].Token
		if objType == "/Page" {
			// Set page and increment curPage
			this.pages[this.curPage] = page
			this.curPage++
		} else if objType == "/Pages" {
			// Resolve kids
			subKids, err := this.resolveObject(page.Value.Dictionary["/Kids"])
			if err != nil {
				return errors.Wrap(err, "Failed to resolve kids")
			}

			// Recurse into page tree
			err = this.readKids(subKids, r+1)
			if err != nil {
				return errors.Wrap(err, "Failed to read kids")
			}
		} else {
			return errors.Errorf("Unknown object type '%s'.  Expected: /Pages or /Page", objType)
		}
	}

	return nil
}

// Read all pages in PDF
func (this *PdfReader) readPages() error {
	var err error

	// resolve_pages_dict
	pagesDict, err := this.resolveObject(this.catalog.Value.Dictionary["/Pages"])
	if err != nil {
		return errors.Wrap(err, "Failed to resolve pages object")
	}

	// This will normally return itself
	kids, err := this.resolveObject(pagesDict.Value.Dictionary["/Kids"])
	if err != nil {
		return errors.Wrap(err, "Failed to resolve kids object")
	}

	// Get number of pages
	pageCount, err := this.resolveObject(pagesDict.Value.Dictionary["/Count"])
	if err != nil {
		return errors.Wrap(err, "Failed to get page count")
	}
	this.pageCount = pageCount.Int

	// Allocate pages
	this.pages = make([]*PdfValue, pageCount.Int)

	// Read kids
	err = this.readKids(kids, 0)
	if err != nil {
		return errors.Wrap(err, "Failed to read kids")
	}

	return nil
}

// Get references to page resources for a given page number
func (this *PdfReader) getPageResources(pageno int) (*PdfValue, error) {
	if pageno < 1 || len(this.pages) < pageno {
		return nil, errors.New(fmt.Sprintf("Page %d does not exist!!", pageno))
	}

	// Resources are inherited from the nearest ancestor that defines them,
	// not from the ancestor's entire page-tree dictionary.
	seen := make(map[[2]int]bool)
	for page := this.pages[pageno-1]; page != nil; {
		if page.Type == PDF_TYPE_OBJREF || page.Type == PDF_TYPE_OBJECT {
			key := [2]int{page.Id, page.Gen}
			if seen[key] {
				return nil, errors.New("Cycle in page resource inheritance")
			}
			seen[key] = true
		}
		resolved, err := this.resolveObject(page)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to resolve page resource ancestor")
		}
		if resolved.Type == PDF_TYPE_OBJECT {
			resolved = resolved.Value
		}
		if resolved == nil || resolved.Type != PDF_TYPE_DICTIONARY {
			return nil, errors.New("Page resource ancestor is not a dictionary")
		}
		if resources, ok := resolved.Dictionary["/Resources"]; ok {
			res, err := this.resolveObject(resources)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to resolve resources object")
			}
			if res.Type == PDF_TYPE_OBJECT {
				return res.Value, nil
			}
			return res, nil
		}
		page = resolved.Dictionary["/Parent"]
	}

	return &PdfValue{}, nil
}

// Get page content and return a slice of PdfValue objects
func (this *PdfReader) getPageContent(objSpec *PdfValue) ([]*PdfValue, error) {
	return this.getPageContentWithRefs(objSpec, make(map[[2]int]bool))
}

func (this *PdfReader) getPageContentWithRefs(objSpec *PdfValue, active map[[2]int]bool) ([]*PdfValue, error) {
	var err error
	var content *PdfValue

	// Allocate slice
	contents := make([]*PdfValue, 0)

	if objSpec.Type == PDF_TYPE_OBJREF {
		ref := [2]int{objSpec.Id, objSpec.Gen}
		if active[ref] {
			return nil, errors.New("cyclic page content reference")
		}
		active[ref] = true
		defer delete(active, ref)

		content, err = this.resolveObject(objSpec)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to resolve object")
		}
		if content.Value != nil && content.Value.Type == PDF_TYPE_ARRAY {
			return this.getPageContentWithRefs(content.Value, active)
		}
		contents = append(contents, content)
	} else if objSpec.Type == PDF_TYPE_ARRAY {
		// If objSpec is an array, loop through the array and recursively get page content and append to contents
		for i := 0; i < len(objSpec.Array); i++ {
			tmpContents, err := this.getPageContentWithRefs(objSpec.Array[i], active)
			if err != nil {
				return nil, errors.Wrap(err, "Failed to get page content")
			}
			for j := 0; j < len(tmpContents); j++ {
				contents = append(contents, tmpContents[j])
			}
		}
	}

	return contents, nil
}

// Get content (i.e. PDF drawing instructions)
func (this *PdfReader) getContent(pageno int) (string, error) {
	var err error
	var contents []*PdfValue

	// Check to make sure page exists in pages slice
	if pageno < 1 || len(this.pages) < pageno {
		return "", errors.New(fmt.Sprintf("Page %d does not exist.", pageno))
	}

	// Get page
	page := this.pages[pageno-1]

	// FIXME: This could be slow, converting []byte to string and appending many times
	buffer := ""

	// Check to make sure /Contents exists in page dictionary
	if _, ok := page.Value.Dictionary["/Contents"]; ok {
		// Get an array of page content
		contents, err = this.getPageContent(page.Value.Dictionary["/Contents"])
		if err != nil {
			return "", errors.Wrap(err, "Failed to get page content")
		}

		for i := 0; i < len(contents); i++ {
			// Decode content if one or more /Filter is specified.
			// Most common filter is FlateDecode which can be uncompressed with zlib
			tmpBuffer, err := this.rebuildContentStream(contents[i])
			if err != nil {
				return "", errors.Wrap(err, "Failed to rebuild content stream")
			}

			// FIXME:  This is probably slow
			buffer += string(tmpBuffer)
		}
	}

	return buffer, nil
}

// Rebuild content stream
// This will decode content if one or more /Filter (such as FlateDecode) is specified.
// If there are multiple filters, they will be decoded in the order in which they were specified.
func (this *PdfReader) rebuildContentStream(content *PdfValue) ([]byte, error) {
	if content.Stream == nil {
		return nil, errors.New("content object is not a stream")
	}

	filters, err := this.streamFilters(content)
	if err != nil {
		return nil, err
	}

	// Set stream variable to content bytes
	stream := content.Stream.Bytes

	// Loop through filters and apply each filter to stream
	for i := 0; i < len(filters); i++ {
		decoded, err := decodeStreamFilter(filters[i].Token, stream)
		if err != nil {
			return nil, errors.Wrap(err, "Unsupported filter: "+filters[i].Token)
		}

		// Set stream to uncompressed data
		stream = decoded
	}

	return stream, nil
}

// streamFilters returns the /Filter entries of a stream value, resolving object
// references and flattening a filter array into the order the filters are applied.
func (this *PdfReader) streamFilters(value *PdfValue) ([]*PdfValue, error) {
	return this.dictionaryFilters(value.Value.Dictionary)
}

// dictionaryFilters returns the /Filter entries of a stream dictionary. A stream
// without /Filter is stored as it is, so no filters are returned.
func (this *PdfReader) dictionaryFilters(dictionary map[string]*PdfValue) ([]*PdfValue, error) {
	filters := make([]*PdfValue, 0, 1)

	filter, ok := dictionary["/Filter"]
	if !ok {
		return filters, nil
	}

	// If filter type is a reference, resolve it
	if filter.Type == PDF_TYPE_OBJREF {
		resolved, err := this.resolveObject(filter)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to resolve object")
		}
		filter = resolved.Value
	}

	switch filter.Type {
	case PDF_TYPE_TOKEN:
		// A single filter (e.g. /FlateDecode)
		filters = append(filters, filter)
	case PDF_TYPE_ARRAY:
		// Multiple filters, applied in the order they are listed
		for _, entry := range filter.Array {
			if entry.Type == PDF_TYPE_OBJREF {
				resolved, err := this.resolveObject(entry)
				if err != nil {
					return nil, errors.Wrap(err, "Failed to resolve filter object")
				}
				entry = resolved.Value
			}
			filters = append(filters, entry)
		}
	}

	return filters, nil
}

// decodeStreamFilter decodes stream bytes with a single PDF filter. Only the
// filters that can appear in page content and object streams are supported; any
// other filter returns an error instead of panicking the caller.
func decodeStreamFilter(token string, stream []byte) ([]byte, error) {
	switch token {
	case "/FlateDecode", "/Fl":
		return decodeFlate(stream)
	case "/ASCII85Decode", "/A85":
		return decodeASCII85(stream)
	case "/ASCIIHexDecode", "/AHx":
		return decodeASCIIHex(stream)
	case "/RunLengthDecode", "/RL":
		return decodeRunLength(stream)
	default:
		return nil, errors.New("unsupported filter")
	}
}

// decodeFlate inflates zlib compressed data. Streams written by several
// producers end with a truncated checksum; the decompressed bytes are still
// usable, so they are returned when some data could be read.
func decodeFlate(stream []byte) ([]byte, error) {
	zlibReader, err := zlib.NewReader(bytes.NewReader(stream))
	if err != nil {
		return nil, errors.Wrap(err, "zlib.NewReader error")
	}
	defer zlibReader.Close()

	var out bytes.Buffer
	if _, err := io.Copy(&out, zlibReader); err != nil && out.Len() == 0 {
		return nil, errors.Wrap(err, "zlib read error")
	}

	return out.Bytes(), nil
}

// decodeASCII85 decodes /ASCII85Decode data, including the "z" shortcut for
// four zero bytes and the "~>" end-of-data marker. Whitespace is ignored.
func decodeASCII85(stream []byte) ([]byte, error) {
	out := make([]byte, 0, len(stream))
	var group [5]byte
	n := 0

	decodeGroup := func(count int) {
		var value uint32
		for i := 0; i < 5; i++ {
			value = value*85 + uint32(group[i])
		}
		buf := [4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
		out = append(out, buf[:count]...)
	}

	done := false
	for i := 0; i < len(stream) && !done; i++ {
		c := stream[i]
		switch {
		case c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0:
			continue
		case c == '~':
			// "~>" terminates the data. A lone "~" is accepted as well.
			done = true
			continue
		case c == 'z':
			if n != 0 {
				return nil, errors.New("'z' inside an ASCII85 group")
			}
			out = append(out, 0, 0, 0, 0)
			continue
		case c < '!' || c > 'u':
			return nil, errors.New(fmt.Sprintf("invalid ASCII85 character: %q", c))
		}

		group[n] = c - '!'
		n++
		if n == 5 {
			decodeGroup(4)
			n = 0
		}
	}

	// A trailing partial group of n characters encodes n-1 bytes; pad with 'u'.
	if n > 1 {
		for i := n; i < 5; i++ {
			group[i] = 'u' - '!'
		}
		decodeGroup(n - 1)
	} else if n == 1 {
		return nil, errors.New("truncated ASCII85 group")
	}

	return out, nil
}

// decodeASCIIHex ignores PDF whitespace and pads a final odd digit with zero.
func decodeASCIIHex(stream []byte) ([]byte, error) {
	out := make([]byte, 0, len(stream)/2)
	high := -1
	for _, c := range stream {
		if c == '>' {
			break
		}
		var digit int
		switch {
		case c == 0 || c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f':
			continue
		case c >= '0' && c <= '9':
			digit = int(c - '0')
		case c >= 'a' && c <= 'f':
			digit = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			digit = int(c-'A') + 10
		default:
			return nil, errors.Errorf("invalid ASCIIHex character: %q", c)
		}
		if high < 0 {
			high = digit
		} else {
			out = append(out, byte(high<<4|digit))
			high = -1
		}
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return out, nil
}

// decodeRunLength decodes /RunLengthDecode data.
func decodeRunLength(stream []byte) ([]byte, error) {
	out := make([]byte, 0, len(stream))
	for i := 0; i < len(stream); {
		length := int(stream[i])
		i++

		switch {
		case length == 128:
			// End of data
			return out, nil
		case length < 128:
			end := i + length + 1
			if end > len(stream) {
				end = len(stream)
			}
			out = append(out, stream[i:end]...)
			i = end
		default:
			if i >= len(stream) {
				return nil, errors.New("truncated run length data")
			}
			for k := 0; k < 257-length; k++ {
				out = append(out, stream[i])
			}
			i++
		}
	}

	return out, nil
}

func (this *PdfReader) getNumPages() (int, error) {
	if this.pageCount == 0 {
		return 0, errors.New("Page count is 0")
	}

	return this.pageCount, nil
}

func (this *PdfReader) getAllPageBoxes(k float64) (map[int]map[string]map[string]float64, error) {
	var err error

	// Allocate result with the number of available boxes
	result := make(map[int]map[string]map[string]float64, len(this.pages))

	for i := 1; i <= len(this.pages); i++ {
		result[i], err = this.getPageBoxes(i, k)
		if result[i] == nil {
			return nil, errors.Wrap(err, "Unable to get page box")
		}
	}

	return result, nil
}

// Get all page box data
func (this *PdfReader) getPageBoxes(pageno int, k float64) (map[string]map[string]float64, error) {
	var err error

	// Allocate result with the number of available boxes
	result := make(map[string]map[string]float64, len(this.availableBoxes))

	// Check to make sure page exists in pages slice
	if pageno < 1 || len(this.pages) < pageno {
		return nil, errors.New(fmt.Sprintf("Page %d does not exist?", pageno))
	}

	// Resolve page object
	page, err := this.resolveObject(this.pages[pageno-1])
	if err != nil {
		return nil, errors.New("Failed to resolve page object")
	}

	// Loop through available boxes and add to result
	for i := 0; i < len(this.availableBoxes); i++ {
		box, err := this.getPageBox(page, this.availableBoxes[i], k)
		if err != nil {
			return nil, errors.New("Failed to get page box")
		}

		result[this.availableBoxes[i]] = box
	}

	return result, nil
}

// Get a specific page box value (e.g. MediaBox) and return its values
func (this *PdfReader) getPageBox(page *PdfValue, box_index string, k float64) (map[string]float64, error) {
	var err error
	var tmpBox *PdfValue

	// Allocate 8 fields in result
	result := make(map[string]float64, 8)

	// Check to make sure box_index (e.g. MediaBox) exists in page dictionary
	if _, ok := page.Value.Dictionary[box_index]; ok {
		box := page.Value.Dictionary[box_index]

		// If the box type is a reference, resolve it
		if box.Type == PDF_TYPE_OBJREF {
			tmpBox, err = this.resolveObject(box)
			if err != nil {
				return nil, errors.New("Failed to resolve object")
			}
			box = tmpBox.Value
		}

		if box.Type == PDF_TYPE_ARRAY {
			if len(box.Array) != 4 {
				return nil, errors.New("Page box must contain four coordinates")
			}
			// If the box type is an array, calculate scaled value based on k
			result["x"] = box.Array[0].Real / k
			result["y"] = box.Array[1].Real / k
			result["w"] = math.Abs(box.Array[0].Real-box.Array[2].Real) / k
			result["h"] = math.Abs(box.Array[1].Real-box.Array[3].Real) / k
			result["llx"] = math.Min(box.Array[0].Real, box.Array[2].Real)
			result["lly"] = math.Min(box.Array[1].Real, box.Array[3].Real)
			result["urx"] = math.Max(box.Array[0].Real, box.Array[2].Real)
			result["ury"] = math.Max(box.Array[1].Real, box.Array[3].Real)
		} else {
			// TODO: Improve error handling
			return nil, errors.New("Could not get page box")
		}
	} else if _, ok := page.Value.Dictionary["/Parent"]; ok && (box_index == "/MediaBox" || box_index == "/CropBox") {
		// Only MediaBox and CropBox are inheritable page boxes. The other
		// boxes default to CropBox when absent on the page itself.
		parentObj, err := this.resolveObject(page.Value.Dictionary["/Parent"])
		if err != nil {
			return nil, errors.Wrap(err, "Could not resolve parent object")
		}

		// If the page box is inherited from /Parent, recursively return page box of parent
		return this.getPageBox(parentObj, box_index, k)
	}

	return result, nil
}

// Get page rotation for a page number
func (this *PdfReader) getPageRotation(pageno int) (*PdfValue, error) {
	// Check to make sure page exists in pages slice
	if pageno < 1 || len(this.pages) < pageno {
		return nil, errors.New(fmt.Sprintf("Page %d does not exist!!!!", pageno))
	}

	return this._getPageRotation(this.pages[pageno-1])
}

// Get page rotation for a page object spec
func (this *PdfReader) _getPageRotation(page *PdfValue) (*PdfValue, error) {
	var err error

	// Resolve page object
	page, err = this.resolveObject(page)
	if err != nil {
		return nil, errors.New("Failed to resolve page object")
	}

	// Check to make sure /Rotate exists in page dictionary
	if _, ok := page.Value.Dictionary["/Rotate"]; ok {
		res, err := this.resolveObject(page.Value.Dictionary["/Rotate"])
		if err != nil {
			return nil, errors.New("Failed to resolve rotate object")
		}

		// If the type is PDF_TYPE_OBJECT, return its value
		if res.Type == PDF_TYPE_OBJECT {
			return res.Value, nil
		}

		// Otherwise, return the object
		return res, nil
	} else {
		// Check to see if parent has a rotation
		if _, ok := page.Value.Dictionary["/Parent"]; ok {
			// Recursively return /Parent page rotation
			res, err := this._getPageRotation(page.Value.Dictionary["/Parent"])
			if err != nil {
				return nil, errors.Wrap(err, "Failed to get page rotation for parent")
			}

			// If the type is PDF_TYPE_OBJECT, return its value
			if res.Type == PDF_TYPE_OBJECT {
				return res.Value, nil
			}

			// Otherwise, return the object
			return res, nil
		}
	}

	return &PdfValue{Int: 0}, nil
}

func (this *PdfReader) read() error {
	// Only run once
	if !this.alreadyRead {
		var err error

		// Find xref position
		err = this.findXref()
		if err != nil {
			return errors.Wrap(err, "Failed to find xref position")
		}

		// Parse xref table
		err = this.readXref()
		if err != nil {
			return errors.Wrap(err, "Failed to read xref table")
		}

		// Read catalog
		err = this.readRoot()
		if err != nil {
			return errors.Wrap(err, "Failed to read root")
		}

		// Read pages
		err = this.readPages()
		if err != nil {
			return errors.Wrap(err, "Failed to to read pages")
		}

		// Now that this has been read, do not read again
		this.alreadyRead = true
	}

	return nil
}
