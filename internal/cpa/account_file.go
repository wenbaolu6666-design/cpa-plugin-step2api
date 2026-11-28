package cpa

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errInvalidAccountID = errors.New("invalid account id")

// Keep account IDs compatible with existing display names while requiring a
// single portable filename component on Unix and Windows.
func accountFilename(id string) (string, error) {
	if id == "" || id != strings.TrimSpace(id) || id == "." || id == ".." ||
		len(id) > 240 || !utf8.ValidString(id) || strings.ContainsAny(id, `/\:*?"<>|`) ||
		strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return "", errInvalidAccountID
	}
	name := "step2api-" + id + ".json"
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", errInvalidAccountID
	}
	return name, nil
}

// Open a directory-scoped handle before touching user-selected credentials.
func openAccountFile(oauthDir, id string) (*os.Root, string, error) {
	name, err := accountFilename(id)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(oauthDir)
	if err != nil {
		return nil, "", err
	}
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, "", errInvalidAccountID
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, "", err
	}
	return root, name, nil
}
