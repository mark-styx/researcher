package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtraction_RoundTripAndList(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := HashText("some text")
	if _, ok, err := st.ReadExtraction("qwen3.5:9b/claims-v1", sha); ok || err != nil {
		t.Fatalf("before writing: ok %v, err %v", ok, err)
	}
	e := Extraction{TextSHA: sha, Extractor: "qwen3.5:9b/claims-v1", Model: "qwen3.5:9b", ExtractedAt: time.Now().UTC().Truncate(time.Second),
		Chunks: 2, Attempts: 1, Claims: []ExtractedClaim{{Text: "A is B.", Quote: "A is B", Volatile: true, Chunk: 0}},
		Failed: []ChunkError{{Chunk: 1, Error: "bad json"}}}
	if err := st.PutExtraction(e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.ReadExtraction(e.Extractor, sha)
	if err != nil || !ok || got.Claims[0] != e.Claims[0] || got.Complete() || !got.ExtractedAt.Equal(e.ExtractedAt) {
		t.Fatalf("read back %+v, %v, %v", got, ok, err)
	}
	// A second attempt replaces the first.
	e.Failed, e.Attempts = nil, 2
	if err := st.PutExtraction(e); err != nil {
		t.Fatal(err)
	}
	other := Extraction{TextSHA: HashText("other"), Extractor: "other-model", Chunks: 1}
	if err := st.PutExtraction(other); err != nil {
		t.Fatal(err)
	}
	files, err := st.Extractions()
	if err != nil || len(files) != 2 {
		t.Fatalf("Extractions = %+v, %v", files, err)
	}
	for _, f := range files {
		x, err := ReadExtractionFile(f)
		if err != nil || x.TextSHA != f.TextSHA || ExtractorDir(x.Extractor) != f.Dir || f.Size == 0 {
			t.Errorf("file %+v read as %+v, %v", f, x, err)
		}
		if x.Extractor == e.Extractor && (!x.Complete() || x.Attempts != 2) {
			t.Errorf("the second attempt didn't replace the first: %+v", x)
		}
	}
	if ExtractorDir("qwen3.5:9b/claims-v1") != "qwen3.5_9b_claims-v1" {
		t.Errorf("ExtractorDir = %q", ExtractorDir("qwen3.5:9b/claims-v1"))
	}
}

func TestExtraction_Invalid(t *testing.T) {
	st, _ := Open(t.TempDir())
	if err := st.PutExtraction(Extraction{TextSHA: "nothex", Extractor: "m"}); err == nil {
		t.Error("bad hash accepted")
	}
	if err := st.PutExtraction(Extraction{TextSHA: HashText("x"), Extractor: "::"}); err == nil {
		t.Error("blank extractor accepted")
	}
	if files, err := st.Extractions(); err != nil || len(files) != 0 {
		t.Errorf("empty store lists %+v, %v", files, err)
	}
}

func TestLinks_AppendReadAndResume(t *testing.T) {
	st, _ := Open(t.TempDir())
	if log, err := st.ReadLinks(0); err != nil || len(log.Links) != 0 || st.LinksSize() != 0 {
		t.Fatalf("empty log = %+v, %v", log, err)
	}
	now := time.Now().UTC()
	first := []Link{
		{From: 1, To: 2, Relation: RelSupports, Method: LinkModel, Model: "sonnet", Confidence: 0.8, CreatedAt: now},
		{From: 3, To: 2, Relation: RelUnrelated, Method: LinkModel, Model: "sonnet", CreatedAt: now},
	}
	if err := st.AppendLinks(first); err != nil {
		t.Fatal(err)
	}
	log, err := st.ReadLinks(0)
	if err != nil || len(log.Links) != 2 || log.Size != st.LinksSize() {
		t.Fatalf("log = %+v, %v", log, err)
	}
	// A writer killed mid-line leaves a partial line: readers skip it, and
	// the next append starts on a fresh line.
	path := filepath.Join(st.Dir(), LinksFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"from":9,"to":`)
	f.Close()
	if l2, _ := st.ReadLinks(0); len(l2.Links) != 2 || l2.Size != log.Size {
		t.Errorf("partial line read: %+v", l2)
	}
	if err := st.AppendLinks([]Link{{From: 2, To: 1, Relation: RelContradicts, Method: LinkHuman, CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	rest, err := st.ReadLinks(log.Size)
	if err != nil || len(rest.Links) != 1 || rest.Links[0].Method != LinkHuman || len(rest.BadLines) != 1 {
		t.Fatalf("from offset: %+v, %v", rest, err)
	}
	if rest.Size != st.LinksSize() {
		t.Errorf("size %d, file %d", rest.Size, st.LinksSize())
	}
}

func TestLinks_Check(t *testing.T) {
	st, _ := Open(t.TempDir())
	bad := []Link{
		{From: 1, To: 1, Relation: RelSame, Method: LinkHuman},
		{From: 1, To: 0, Relation: RelSame, Method: LinkHuman},
		{From: 1, To: 2, Relation: "agrees", Method: LinkHuman},
		{From: 1, To: 2, Relation: RelSame, Method: LinkRule},
	}
	for _, l := range bad {
		if err := st.AppendLinks([]Link{l}); err == nil {
			t.Errorf("accepted %+v", l)
		}
	}
	if st.LinksSize() != 0 {
		t.Error("a rejected link was written")
	}
}

func TestLinks_ConcurrentAppendsDontInterleave(t *testing.T) {
	st, _ := Open(t.TempDir())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ls []Link
			for j := range 50 {
				ls = append(ls, Link{From: int64(i*1000 + j + 1), To: 999999, Relation: RelSame, Method: LinkModel,
					Note: strings.Repeat("x", 500), CreatedAt: time.Now()})
			}
			if err := st.AppendLinks(ls); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	log, err := st.ReadLinks(0)
	if err != nil || len(log.Links) != 400 || len(log.BadLines) != 0 {
		t.Errorf("%d links, bad lines %v, %v", len(log.Links), log.BadLines, err)
	}
}
