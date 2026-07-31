// Package qr implements a minimal QR Code encoder: byte mode, error
// correction level M, versions 1 through 10.
//
// That is everything Steward needs to draw an otpauth:// enrolment code — a
// URI of roughly 60 to 200 characters — and small enough to keep the
// framework's dependency list unchanged. Format and version information bits
// are computed from their BCH generators rather than tabulated, so there are
// no transcription errors to find.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// Code is an encoded symbol: a square grid of dark/light modules.
type Code struct {
	size int
	mods []bool // row-major, len == size*size
}

// Size returns the symbol's width in modules, excluding the quiet zone.
func (c *Code) Size() int { return c.size }

// Module reports whether the module at column x, row y is dark. Coordinates
// outside the symbol are light.
func (c *Code) Module(x, y int) bool {
	if x < 0 || y < 0 || x >= c.size || y >= c.size {
		return false
	}
	return c.mods[y*c.size+x]
}

// ErrTooLong reports data that will not fit in a version-10 level-M symbol.
var ErrTooLong = errors.New("qr: data too long")

// eccLevel is fixed at M ("medium", ~15% recovery) — the level authenticator
// apps are drawn against and the usual default.
const eccBits = 0b00 // level M's two-bit indicator in the format information

// versionSpec describes one version's level-M block structure. Group 2 blocks
// hold exactly one more data codeword than group 1; a zero count means the
// version has only one group.
type versionSpec struct {
	eccPerBlock int
	blocks1     int
	dataPer1    int
	blocks2     int
	dataPer2    int
}

// specs is indexed by version, so entry 0 is unused padding.
var specs = [11]versionSpec{
	{},
	{eccPerBlock: 10, blocks1: 1, dataPer1: 16},
	{eccPerBlock: 16, blocks1: 1, dataPer1: 28},
	{eccPerBlock: 26, blocks1: 1, dataPer1: 44},
	{eccPerBlock: 18, blocks1: 2, dataPer1: 32},
	{eccPerBlock: 24, blocks1: 2, dataPer1: 43},
	{eccPerBlock: 16, blocks1: 4, dataPer1: 27},
	{eccPerBlock: 18, blocks1: 4, dataPer1: 31},
	{eccPerBlock: 22, blocks1: 2, dataPer1: 38, blocks2: 2, dataPer2: 39},
	{eccPerBlock: 22, blocks1: 3, dataPer1: 36, blocks2: 2, dataPer2: 37},
	{eccPerBlock: 26, blocks1: 4, dataPer1: 43, blocks2: 1, dataPer2: 44},
}

