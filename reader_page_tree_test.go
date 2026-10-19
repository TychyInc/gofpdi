package gofpdi

import (
	"bytes"
	"fmt"
	"runtime/debug"
	"strings"
	"testing"
)

// limitStack makes a regression back to unbounded recursion fail fast instead
// of growing the goroutine stack to the 1 GB default.
func limitStack(t *testing.T) {
	t.Helper()
	old := debug.SetMaxStack(64 << 20)
	t.Cleanup(func() { debug.SetMaxStack(old) })
}

// TestPageCountComesFromTheTree checks that a wrong /Count on the root does not
// panic (too small) or leave nil pages (too large).
func TestPageCountComesFromTheTree(t *testing.T) {
	for _, count := range []int{0, 1, 5} {
		t.Run(fmt.Sprintf("Count=%d", count), func(t *testing.T) {
			pdf := classicPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: fmt.Sprintf("<< /Type /Pages /Kids [3 0 R 4 0 R] /Count %d /MediaBox [0 0 100 200] >>", count),
				3: "<< /Type /Page /Parent 2 0 R >>",
				4: "<< /Type /Page /Parent 2 0 R >>",
			})
			reader := newTestReader(t, pdf)

			pages, err := reader.getNumPages()
			if err != nil || pages != 2 {
				t.Fatalf("getNumPages = %d, %v; want 2", pages, err)
			}
			boxes, err := reader.getAllPageBoxes(1)
			if err != nil {
				t.Fatalf("failed to read page boxes: %v", err)
			}
			if got := boxes[2]["/MediaBox"]; got["w"] != 100 || got["h"] != 200 {
				t.Fatalf("MediaBox = %v, want 100x200", got)
			}
		})
	}
}

// TestPageTreeNodesWithoutType checks that nodes missing the required /Type are
// classified by the presence of /Kids instead of dereferencing nil.
func TestPageTreeNodesWithoutType(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Kids [3 0 R] /Count 1 >>",
		3: "<< /Parent 2 0 R /Kids [4 0 R] /Count 1 >>",
		4: "<< /Parent 3 0 R /MediaBox [0 0 300 400] >>",
	})
	reader := newTestReader(t, pdf)

	boxes, err := reader.getAllPageBoxes(1)
	if err != nil {
		t.Fatalf("failed to read page boxes: %v", err)
	}
	if len(boxes) != 1 || boxes[1]["/MediaBox"]["w"] != 300 {
		t.Fatalf("unexpected boxes: %v", boxes)
	}
}

// TestIndirectKidsArray checks /Kids given as a reference to an array object.
func TestIndirectKidsArray(t *testing.T) {
	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids 5 0 R /Count 2 /MediaBox [0 0 1 1] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R >>",
		5: "[3 0 R 4 0 R]",
	})
	if pages, err := newTestReader(t, pdf).getNumPages(); err != nil || pages != 2 {
		t.Fatalf("getNumPages = %d, %v; want 2", pages, err)
	}
}

// TestPageTreeCyclesAreErrors covers page trees that loop back on themselves.
// Before the fix these recursed until the stack overflowed, which is a fatal
// error that recover() cannot catch and that kills the whole process.
func TestPageTreeCyclesAreErrors(t *testing.T) {
	limitStack(t)

	cases := map[string]map[int]string{
		"root lists itself": {
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [2 0 R] /Count 1 >>",
		},
		"child lists ancestor": {
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			3: "<< /Type /Pages /Parent 2 0 R /Kids [2 0 R] /Count 1 >>",
		},
		"page listed twice": {
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R 3 0 R] /Count 2 >>",
			3: "<< /Type /Page /Parent 2 0 R >>",
		},
	}
	for name, objects := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(classicPDF(objects)))
			if err == nil {
				t.Fatal("expected an error for a cyclic page tree")
			}
		})
	}
}

// TestDeepPageTreeIsRejected checks the nesting limit on /Pages nodes.
func TestDeepPageTreeIsRejected(t *testing.T) {
	limitStack(t)

	objects := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	const depth = maxPageTreeDepth + 10
	for id := 2; id < depth; id++ {
		objects[id] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", id+1)
	}
	objects[depth] = "<< /Type /Page /MediaBox [0 0 1 1] >>"

	_, err := NewPdfReaderFromStream("test.pdf", bytes.NewReader(classicPDF(objects)))
	if err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("err = %v, want nesting error", err)
	}
}

// TestParentCycleIsAnError checks inherited attribute lookups on a /Parent
// chain that loops. The page tree itself is walked through /Kids and is fine.
func TestParentCycleIsAnError(t *testing.T) {
	limitStack(t)

	pdf := classicPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /Parent 3 0 R >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	})
	reader := newTestReader(t, pdf)

	if _, err := reader.getPageRotation(1); err == nil {
		t.Fatal("expected getPageRotation to report the cycle")
	}
	if _, err := reader.getAllPageBoxes(1); err == nil {
		t.Fatal("expected getAllPageBoxes to report the cycle")
	}
	if _, err := reader.getPageResources(1); err == nil {
		t.Fatal("expected getPageResources to report the cycle")
	}
}

// TestShortPageBoxIsAnError checks that a box with fewer than four numbers is
// reported instead of indexing past the array.
func TestShortPageBoxIsAnError(t *testing.T) {
	for _, box := range []string{"[0 0 100]", "[]", "[0 0 100 /Foo]"} {
		t.Run(box, func(t *testing.T) {
			pdf := classicPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
				3: "<< /Type /Page /Parent 2 0 R /MediaBox " + box + " >>",
			})
			if _, err := newTestReader(t, pdf).getAllPageBoxes(1); err == nil {
				t.Fatal("expected an error for a malformed box")
			}
		})
	}
}

// TestPageNumberZero checks that page numbers below 1 are rejected.
func TestPageNumberZero(t *testing.T) {
	reader := newTestReader(t, singlePagePDF("q Q\n"))

	if _, err := reader.getContent(0); err == nil {
		t.Fatal("getContent(0) succeeded")
	}
	if _, err := reader.getPageBoxes(0, 1); err == nil {
		t.Fatal("getPageBoxes(0) succeeded")
	}
	if _, err := reader.getPageRotation(0); err == nil {
		t.Fatal("getPageRotation(0) succeeded")
	}
}
