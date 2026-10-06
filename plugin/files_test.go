package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMatchFilesRecursiveWindowsAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "junit", "root.xml"),
		filepath.Join(root, "build", "junit", "nested.xml"),
		filepath.Join(root, "reports", "space name.xml"),
		filepath.Join(root, "reports", "ignore.txt"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := MatchFiles(root, []string{
		`**\junit\*.xml`,
		"reports/*.xml",
		"**/junit/root.xml",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{paths[1], paths[0], paths[2]}
	for index := range expected {
		expected[index], _ = filepath.Abs(expected[index])
	}
	if !reflect.DeepEqual(matches, expected) {
		t.Fatalf("matches:\n%q\nwant:\n%q", matches, expected)
	}
}

func TestGlobRegexRecursivePrefixCanMatchRoot(t *testing.T) {
	expression, err := globRegex("**/junit/*.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"junit/a.xml", "one/two/junit/b.xml"} {
		if !expression.MatchString(path) {
			t.Fatalf("expected %s to match", path)
		}
	}
}

func TestGlobRejectsEscapesAndAbsolutePaths(t *testing.T) {
	for _, pattern := range []string{"../secret.xml", "/tmp/*.xml"} {
		if _, err := globRegex(pattern); err == nil {
			t.Fatalf("expected %q to be rejected", pattern)
		}
	}
}
