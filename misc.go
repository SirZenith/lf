package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Xuanwo/go-locale"
	"github.com/mattn/go-runewidth"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	localeStrDisable = ""  // disable locale ordering for this locale value
	localeStrSys     = "*" // replace this locale value with locale value read from environment
)

func isRoot(name string) bool { return filepath.Dir(name) == name }

func replaceTilde(s string) string {
	if strings.HasPrefix(s, "~") {
		s = strings.Replace(s, "~", gUser.HomeDir, 1)
	}
	return s
}

func runeSliceWidth(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += runewidth.RuneWidth(r)
	}
	return w
}

func runeSliceWidthRange(rs []rune, beg, end int) []rune {
	if beg == end {
		return []rune{}
	}

	curr := 0
	b := 0
	foundb := false
	for i, r := range rs {
		w := runewidth.RuneWidth(r)
		if curr >= beg && !foundb {
			b = i
			foundb = true
		}
		if curr == end || curr+w > end {
			return rs[b:i]
		}
		curr += w
	}

	return rs[b:]
}

// Returns the last runes of `rs` that take up at most `maxWidth` space.
func runeSliceWidthLastRange(rs []rune, maxWidth int) []rune {
	lastWidth := 0
	for i := len(rs) - 1; i >= 0; i-- {
		w := runewidth.RuneWidth(rs[i])
		if lastWidth+w > maxWidth {
			return rs[i+1:]
		}
		lastWidth += w
	}
	return rs
}

// This function is used to escape whitespaces and special characters with
// backlashes in a given string.
func escape(s string) string {
	buf := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || r == '\\' || r == ';' || r == '#' {
			buf = append(buf, '\\')
		}
		buf = append(buf, r)
	}
	return string(buf)
}