// alignmentCenters lists the row/column centres of a version's alignment
// patterns; version 1 has none.
var alignmentCenters = [11][]int{
	{}, {},
	{6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34},
	{6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
}

// dataCodewords totals the version's data capacity in codewords.
func (s versionSpec) dataCodewords() int {
	return s.blocks1*s.dataPer1 + s.blocks2*s.dataPer2
}

func (s versionSpec) totalBlocks() int { return s.blocks1 + s.blocks2 }

// charCountBits is the length of byte mode's character-count indicator: 8 bits
// up to version 9, 16 from version 10.
func charCountBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

// Encode encodes data as a byte-mode, level-M QR symbol in the smallest
// version that fits.
func Encode(data string) (*Code, error) {
	b := []byte(data)
	version := 0
	for v := 1; v <= 10; v++ {
		need := 4 + charCountBits(v) + 8*len(b)
		if need <= 8*specs[v].dataCodewords() {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("%w: %d bytes exceeds version 10 level M", ErrTooLong, len(b))
	}
	spec := specs[version]

	// ---- bit stream: mode, length, payload, terminator, padding -------------
	var bits bitBuffer
	bits.append(0b0100, 4) // byte mode
	bits.append(uint(len(b)), charCountBits(version))
	for _, by := range b {
		bits.append(uint(by), 8)
	}
	capacity := 8 * spec.dataCodewords()
	// Terminator: up to four zero bits, truncated against the capacity.
	bits.append(0, min(4, capacity-bits.len()))
	for bits.len()%8 != 0 {
		bits.append(0, 1)
	}
	// Pad codewords alternate 0xEC / 0x11 until the version is full.
	for pad := byte(0xEC); bits.len() < capacity; pad ^= 0xEC ^ 0x11 {
		bits.append(uint(pad), 8)
	}
	dataWords := bits.bytes()

	// ---- split into blocks, add Reed-Solomon parity, interleave -------------
	blocks := make([][]byte, 0, spec.totalBlocks())
	ecc := make([][]byte, 0, spec.totalBlocks())
	off := 0
	appendBlocks := func(count, per int) {
		for i := 0; i < count; i++ {
			blk := dataWords[off : off+per]
			off += per
			blocks = append(blocks, blk)
			ecc = append(ecc, reedSolomon(blk, spec.eccPerBlock))
		}
	}
	appendBlocks(spec.blocks1, spec.dataPer1)
	appendBlocks(spec.blocks2, spec.dataPer2)

	final := make([]byte, 0, spec.dataCodewords()+spec.eccPerBlock*spec.totalBlocks())
	maxData := max(spec.dataPer1, spec.dataPer2)
	for i := 0; i < maxData; i++ {
		for _, blk := range blocks {
			if i < len(blk) {
				final = append(final, blk[i])
			}
		}
	}
	for i := 0; i < spec.eccPerBlock; i++ {
		for _, e := range ecc {
			final = append(final, e[i])
		}
	}

	// ---- draw: function patterns, then data, then the best mask ------------
	size := 17 + 4*version
	c := &Code{size: size, mods: make([]bool, size*size)}
	fn := make([]bool, size*size) // reserved (non-data) modules
	c.drawFunctionPatterns(fn, version)
	c.placeData(fn, final)

	best, bestPenalty := 0, -1
	for mask := 0; mask < 8; mask++ {
		c.applyMask(fn, mask)
		c.drawFormatInfo(mask)
		p := c.penalty()
		if bestPenalty < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
		c.applyMask(fn, mask) // XOR is its own inverse — restore
	}
	c.applyMask(fn, best)
	c.drawFormatInfo(best)
	if version >= 7 {
		c.drawVersionInfo(version)
	}
	return c, nil
}

// ---- bit buffer -------------------------------------------------------------

type bitBuffer struct {
	buf  []byte
	nbit int
}

func (b *bitBuffer) len() int { return b.nbit }

// append writes the low n bits of v, most significant first.
func (b *bitBuffer) append(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		if b.nbit%8 == 0 {
			b.buf = append(b.buf, 0)
		}
		if v&(1<<uint(i)) != 0 {
			b.buf[b.nbit/8] |= 1 << uint(7-b.nbit%8)
		}
		b.nbit++
	}
}

func (b *bitBuffer) bytes() []byte { return b.buf }

// ---- Reed-Solomon over GF(256), primitive polynomial 0x11D -----------------

var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	// Duplicate the cycle so exponent sums up to 508 need no modulo.
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGenerator returns the generator polynomial of degree n, coefficients
// highest-order first.
func rsGenerator(n int) []byte {
	g := []byte{1}
	for i := 0; i < n; i++ {
		// Multiply g by (x - α^i); in GF(2^m) subtraction is XOR.
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		g = next
	}
	return g
}

// reedSolomon returns n parity codewords for data.
func reedSolomon(data []byte, n int) []byte {
	g := rsGenerator(n)
	rem := make([]byte, n)
	for _, d := range data {
		factor := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for i, gc := range g[1:] {
			rem[i] ^= gfMul(gc, factor)
		}
	}
	return rem
}

// ---- module drawing ---------------------------------------------------------

func (c *Code) set(x, y int, dark bool) {
	if x < 0 || y < 0 || x >= c.size || y >= c.size {
		return
	}
	c.mods[y*c.size+x] = dark
}

func (c *Code) reserve(fn []bool, x, y int) {
	if x < 0 || y < 0 || x >= c.size || y >= c.size {
		return
	}
	fn[y*c.size+x] = true
}

func (c *Code) drawFunctionPatterns(fn []bool, version int) {
	// Finder patterns and their separators, at three corners.
	for _, p := range [][2]int{{0, 0}, {c.size - 7, 0}, {0, c.size - 7}} {
		c.drawFinder(fn, p[0], p[1])
	}
	// Timing patterns along row and column 6.
	for i := 8; i < c.size-8; i++ {
		dark := i%2 == 0
		c.set(i, 6, dark)
		c.reserve(fn, i, 6)
		c.set(6, i, dark)
		c.reserve(fn, 6, i)
	}
	// Alignment patterns at every centre pair, except where a finder sits.
	centers := alignmentCenters[version]
	for _, cy := range centers {
		for _, cx := range centers {
			if (cx == 6 && cy == 6) ||
				(cx == 6 && cy == c.size-7) ||
				(cx == c.size-7 && cy == 6) {
				continue
			}
			c.drawAlignment(fn, cx, cy)
		}
	}
	// The dark module, always set.
	c.set(8, c.size-8, true)
	c.reserve(fn, 8, c.size-8)
	// Format information areas, written later but reserved now.
	for i := 0; i < 9; i++ {
		c.reserve(fn, i, 8)
		c.reserve(fn, 8, i)
	}
	for i := 0; i < 8; i++ {
		c.reserve(fn, c.size-1-i, 8)
		c.reserve(fn, 8, c.size-1-i)
	}
	// Version information areas (version 7 and up).
	if version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				c.reserve(fn, c.size-11+j, i)
				c.reserve(fn, i, c.size-11+j)
			}
		}
	}
}

