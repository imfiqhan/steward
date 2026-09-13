// Package suggest builds the part of an error message that says what would
// have been right: the nearest valid name, and the set to choose from.
//
// The output is shaped for a reader that has to act on it without opening the
// source — a person scanning a boot failure, or an agent reading stderr:
//
//	did you mean: title
//	available: author_id, body, cover, created_at ... and 9 more
package suggest

import (
	"sort"
	"strings"
)

const (
	// maxDistance is how wrong a name may be and still be offered back. Past
	// two edits a suggestion is a guess, and a wrong guess sends a reader to
	// change the one thing that was right.
	maxDistance = 2

	// maxList bounds the "available" line. The full set can be thousands —
	// every Lucide icon, every column of a wide model — and a line that long
	// is not read by anyone.
	maxList = 15
)

// Nearest returns the candidate closest to input, or "" when none is within
// two edits. Matching ignores case, so a name that is right but capitalised
// wrongly is offered back rather than missed.
//
// Ties go to the lexicographically smallest candidate, so the same mistake
// produces the same message every time.
func Nearest(input string, candidates []string) string {
	want := strings.ToLower(strings.TrimSpace(input))
	if want == "" {
		return ""
	}
	best, bestDist := "", maxDistance+1
	for _, c := range candidates {
		d := distance(want, strings.ToLower(c))
		if d > maxDistance {
			continue
		}
		if d < bestDist || (d == bestDist && c < best) {
			best, bestDist = c, d
		}
	}
	return best
}

// List renders candidates for an "available" line: sorted, so the same set
// reads the same way every run, and capped.
func List(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	if len(sorted) <= maxList {
		return strings.Join(sorted, ", ")
	}
	return strings.Join(sorted[:maxList], ", ") +
		" ... and " + itoa(len(sorted)-maxList) + " more"
}

// Block renders the indented lines that follow an error's first line, or ""
// when there is nothing to say. Append it to the first line directly:
//
//	fmt.Errorf("unknown disk %q%s", name, suggest.Block(name, disks))
func Block(input string, candidates []string) string {
	var b strings.Builder
	if near := Nearest(input, candidates); near != "" {
		b.WriteString("\n  did you mean: ")
		b.WriteString(near)
	}
	if list := List(candidates); list != "" {
		b.WriteString("\n  available: ")
		b.WriteString(list)
	}
	return b.String()
}

// distance is Levenshtein, counting an insertion, a deletion or a substitution
// as one edit, over runes rather than bytes.
func distance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