// This function is used to remove backlashes that are used to escape
// whitespaces and special characters in a given string.
func unescape(s string) string {
	esc := false
	buf := make([]rune, 0, len(s))
	for _, r := range s {
		if esc {
			if !unicode.IsSpace(r) && r != '\\' && r != ';' && r != '#' {
				buf = append(buf, '\\')
			}
			buf = append(buf, r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		esc = false
		buf = append(buf, r)
	}
	if esc {
		buf = append(buf, '\\')
	}
	return string(buf)
}

// This function splits the given string by whitespaces. It is aware of escaped
// whitespaces so that they are not split unintentionally.
func tokenize(s string) []string {
	esc := false
	var buf []rune
	var toks []string
	for _, r := range s {
		if r == '\\' {
			esc = true
			buf = append(buf, r)
			continue
		}
		if esc {
			esc = false
			buf = append(buf, r)
			continue
		}
		if !unicode.IsSpace(r) {
			buf = append(buf, r)
		} else {
			toks = append(toks, string(buf))
			buf = nil
		}
	}
	toks = append(toks, string(buf))
	return toks
}

// This function splits the first word of a string delimited by whitespace from
// the rest. This is used to tokenize a string one by one without touching the
// rest. Whitespace on the left side of both the word and the rest are trimmed.
func splitWord(s string) (word, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	ind := len(s)
	for i, c := range s {
		if unicode.IsSpace(c) {
			ind = i
			break
		}
	}
	word = s[0:ind]
	rest = strings.TrimLeftFunc(s[ind:], unicode.IsSpace)
	return
}

// This function reads whitespace separated string arrays at each line. Single
// or double quotes can be used to escape whitespaces. Hash characters can be
// used to add a comment until the end of line. Leading and trailing space is
// trimmed. Empty lines are skipped.
func readArrays(r io.Reader, min_cols, max_cols int) ([][]string, error) {
	var arrays [][]string
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := s.Text()

		squote, dquote := false, false
		for i := 0; i < len(line); i++ {
			if line[i] == '\'' && !dquote {
				squote = !squote
			} else if line[i] == '"' && !squote {
				dquote = !dquote
			}
			if !squote && !dquote && line[i] == '#' {
				line = line[:i]
				break
			}
		}

		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		squote, dquote = false, false
		arr := strings.FieldsFunc(line, func(r rune) bool {
			if r == '\'' && !dquote {
				squote = !squote
			} else if r == '"' && !squote {
				dquote = !dquote
			}
			return !squote && !dquote && unicode.IsSpace(r)
		})

		if len(arr) < min_cols || len(arr) > max_cols {
			if min_cols == max_cols {
				return nil, fmt.Errorf("expected %d columns but found: %s", min_cols, s.Text())
			}
			return nil, fmt.Errorf("expected %d~%d columns but found: %s", min_cols, max_cols, s.Text())
		}

		for i := 0; i < len(arr); i++ {
			squote, dquote = false, false
			buf := make([]rune, 0, len(arr[i]))
			for _, r := range arr[i] {
				if r == '\'' && !dquote {
					squote = !squote
					continue
				}
				if r == '"' && !squote {
					dquote = !dquote
					continue
				}
				buf = append(buf, r)
			}
			arr[i] = string(buf)
		}

		arrays = append(arrays, arr)
	}

	return arrays, nil
}

func readPairs(r io.Reader) ([][]string, error) {
	return readArrays(r, 2, 2)
}

// This function converts a size in bytes to a human readable form using metric
// suffixes (e.g. 1K = 1000). For values less than 10 the first significant
// digit is shown, otherwise it is hidden. Numbers are always rounded down.
// This should be fine for most human beings.
func humanize(size int64) string {
	if size < 1000 {
		return fmt.Sprintf("%dB", size)
	}

	suffix := []string{
		"K", // kilo
		"M", // mega
		"G", // giga
		"T", // tera
		"P", // peta
		"E", // exa
		"Z", // zeta
		"Y", // yotta
	}

	curr := float64(size) / 1000
	for _, s := range suffix {
		if curr < 10 {
			return fmt.Sprintf("%.1f%s", curr-0.0499, s)
		} else if curr < 1000 {
			return fmt.Sprintf("%d%s", int(curr), s)
		}
		curr /= 1000
	}

	return ""
}

// This function compares two strings for natural sorting which takes into
// account values of numbers in strings. For example, '2' is less than '10',
// and similarly 'foo2bar' is less than 'foo10bar', but 'bar2bar' is greater
// than 'foo10bar'.
func naturalLess(s1, s2 string) bool {
	lo1, lo2, hi1, hi2 := 0, 0, 0, 0
	for {
		if hi1 >= len(s1) {
			return hi2 != len(s2)
		}

		if hi2 >= len(s2) {
			return false
		}

		isDigit1 := isDigit(s1[hi1])
		isDigit2 := isDigit(s2[hi2])

		for lo1 = hi1; hi1 < len(s1) && isDigit(s1[hi1]) == isDigit1; hi1++ {
		}

		for lo2 = hi2; hi2 < len(s2) && isDigit(s2[hi2]) == isDigit2; hi2++ {
		}

		if s1[lo1:hi1] == s2[lo2:hi2] {
			continue
		}

		if isDigit1 && isDigit2 {
			num1, err1 := strconv.Atoi(s1[lo1:hi1])
			num2, err2 := strconv.Atoi(s2[lo2:hi2])

			if err1 == nil && err2 == nil {
				return num1 < num2
			}
		}

		return s1[lo1:hi1] < s2[lo2:hi2]
	}
}

// This function returns the extension of a file with a leading dot
// it returns an empty string if extension could not be determined
// i.e. directories, filenames without extensions
func getFileExtension(file fs.FileInfo) string {
	if file.IsDir() {
		return ""
	}
	if strings.Count(file.Name(), ".") == 1 && file.Name()[0] == '.' {
		// hidden file without extension
		return ""
	}
	return filepath.Ext(file.Name())
}

var (
	reModKey   = regexp.MustCompile(`<(c|s|a)-(.+)>`)
	reRulerSub = regexp.MustCompile(`%[apmcsfithd]|%\{[^}]+\}`)
)

var (
	reWord    = regexp.MustCompile(`(\pL|\pN)+`)
	reWordBeg = regexp.MustCompile(`([^\pL\pN]|^)(\pL|\pN)`)
	reWordEnd = regexp.MustCompile(`(\pL|\pN)([^\pL\pN]|$)`)
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// This function parses given locale string into language tag value. Passing empty
// string as locale means reading locale value from environment.
func getLocaleTag(localeStr string) (language.Tag, error) {
	localeTag := language.Und
	var err error

	if localeStr == localeStrSys {
		// read environment locale
		return locale.Detect()
	}

	localeTag, err = language.Parse(localeStr)
	if err != nil {
		return localeTag, fmt.Errorf("invalid locale %q: %s", localeStr, err)
	}

	return localeTag, nil
}

// This function creates new collator for given locale. Passing empty string as
// as locale means reading locale value from environment.
func makeCollator(localeStr string, o ...collate.Option) (*collate.Collator, error) {
	if localeStr == localeStrDisable {
		return nil, fmt.Errorf("locale suppose to be disabled with given string")
	}

	localeTag, err := getLocaleTag(localeStr)
	if err != nil {
		return nil, err
	}

	collator := collate.New(localeTag, o...)

	return collator, nil
}

// We don't need no generic code
// We don't need no type control
// No dark templates in compiler
// Haskell leave them kids alone
// Hey Bjarne leave them kids alone
// All in all it's just another brick in the code
// All in all you're just another brick in the code
//
// -- Pink Trolled --
