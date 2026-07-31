package qr

import (
	"strings"
	"testing"
)

// TestReedSolomonSyndromes checks the parity algebraically: a correct
// Reed-Solomon codeword is divisible by its generator, so evaluating it at
// every generator root α^i must yield zero. This catches a wrong generator,
// a wrong remainder loop, and bad GF(256) arithmetic without needing a
// reference vector.
func TestReedSolomonSyndromes(t *testing.T) {
	for _, n := range []int{10, 16, 18, 22, 24, 26} {
		data := make([]byte, 40)
		for i := range data {
			data[i] = byte(7*i + 3)
		}
		code := append(append([]byte(nil), data...), reedSolomon(data, n)...)
		for i := 0; i < n; i++ {
			// Horner evaluation of the codeword polynomial at α^i.
			var acc byte
			for _, c := range code {
				acc = gfMul(acc, gfExp[i]) ^ c
			}
			if acc != 0 {
				t.Fatalf("ecc=%d: syndrome at root %d is %d, want 0", n, i, acc)
			}
		}
	}
}

func TestGaloisFieldTables(t *testing.T) {
	for i := 1; i < 256; i++ {
		if got := gfExp[int(gfLog[byte(i)])]; got != byte(i) {
			t.Fatalf("gfExp[gfLog[%d]] = %d", i, got)
		}
	}
	if gfMul(3, 7) != 9 { // x+1 times x^2+x+1 = x^3+1 over GF(2)
		t.Fatalf("gfMul(3,7) = %d, want 9", gfMul(3, 7))
	}
}

// decoded is what readBack recovers from a rendered symbol.
type decoded struct {
	version int
	mask    int
	payload string
}

// readBack reverses Encode: it recovers the mask from the format information,
// un-masks the grid, re-reads the data modules in placement order,
// de-interleaves the blocks, and parses the byte-mode header. Anything wrong
// in masking, module placement, interleaving, or the format bits shows up
// here as a mismatch.
func readBack(t *testing.T, c *Code) decoded {
	t.Helper()
	version := (c.size - 17) / 4
	spec := specs[version]

	// Recover the format information from copy 1 and undo its BCH masking.
	var raw int
	read := func(i int, x, y int) {
		if c.Module(x, y) {
			raw |= 1 << uint(14-i)
		}
	}
	for i := 0; i < 6; i++ {
		read(i, 8, i)
	}
	read(6, 8, 7)
	read(7, 8, 8)
	read(8, 7, 8)
	for i := 9; i < 15; i++ {
		read(i, 14-i, 8)
	}
	unmasked := raw ^ 0x5412
	if got := bch(unmasked>>10, 0x537, 10); got != unmasked&0x3FF {
		t.Fatalf("format information fails its BCH check")
	}
	if lvl := unmasked >> 13; lvl != eccBits {
		t.Fatalf("format information says ecc level %02b, want %02b", lvl, eccBits)
	}
	mask := (unmasked >> 10) & 0b111

	// Rebuild the reserved-module map and undo the mask.
	work := &Code{size: c.size, mods: append([]bool(nil), c.mods...)}
	fn := make([]bool, c.size*c.size)
	(&Code{size: c.size, mods: make([]bool, c.size*c.size)}).drawFunctionPatterns(fn, version)
	work.applyMask(fn, mask)

	// Re-read the data modules in the same boustrophedon order.
	var bits []bool
	upward := true
	for x := work.size - 1; x > 0; x -= 2 {
		if x == 6 {
			x = 5
		}
		for k := 0; k < work.size; k++ {
			y := k
			if upward {
				y = work.size - 1 - k
			}
			for _, xx := range [2]int{x, x - 1} {
				if !fn[y*work.size+xx] {
					bits = append(bits, work.mods[y*work.size+xx])
				}
			}
		}
		upward = !upward
	}
	words := make([]byte, len(bits)/8)
	for i := range words {
		for j := 0; j < 8; j++ {
			if bits[i*8+j] {
				words[i] |= 1 << uint(7-j)
			}
		}
	}

	// De-interleave: data codewords first, column-major across the blocks.
	sizes := make([]int, 0, spec.totalBlocks())
	for i := 0; i < spec.blocks1; i++ {
		sizes = append(sizes, spec.dataPer1)
	}
	for i := 0; i < spec.blocks2; i++ {
		sizes = append(sizes, spec.dataPer2)
	}
	blocks := make([][]byte, len(sizes))
	pos := 0
	for i := 0; i < max(spec.dataPer1, spec.dataPer2); i++ {
		for b, n := range sizes {
			if i < n {
				blocks[b] = append(blocks[b], words[pos])
				pos++
			}
		}
	}
	var data []byte
	for _, b := range blocks {
		data = append(data, b...)
	}

	// Parse the byte-mode header.
	var br bitReader
	br.buf = data
	if mode := br.read(4); mode != 0b0100 {
		t.Fatalf("mode indicator = %04b, want 0100", mode)
	}
	n := br.read(charCountBits(version))
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(br.read(8))
	}
	return decoded{version: version, mask: mask, payload: string(payload)}
}

