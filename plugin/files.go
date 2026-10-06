package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxScannedFiles = 100000

func MatchFiles(root string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, errors.New("at least one result pattern is required")
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		expression, err := globRegex(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, expression)
	}

	matched := map[string]struct{}{}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		scanned++
		if scanned > maxScannedFiles {
			return fmt.Errorf("workspace contains more than %d files", maxScannedFiles)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		for _, expression := range compiled {
			if expression.MatchString(relative) {
				absolute, err := filepath.Abs(path)
				if err != nil {
					return err
				}
				matched[filepath.Clean(absolute)] = struct{}{}
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("match result files: %w", err)
	}

	result := make([]string, 0, len(matched))
	for path := range matched {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func globRegex(pattern string) (*regexp.Regexp, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	normalized = strings.TrimPrefix(normalized, "./")
	if normalized == "" {
		return nil, errors.New("result pattern cannot be empty")
	}
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("result pattern must be workspace-relative: %q", pattern)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return nil, fmt.Errorf("result pattern cannot escape the workspace: %q", pattern)
		}
	}

	var result strings.Builder
	result.WriteString("^")
	for index := 0; index < len(normalized); {
		switch normalized[index] {
		case '*':
			if index+1 < len(normalized) && normalized[index+1] == '*' {
				index += 2
				if index < len(normalized) && normalized[index] == '/' {
					result.WriteString("(?:.*/)?")
					index++
				} else {
					result.WriteString(".*")
				}
			} else {
				result.WriteString("[^/]*")
				index++
			}
		case '?':
			result.WriteString("[^/]")
			index++
		default:
			result.WriteString(regexp.QuoteMeta(string(normalized[index])))
			index++
		}
	}
	result.WriteString("$")
	expression, err := regexp.Compile(result.String())
	if err != nil {
		return nil, fmt.Errorf("invalid result pattern %q: %w", pattern, err)
	}
	return expression, nil
}
