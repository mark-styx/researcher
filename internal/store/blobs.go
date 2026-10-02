package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Content-addressed files shared by every run, written once and never
// changed:
//
//	<store.dir>/blobs/sha256/<ab>/<hash>.gz       raw fetched bytes, gzipped
//	<store.dir>/text/sha256/<ab>/<hash>.txt       extracted text
//	<store.dir>/vectors/<model>/<ab>/<hash>.f32   a passage's embedding
//
// A blob's hash is of the raw bytes, a text's of the text. A vector is
// keyed by the hash of the passage text it embeds, so a rebuild finds it
// without calling the embedder again.

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrNotFound means the store has no file for a hash.
var ErrNotFound = errors.New("not in store")

// HashBytes is the hex SHA-256 the store files content under.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashText is HashBytes of a string.
func HashText(s string) string { return HashBytes([]byte(s)) }

func (s *Store) contentPath(kind, sha, ext string) (string, error) {
	if !sha256Hex.MatchString(sha) {
		return "", fmt.Errorf("invalid content hash %q", sha)
	}
	return filepath.Join(s.dir, kind, "sha256", sha[:2], sha+ext), nil
}

// PutBlob stores raw bytes gzipped and returns their hash. Storing bytes
// already there is a no-op.
func (s *Store) PutBlob(data []byte) (string, error) {
	sha := HashBytes(data)
	path, _ := s.contentPath("blobs", sha, ".gz")
	if exists(path) {
		return sha, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return "", fmt.Errorf("compressing blob: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("compressing blob: %w", err)
	}
	return sha, putFile(path, buf.Bytes())
}

// ReadBlob returns the raw bytes stored under sha.
func (s *Store) ReadBlob(sha string) ([]byte, error) {
	path, err := s.contentPath("blobs", sha, ".gz")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("blob %s: %w", sha, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", sha, err)
	}
	return io.ReadAll(zr)
}

// PutText stores extracted text and returns its hash.
func (s *Store) PutText(text string) (string, error) {
	sha := HashText(text)
	path, _ := s.contentPath("text", sha, ".txt")
	if exists(path) {
		return sha, nil
	}
	return sha, putFile(path, []byte(text))
}

// ReadText returns the text stored under sha.
func (s *Store) ReadText(sha string) (string, error) {
	path, err := s.contentPath("text", sha, ".txt")
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("text %s: %w", sha, ErrNotFound)
	}
	return string(b), err
}

var unsafeModelChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Store) vectorPath(model, textSHA string) (string, error) {
	if !sha256Hex.MatchString(textSHA) {
		return "", fmt.Errorf("invalid text hash %q", textSHA)
	}
	name := strings.Trim(unsafeModelChars.ReplaceAllString(model, "_"), "._")
	if name == "" {
		return "", fmt.Errorf("invalid embedding model %q", model)
	}
	return filepath.Join(s.dir, "vectors", name, textSHA[:2], textSHA+".f32"), nil
}

// PutVector caches the embedding model produced for the text hashed as
// textSHA, as little-endian float32s.
func (s *Store) PutVector(model, textSHA string, v []float32) error {
	path, err := s.vectorPath(model, textSHA)
	if err != nil {
		return err
	}
	if len(v) == 0 {
		return fmt.Errorf("empty vector for %s", textSHA)
	}
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return putFile(path, buf)
}

// ReadVector returns the cached embedding of textSHA under model. ok is
// false when there isn't one.
func (s *Store) ReadVector(model, textSHA string) (v []float32, ok bool, err error) {
	path, err := s.vectorPath(model, textSHA)
	if err != nil {
		return nil, false, err
	}
	buf, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(buf) == 0 || len(buf)%4 != 0 {
		return nil, false, fmt.Errorf("vector %s: %d bytes is not a float32 array", textSHA, len(buf))
	}
	v = make([]float32, len(buf)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return v, true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// putFile writes a content-addressed file atomically, creating its
// directory. Two writers of one hash write the same bytes, so the last
// rename winning is harmless.
func putFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	return writeAtomic(path, data)
}
