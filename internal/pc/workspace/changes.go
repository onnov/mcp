package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const maxUntrackedSummaryBytes = 8 << 20

type ChangeFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

type ChangeSummary struct {
	Base      string       `json:"base"`
	MergeBase string       `json:"merge_base"`
	Files     []ChangeFile `json:"files"`
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
}

func changeStatus(code string) string {
	switch code {
	case "A":
		return "created"
	case "D":
		return "deleted"
	case "M":
		return "modified"
	case "T":
		return "type_changed"
	default:
		return "modified"
	}
}

func splitNUL(s string) []string {
	parts := strings.Split(s, "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func (s *Service) untrackedLines(repo, path string) (int, bool, error) {
	r, err := s.open(repo)
	if err != nil {
		return 0, false, err
	}
	defer r.Close()
	f, err := r.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	if !st.Mode().IsRegular() {
		return 0, true, nil
	}
	buf := make([]byte, 32*1024)
	total, lines := 0, 0
	var last byte
	for {
		n, er := f.Read(buf)
		if n > 0 {
			total += n
			if total > maxUntrackedSummaryBytes {
				return 0, true, nil
			}
			for _, b := range buf[:n] {
				if b == 0 {
					return 0, true, nil
				}
				if b == '\n' {
					lines++
				}
				last = b
			}
		}
		if errors.Is(er, io.EOF) {
			break
		}
		if er != nil {
			return 0, false, er
		}
	}
	if total > 0 && last != '\n' {
		lines++
	}
	return lines, false, nil
}

// ChangeSummary reports branch changes from the merge-base with base through the
// current working tree. This includes committed branch work, staged/unstaged
// tracked changes and untracked files. It is a live read-only snapshot.
func (s *Service) ChangeSummary(ctx context.Context, t Target, base string) (ChangeSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := s.inspect(ctx, t.Directory)
	if err != nil {
		return ChangeSummary{}, err
	}
	if !info.Git {
		return ChangeSummary{}, errors.New("change summary requires a Git checkout")
	}
	if t.Branch == "" || info.Branch != t.Branch {
		return ChangeSummary{}, fmt.Errorf("current branch is %q; explicit branch %q does not match", info.Branch, t.Branch)
	}
	if base == "" {
		base = "main"
	}
	if len(base) > 256 || strings.HasPrefix(base, "-") || strings.ContainsAny(base, "\x00\r\n\t") {
		return ChangeSummary{}, errors.New("invalid base ref")
	}
	mergeBase, err := s.git(ctx, info.GitRoot, "merge-base", base, "HEAD")
	if err != nil {
		return ChangeSummary{}, fmt.Errorf("cannot resolve merge-base with %q: %w", base, err)
	}

	names, err := s.git(ctx, info.GitRoot, "diff", "--name-status", "-z", "--no-renames", mergeBase, "--")
	if err != nil {
		return ChangeSummary{}, err
	}
	numstat, err := s.git(ctx, info.GitRoot, "diff", "--numstat", "-z", "--no-renames", mergeBase, "--")
	if err != nil {
		return ChangeSummary{}, err
	}

	byPath := map[string]*ChangeFile{}
	nameParts := splitNUL(names)
	for i := 0; i+1 < len(nameParts); i += 2 {
		code, path := nameParts[i], nameParts[i+1]
		if path == "" {
			continue
		}
		byPath[path] = &ChangeFile{Path: path, Status: changeStatus(code)}
	}
	for _, row := range splitNUL(numstat) {
		fields := strings.SplitN(row, "\t", 3)
		if len(fields) != 3 || fields[2] == "" {
			continue
		}
		f := byPath[fields[2]]
		if f == nil {
			f = &ChangeFile{Path: fields[2], Status: "modified"}
			byPath[fields[2]] = f
		}
		if fields[0] == "-" || fields[1] == "-" {
			f.Binary = true
			continue
		}
		f.Additions, _ = strconv.Atoi(fields[0])
		f.Deletions, _ = strconv.Atoi(fields[1])
	}

	untracked, err := s.git(ctx, info.GitRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ChangeSummary{}, err
	}
	for _, path := range splitNUL(untracked) {
		if path == "" || byPath[path] != nil {
			continue
		}
		additions, binary, er := s.untrackedLines(info.GitRoot, path)
		if er != nil {
			return ChangeSummary{}, fmt.Errorf("count untracked %q: %w", path, er)
		}
		byPath[path] = &ChangeFile{Path: path, Status: "created", Additions: additions, Binary: binary}
	}

	out := ChangeSummary{Base: base, MergeBase: mergeBase, Files: make([]ChangeFile, 0, len(byPath))}
	for _, f := range byPath {
		out.Files = append(out.Files, *f)
		out.Additions += f.Additions
		out.Deletions += f.Deletions
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil
}
