package gofpdi

import (
	"strings"
	"testing"
)

func TestIndirectContentsArrayRejectsCycle(t *testing.T) {
	r := newTestReader(t, fixturePDF(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 595 842] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>",
		4: "[4 0 R]",
	}))
	_, err := r.getContent(1)
	if err == nil || !strings.Contains(err.Error(), "cyclic page content reference") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}
