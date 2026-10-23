package gofpdi

import "testing"

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		name string
		val  interface{}
		want bool
	}{
		{name: "int", val: 12, want: true},
		{name: "uint64", val: uint64(12), want: true},
		{name: "float", val: 1.5, want: true},
		{name: "plain string", val: "12", want: true},
		{name: "negative string", val: "-12", want: true},
		{name: "positive sign", val: "+12", want: true},
		{name: "whitespace", val: "  12\n", want: true},
		{name: "decimal", val: "12.5", want: true},
		{name: "scientific", val: "1e10", want: true},
		{name: "hex", val: "0x1f", want: true},
		{name: "empty", val: "", want: false},
		{name: "sign only", val: "-", want: false},
		{name: "leading space", val: "  ", want: false},
		{name: "two points", val: "1.2.3", want: false},
		{name: "trailing point", val: "12.", want: true},
		{name: "leading point", val: ".5", want: true},
		{name: "negative leading point", val: "-.5", want: true},
		{name: "signed trailing point", val: "+12.", want: true},
		{name: "point only", val: ".", want: false},
		{name: "signed point only", val: "-.", want: false},
		{name: "two leading points", val: "..5", want: false},
		{name: "leading and trailing points", val: ".5.", want: false},
		{name: "exponent without digits", val: "1e", want: false},
		{name: "exponent first", val: "e10", want: false},
		{name: "double exponent", val: "1e2e3", want: false},
		{name: "hex without digits", val: "0x", want: false},
		{name: "bad hex digit", val: "0xg", want: false},
		{name: "letters", val: "abc", want: false},
		{name: "bool", val: true, want: false},
		{name: "nil", val: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := is_numeric(test.val); got != test.want {
				t.Fatalf("is_numeric(%#v) = %t, want %t", test.val, got, test.want)
			}
		})
	}
}

func TestInArray(t *testing.T) {
	strings := []string{"a", "b"}
	ints := []int{1, 2}
	int64s := []int64{1, 2}

	if !in_array("b", strings) {
		t.Error("string should be found")
	}
	if in_array("c", strings) {
		t.Error("missing string should not be found")
	}
	if !in_array(2, ints) {
		t.Error("int should be found")
	}
	if in_array(3, ints) {
		t.Error("missing int should not be found")
	}
	if !in_array(int64(1), int64s) {
		t.Error("int64 should be found")
	}
	if in_array(int64(3), int64s) {
		t.Error("missing int64 should not be found")
	}
	if in_array(1.5, []float64{1.5}) {
		t.Error("unsupported types should not be found")
	}
}

func TestAbs(t *testing.T) {
	if got := abs(0); got != 0 {
		t.Fatalf("abs(0) = %d", got)
	}
	if got := abs(7); got != 7 {
		t.Fatalf("abs(7) = %d", got)
	}
	if got := abs(-7); got != 7 {
		t.Fatalf("abs(-7) = %d", got)
	}
}

func TestPaeth(t *testing.T) {
	tests := []struct {
		a, b, c, want byte
	}{
		{a: 10, b: 20, c: 30, want: 10}, // p=0, a is nearest
		{a: 10, b: 20, c: 5, want: 20},  // p=25, b is nearest
		{a: 10, b: 20, c: 15, want: 15}, // p=15, c is nearest
		{a: 0, b: 0, c: 0, want: 0},
	}
	for _, test := range tests {
		if got := paeth(test.a, test.b, test.c); got != test.want {
			t.Fatalf("paeth(%d, %d, %d) = %d, want %d", test.a, test.b, test.c, got, test.want)
		}
	}
}

func TestReadUintBE(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	if got := readUintBE(data, 0, 4); got != 0x01020304 {
		t.Fatalf("readUintBE(4 bytes) = %#x", got)
	}
	if got := readUintBE(data, 1, 2); got != 0x0203 {
		t.Fatalf("readUintBE(offset 1, 2 bytes) = %#x", got)
	}
	if got := readUintBE(data, 3, 1); got != 0x04 {
		t.Fatalf("readUintBE(offset 3, 1 byte) = %#x", got)
	}
}
