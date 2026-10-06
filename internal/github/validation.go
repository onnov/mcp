package github

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxTextBytes = 512 << 10
const MaxCommitBytes = 2 << 20

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

func ValidateRepo(owner, repo string) error {
	for _, s := range []string{owner, repo} {
		if len(s) == 0 || len(s) > 100 || !namePattern.MatchString(s) || s == "." || s == ".." {
			return errors.New("owner and repo must be GitHub names, not URLs")
		}
	}
	return nil
}

func ValidateRef(s string) error {
	if s == "" || len(s) > 256 || s == "@" || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.ContainsAny(s, " ~^:?*[\\") || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errors.New("invalid branch, tag or SHA")
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errors.New("invalid ref segment")
		}
	}
	return nil
}

func ValidatePath(path string, allowRoot bool) error {
	if path == "" && allowRoot {
		return nil
	}
	if path == "" || len(path) > 2048 || !utf8.ValidString(path) || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return errors.New("invalid relative repository path")
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." {
			return errors.New("invalid repository path segment")
		}
	}
	return nil
}

func ValidateText(s string, limit int) error {
	if len(s) > limit || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return errors.New("text must be UTF-8 without NUL and within the documented size limit")
	}
	return nil
}

func ValidateSHA(s string) error {
	if !shaPattern.MatchString(s) {
		return errors.New("expected SHA must contain exactly 40 hexadecimal characters")
	}
	return nil
}

func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
