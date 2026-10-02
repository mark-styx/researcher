package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlob_RoundTripAndDedup(t *testing.T) {
	s := testStore(t)
	data := []byte("<html><body>hello</body></html>")
	sha, err := s.PutBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	if sha != HashBytes(data) {
		t.Errorf("sha = %s, want hash of the raw bytes", sha)
	}
	path := filepath.Join(s.Dir(), "blobs", "sha256", sha[:2], sha+".gz")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("blob not at %s: %v", path, err)
	}
	if again, err := s.PutBlob(data); err != nil || again != sha {
		t.Fatalf("second PutBlob = %s, %v", again, err)
	}
	if info2, _ := os.Stat(path); !info2.ModTime().Equal(info.ModTime()) {
		t.Error("second PutBlob rewrote the file")
	}
	got, err := s.ReadBlob(sha)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ReadBlob = %q, %v", got, err)
	}
}

func TestText_RoundTripAndMissing(t *testing.T) {
	s := testStore(t)
	sha, err := s.PutText("Paragraph one.\n\nParagraph two, ü.")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadText(sha)
	if err != nil || got != "Paragraph one.\n\nParagraph two, ü." {
		t.Fatalf("ReadText = %q, %v", got, err)
	}
	if _, err := s.ReadText(HashText("never stored")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing text: err = %v, want ErrNotFound", err)
	}
	if _, err := s.ReadBlob(HashText("never stored")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing blob: err = %v, want ErrNotFound", err)
	}
}

func TestContentPaths_RejectBadHashes(t *testing.T) {
	s := testStore(t)
	for _, sha := range []string{"", "../../etc/passwd", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := s.ReadText(sha); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("ReadText(%q) err = %v, want invalid hash", sha, err)
		}
		if _, _, err := s.ReadVector("m", sha); err == nil {
			t.Errorf("ReadVector(%q) accepted a bad hash", sha)
		}
	}
}

func TestVector_RoundTripPerModel(t *testing.T) {
	s := testStore(t)
	sha := HashText("passage")
	v := []float32{0.25, -1.5, 3e-7}
	if err := s.PutVector("nomic-embed-text:latest", sha, v); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ReadVector("nomic-embed-text:latest", sha)
	if err != nil || !ok || len(got) != 3 || got[0] != 0.25 || got[1] != -1.5 || got[2] != 3e-7 {
		t.Fatalf("ReadVector = %v, %v, %v", got, ok, err)
	}
	if _, ok, err := s.ReadVector("other-model", sha); ok || err != nil {
		t.Errorf("other model: ok=%v err=%v, want a miss", ok, err)
	}
	if err := s.PutVector("m", sha, nil); err == nil {
		t.Error("empty vector accepted")
	}
	if err := s.PutVector("../..", sha, v); err == nil {
		t.Error("model name made only of path characters accepted")
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "vectors", "nomic-embed-text_latest", sha[:2], sha+".f32")); err != nil {
		t.Errorf("vector file not under a sanitized model dir: %v", err)
	}
}

func TestReadVector_CorruptFile(t *testing.T) {
	s := testStore(t)
	sha := HashText("x")
	path, _ := s.vectorPath("m", sha)
	if err := putFile(path, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadVector("m", sha); err == nil {
		t.Error("3-byte vector file read without error")
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
