package gofpdi

import (
	"errors"
	"strings"
	"testing"
)

// streamStub is an io.ReadSeeker whose reads and seeks fail on demand. It stands
// for input that cannot be measured or read, such as a closed network stream.
type streamStub struct {
	size    int64
	readErr error
	seekErr error
}

func (s streamStub) Read([]byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return 0, nil
}

func (s streamStub) Seek(int64, int) (int64, error) {
	if s.seekErr != nil {
		return 0, s.seekErr
	}
	return s.size, nil
}

func TestNewPdfReaderFromStreamReportsStreamErrors(t *testing.T) {
	tests := []struct {
		name    string
		stream  streamStub
		wantErr string
	}{
		{
			name:    "seek",
			stream:  streamStub{seekErr: errors.New("seek failed")},
			wantErr: "stream length",
		},
		{
			name:    "read",
			stream:  streamStub{size: 24, readErr: errors.New("read failed")},
			wantErr: "initialize parser",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("test.pdf", test.stream)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

func TestNewPdfReaderRejectsDirectory(t *testing.T) {
	// Opening a directory succeeds, so the failure is reported while the file
	// is parsed rather than when it is opened.
	_, err := NewPdfReader(t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a directory")
	}
	if !strings.Contains(err.Error(), "Failed to read pdf") {
		t.Fatalf("err = %v, want a read error", err)
	}
}
