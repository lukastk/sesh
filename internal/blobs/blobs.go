// Package blobs is sesh's content-addressed file store: arbitrary files (images,
// logs, anything) referenced from prompts by a stable token. The store is purely
// filesystem under <SESH_HOME>/blobs/<sha256>/<name> — the hash dir is the content
// address; the file keeps its original name so an agent that reads the path sees a
// real filename (right extension). No DB, no schema: the filesystem IS the store.
//
// A prompt references a blob with the token  @blob(<hex-prefix>)  — a prefix of the
// content hash, stable across machines (same bytes ⇒ same hash ⇒ same prefix), and
// resolved by prefix like every other sesh id. Expansion replaces each token with
// the blob's absolute path; a token that resolves to no blob (or an ambiguous
// prefix) is a LOUD error, never a silent passthrough. The literal text "@blob(…)"
// is written by doubling the sigil: "@@blob(…)" expands to "@blob(…)" unsubstituted.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TokenPrefixLen is how many hex chars of the content hash `blob add` bakes into
// the token. 12 hex = 48 bits — short enough to live in a prompt, collision-safe
// for a personal store, and a stable prefix of the full hash.
const TokenPrefixLen = 12

// Store is a content-addressed blob store rooted at one directory.
type Store struct{ dir string }

// Blob is one stored file's metadata.
type Blob struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// New returns the store for a SESH_HOME (the blobs/ subdir; created lazily on Add).
func New(home string) *Store { return &Store{dir: filepath.Join(home, "blobs")} }

// Dir is the store root (<home>/blobs).
func (s *Store) Dir() string { return s.dir }

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Add stores data under <hash>/<name>, returning the full content hash. It is
// idempotent: identical bytes always land in the same hash dir (dedup), and a
// re-add keeps the first stored name. name defaults to "blob" if empty.
func (s *Store) Add(name string, data []byte) (string, error) {
	if name == "" {
		name = "blob"
	}
	name = filepath.Base(filepath.Clean(name)) // never let a name escape its hash dir
	if name == "." || name == "/" || name == ".." {
		name = "blob"
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(s.dir, hash)
	// Already present (any name): keep the existing one (dedup).
	if existing, err := os.ReadDir(dir); err == nil && len(existing) > 0 {
		return hash, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("blobs: mkdir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return "", fmt.Errorf("blobs: write: %w", err)
	}
	return hash, nil
}

// Resolve turns a full hash or an unambiguous hex prefix into the full hash. A
// prefix matching zero or more-than-one blob is a loud error.
func (s *Store) Resolve(hashOrPrefix string) (string, error) {
	hashOrPrefix = strings.ToLower(hashOrPrefix)
	if !isHex(hashOrPrefix) {
		return "", fmt.Errorf("blobs: %q is not a hex hash/prefix", hashOrPrefix)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("blobs: no blob matches %q (store is empty)", hashOrPrefix)
		}
		return "", err
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), hashOrPrefix) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("blobs: no blob matches %q", hashOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("blobs: %q is ambiguous (%d blobs match) — use more characters", hashOrPrefix, len(matches))
	}
}

// blobFile returns the single file inside a resolved hash dir (hash, name, path).
func (s *Store) blobFile(hashOrPrefix string) (hash, name, path string, err error) {
	hash, err = s.Resolve(hashOrPrefix)
	if err != nil {
		return "", "", "", err
	}
	dir := filepath.Join(s.dir, hash)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			return hash, e.Name(), filepath.Join(dir, e.Name()), nil
		}
	}
	return "", "", "", fmt.Errorf("blobs: %s has no file (corrupt store)", hash)
}

// Path returns the absolute on-disk path of a blob (the value a token expands to).
func (s *Store) Path(hashOrPrefix string) (string, error) {
	_, _, path, err := s.blobFile(hashOrPrefix)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// Bytes returns a blob's original name and content.
func (s *Store) Bytes(hashOrPrefix string) (name string, data []byte, err error) {
	_, name, path, err := s.blobFile(hashOrPrefix)
	if err != nil {
		return "", nil, err
	}
	data, err = os.ReadFile(path)
	return name, data, err
}

// List returns every stored blob (hash, name, size), sorted by name then hash.
func (s *Store) List() ([]Blob, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Blob
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, name, path, err := s.blobFile(e.Name())
		if err != nil {
			continue // skip a corrupt/empty dir
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		out = append(out, Blob{Hash: e.Name(), Name: name, Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// Remove deletes a blob (its whole hash dir). Manual GC — no reference checking.
func (s *Store) Remove(hashOrPrefix string) error {
	hash, err := s.Resolve(hashOrPrefix)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, hash))
}

const sigil = "@blob("

// Expand replaces every @blob(<prefix>) token with the blob's absolute path. A
// token whose prefix resolves to no blob (or to more than one) is a LOUD error.
// The escape @@blob(<prefix>) emits the literal "@blob(<prefix>)" unsubstituted.
func (s *Store) Expand(text string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(text) {
		// Escape: @@blob(<hex>) -> literal @blob(<hex>), no expansion.
		if strings.HasPrefix(text[i:], "@"+sigil) {
			if hex, end, ok := parseToken(text, i+1); ok {
				b.WriteString(sigil + hex + ")")
				i = end
				continue
			}
		}
		// Token: @blob(<hex>) -> the resolved absolute path.
		if strings.HasPrefix(text[i:], sigil) {
			if hex, end, ok := parseToken(text, i); ok {
				path, err := s.Path(hex)
				if err != nil {
					return "", fmt.Errorf("prompt references unknown blob @blob(%s): %w", hex, err)
				}
				b.WriteString(path)
				i = end
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String(), nil
}

// parseToken matches "@blob(<hex>)" starting exactly at index start. It returns the
// hex body, the index just past the closing ')', and whether it matched.
func parseToken(text string, start int) (hex string, end int, ok bool) {
	if !strings.HasPrefix(text[start:], sigil) {
		return "", 0, false
	}
	j := start + len(sigil)
	k := j
	for k < len(text) && text[k] != ')' {
		k++
	}
	if k >= len(text) || k == j { // no closing paren, or empty body
		return "", 0, false
	}
	body := text[j:k]
	if !isHex(body) {
		return "", 0, false
	}
	return body, k + 1, true
}

// References returns the (deduped) hex prefixes every UNescaped @blob token in text
// refers to — what `ticket move` parses to know which blobs to carry along.
func References(text string) []string {
	var out []string
	seen := map[string]bool{}
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "@"+sigil) { // escaped — skip the whole token
			if _, end, ok := parseToken(text, i+1); ok {
				i = end
				continue
			}
		}
		if strings.HasPrefix(text[i:], sigil) {
			if hex, end, ok := parseToken(text, i); ok {
				if !seen[hex] {
					seen[hex] = true
					out = append(out, hex)
				}
				i = end
				continue
			}
		}
		i++
	}
	return out
}