type bitReader struct {
	buf []byte
	pos int
}

func (r *bitReader) read(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v <<= 1
		if r.buf[r.pos/8]&(1<<uint(7-r.pos%8)) != 0 {
			v |= 1
		}
		r.pos++
	}
	return v
}

func TestEncodeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"short", "HELLO"},
		{"otpauth typical", "otpauth://totp/Steward:admin?secret=JBSWY3DPEHPK3PXPJBSWY3DP&issuer=Steward"},
		{"otpauth long brand", "otpauth://totp/Dinas%20Komunikasi%20dan%20Informatika%20Jawa%20Timur:reporter01?secret=MFRGGZDFMZTWQ2LKNNWG23TPOA&issuer=Dinas%20Komunikasi%20dan%20Informatika%20Jawa%20Timur"},
		{"single byte", "x"},
		{"high bytes", "\x00\xff\xfe\x01binary\xc3\xa9"},
		{"version boundary 16", strings.Repeat("a", 16)},
		{"version boundary 17", strings.Repeat("a", 17)},
		{"version boundary 28", strings.Repeat("a", 28)},
		{"version boundary 29", strings.Repeat("a", 29)},
		{"max v10", strings.Repeat("z", 213)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Encode(tc.in)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got := readBack(t, c)
			if got.payload != tc.in {
				t.Fatalf("round trip mismatch:\n got %q\nwant %q", got.payload, tc.in)
			}
			if got.mask < 0 || got.mask > 7 {
				t.Fatalf("mask out of range: %d", got.mask)
			}
		})
	}
}

// TestEncodePicksSmallestVersion pins the capacity arithmetic: 17 bytes no
// longer fits version 1 level M (16 data codewords, one consumed by the mode
// and length header).
func TestEncodePicksSmallestVersion(t *testing.T) {
	for _, tc := range []struct{ n, want int }{
		{14, 1}, {17, 2}, {26, 2}, {27, 3}, {213, 10},
	} {
		c, err := Encode(strings.Repeat("a", tc.n))
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", tc.n, err)
		}
		if got := (c.Size() - 17) / 4; got != tc.want {
			t.Errorf("%d bytes: version %d, want %d", tc.n, got, tc.want)
		}
	}
}

func TestEncodeTooLong(t *testing.T) {
	// Version 10 level M carries 216 data codewords; the four-bit mode
	// indicator and sixteen-bit character count leave room for 213 bytes.
	if _, err := Encode(strings.Repeat("a", 213)); err != nil {
		t.Fatalf("213 bytes should fit version 10: %v", err)
	}
	if _, err := Encode(strings.Repeat("a", 214)); err == nil {
		t.Fatal("expected an error for data beyond version 10")
	}
}

// TestFunctionPatterns checks the fixed structures a scanner locks onto.
func TestFunctionPatterns(t *testing.T) {
	c, err := Encode("otpauth://totp/Steward:admin?secret=JBSWY3DPEHPK3PXP&issuer=Steward")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	n := c.Size()
	for _, o := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 7; dx++ {
				ring := max(abs(dx-3), abs(dy-3))
				if got, want := c.Module(o[0]+dx, o[1]+dy), ring != 2; got != want {
					t.Fatalf("finder at %v offset (%d,%d): got %v want %v", o, dx, dy, got, want)
				}
			}
		}
	}
	for i := 8; i < n-8; i++ {
		if c.Module(i, 6) != (i%2 == 0) {
			t.Fatalf("horizontal timing broken at %d", i)
		}
		if c.Module(6, i) != (i%2 == 0) {
			t.Fatalf("vertical timing broken at %d", i)
		}
	}
	if !c.Module(8, n-8) {
		t.Fatal("dark module is not set")
	}
	// The separator ring around the top-left finder must be light.
	for i := 0; i < 8; i++ {
		if c.Module(i, 7) || c.Module(7, i) {
			t.Fatalf("separator not light at %d", i)
		}
	}
}

func TestSVGIsSelfContained(t *testing.T) {
	c, err := Encode("HELLO")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	svg := c.SVG(4)
	for _, bad := range []string{"<script", "href", "url(", "onload"} {
		if strings.Contains(strings.ToLower(svg), bad) {
			t.Errorf("SVG contains %q", bad)
		}
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Error("SVG is not a single element")
	}
	want := c.Size() + 8
	if !strings.Contains(svg, "viewBox=\"0 0 "+itoa(want)+" "+itoa(want)+"\"") {
		t.Errorf("viewBox does not account for the quiet zone: %s", svg[:80])
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestModuleOutOfRangeIsLight(t *testing.T) {
	c, err := Encode("HELLO")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {c.Size(), 0}, {0, c.Size()}} {
		if c.Module(p[0], p[1]) {
			t.Errorf("Module%v should be light", p)
		}
	}
}