func (c *Code) drawFinder(fn []bool, ox, oy int) {
	// The 7x7 pattern plus a one-module light separator all around.
	for dy := -1; dy <= 7; dy++ {
		for dx := -1; dx <= 7; dx++ {
			x, y := ox+dx, oy+dy
			if x < 0 || y < 0 || x >= c.size || y >= c.size {
				continue
			}
			inner := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6
			dark := false
			if inner {
				ring := max(abs(dx-3), abs(dy-3))
				dark = ring != 2 // rings 0,1,3 dark; ring 2 light
			}
			c.set(x, y, dark)
			c.reserve(fn, x, y)
		}
	}
}

func (c *Code) drawAlignment(fn []bool, cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			ring := max(abs(dx), abs(dy))
			c.set(cx+dx, cy+dy, ring != 1) // rings 0 and 2 dark
			c.reserve(fn, cx+dx, cy+dy)
		}
	}
}

// placeData fills the non-function modules in the standard two-column
// boustrophedon order, starting bottom-right.
func (c *Code) placeData(fn []bool, words []byte) {
	bit := 0
	next := func() bool {
		if bit >= 8*len(words) {
			return false // remainder bits are light
		}
		b := words[bit/8]&(1<<uint(7-bit%8)) != 0
		bit++
		return b
	}
	upward := true
	for x := c.size - 1; x > 0; x -= 2 {
		if x == 6 {
			x = 5 // the timing column is not part of any pair
		}
		for k := 0; k < c.size; k++ {
			y := k
			if upward {
				y = c.size - 1 - k
			}
			for _, xx := range [2]int{x, x - 1} {
				if !fn[y*c.size+xx] {
					c.mods[y*c.size+xx] = next()
				}
			}
		}
		upward = !upward
	}
}

// maskAt reports whether the mask pattern inverts the module at x, y.
func maskAt(mask, x, y int) bool {
	switch mask {
	case 0:
		return (y+x)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (y+x)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (y*x)%2+(y*x)%3 == 0
	case 6:
		return ((y*x)%2+(y*x)%3)%2 == 0
	default:
		return ((y+x)%2+(y*x)%3)%2 == 0
	}
}

// applyMask XORs the mask over every data module; calling it twice restores
// the original.
func (c *Code) applyMask(fn []bool, mask int) {
	for y := 0; y < c.size; y++ {
		for x := 0; x < c.size; x++ {
			if !fn[y*c.size+x] && maskAt(mask, x, y) {
				c.mods[y*c.size+x] = !c.mods[y*c.size+x]
			}
		}
	}
}

// bch computes the BCH check bits for val: the remainder of val shifted left
// by deg, divided by gen.
func bch(val, gen, deg int) int {
	v := val << deg
	genBits := bitLen(gen)
	for bitLen(v) >= genBits {
		v ^= gen << (bitLen(v) - genBits)
	}
	return v
}

func bitLen(v int) int {
	n := 0
	for ; v != 0; v >>= 1 {
		n++
	}
	return n
}

// drawFormatInfo writes the 15-bit format information into both copies.
func (c *Code) drawFormatInfo(mask int) {
	data := eccBits<<3 | mask
	bits := (data<<10 | bch(data, 0x537, 10)) ^ 0x5412

	get := func(i int) bool { return bits&(1<<uint(14-i)) != 0 }

	// Copy 1: around the top-left finder, skipping the timing row/column.
	for i := 0; i < 6; i++ {
		c.set(8, i, get(i))
	}
	c.set(8, 7, get(6))
	c.set(8, 8, get(7))
	c.set(7, 8, get(8))
	for i := 9; i < 15; i++ {
		c.set(14-i, 8, get(i))
	}
	// Copy 2: split beneath the top-right and beside the bottom-left finders.
	for i := 0; i < 8; i++ {
		c.set(c.size-1-i, 8, get(i))
	}
	for i := 8; i < 15; i++ {
		c.set(8, c.size-15+i, get(i))
	}
}

// drawVersionInfo writes the 18-bit version information (version 7 and up).
func (c *Code) drawVersionInfo(version int) {
	bits := version<<12 | bch(version, 0x1F25, 12)
	for i := 0; i < 18; i++ {
		on := bits&(1<<uint(i)) != 0
		x, y := i/3, i%3
		c.set(y+c.size-11, x, on)
		c.set(x, y+c.size-11, on)
	}
}

// ---- mask penalty scoring ---------------------------------------------------

// penalty scores the current grid by the four standard rules; lower is
// better. Mask choice is a legibility optimisation, not a correctness one, but
// a bad mask genuinely defeats some scanners.
func (c *Code) penalty() int {
	total := c.penaltyRuns() + c.penaltyBlocks() + c.penaltyFinderLike() + c.penaltyBalance()
	return total
}

// penaltyRuns charges runs of five or more same-coloured modules.
func (c *Code) penaltyRuns() int {
	score := 0
	charge := func(run int) {
		if run >= 5 {
			score += 3 + (run - 5)
		}
	}
	for i := 0; i < c.size; i++ {
		runH, runV := 1, 1
		for j := 1; j < c.size; j++ {
			if c.Module(j, i) == c.Module(j-1, i) {
				runH++
			} else {
				charge(runH)
				runH = 1
			}
			if c.Module(i, j) == c.Module(i, j-1) {
				runV++
			} else {
				charge(runV)
				runV = 1
			}
		}
		charge(runH)
		charge(runV)
	}
	return score
}

// penaltyBlocks charges every 2x2 block of one colour.
func (c *Code) penaltyBlocks() int {
	score := 0
	for y := 0; y < c.size-1; y++ {
		for x := 0; x < c.size-1; x++ {
			v := c.Module(x, y)
			if c.Module(x+1, y) == v && c.Module(x, y+1) == v && c.Module(x+1, y+1) == v {
				score += 3
			}
		}
	}
	return score
}

// penaltyFinderLike charges 1:1:3:1:1 sequences with four light modules on
// either side — the pattern a scanner mistakes for a finder.
func (c *Code) penaltyFinderLike() int {
	pattern := [7]bool{true, false, true, true, true, false, true}
	score := 0
	match := func(get func(int) bool, start int) bool {
		for i, want := range pattern {
			if get(start+i) != want {
				return false
			}
		}
		// Four light modules must precede or follow the sequence.
		clear := func(from, n int) bool {
			for i := 0; i < n; i++ {
				if get(from + i) {
					return false
				}
			}
			return true
		}
		return clear(start-4, 4) || clear(start+7, 4)
	}
	for i := 0; i < c.size; i++ {
		row := func(j int) bool { return c.Module(j, i) }
		col := func(j int) bool { return c.Module(i, j) }
		for j := 0; j <= c.size-7; j++ {
			if match(row, j) {
				score += 40
			}
			if match(col, j) {
				score += 40
			}
		}
	}
	return score
}

// penaltyBalance charges deviation from an even split of dark and light.
func (c *Code) penaltyBalance() int {
	dark := 0
	for _, m := range c.mods {
		if m {
			dark++
		}
	}
	percent := dark * 100 / len(c.mods)
	return 10 * (abs(percent-50) / 5)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ---- rendering --------------------------------------------------------------

// SVG renders the symbol as a standalone <svg> element: one path of dark
// modules over a light background, scaled to a viewBox so CSS sizes it. quiet
// is the quiet-zone width in modules (4 is the specified minimum).
//
// The output is deliberately attribute-only — no scripts, no external
// references — so it is safe to inline into a page with a strict CSP.
func (c *Code) SVG(quiet int) string {
	if quiet < 0 {
		quiet = 0
	}
	dim := c.size + 2*quiet
	var path strings.Builder
	for y := 0; y < c.size; y++ {
		for x := 0; x < c.size; x++ {
			if c.Module(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`,
		dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, dim, dim)
	fmt.Fprintf(&b, `<path fill="#000" d="%s"/>`, path.String())
	b.WriteString(`</svg>`)
	return b.String()
}
