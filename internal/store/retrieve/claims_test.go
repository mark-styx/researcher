package retrieve

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
)

const claimExtractor = "test/claims-v1"

// claimFixture is the find fixture with claims and links:
//
//	cat     "the cat is a popular animal" (cats page, published 2020-01)
//	chapter the same, from the cat book, true as of 2019, by model
//	dog     contradicts cat (dogs page, 2024-06), by model
//	fell    markets fell, volatile, as of 2026-08 (undated markets page)
//	dropped supersedes fell, volatile, as of 2026-09-20, by a person
//	bonds   a quote not in the markets page: unverified
type claimFixture struct {
	*fixture
	ids map[string]int64
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	ctx := context.Background()
	f := &claimFixture{fixture: newFixture(t), ids: map[string]int64{}}
	type claim struct {
		name string
		store.ExtractedClaim
	}
	for d, claims := range map[doc][]claim{
		catDoc:    {{"cat", store.ExtractedClaim{Text: "Cats are popular animals.", Quote: "the cat is a popular animal"}}},
		longCat:   {{"chapter", store.ExtractedClaim{Text: "The book says cats are popular.", Quote: "The cat chapter one", AsOf: "2019"}}},
		dogDoc:    {{"dog", store.ExtractedClaim{Text: "Dogs, not cats, are the popular animal.", Quote: "the dog is a popular animal"}}},
		marketDoc: {{"fell", store.ExtractedClaim{Text: "Stock markets fell.", Quote: "Stock markets fell", AsOf: "2026-08", Volatile: true}}, {"dropped", store.ExtractedClaim{Text: "Equity prices dropped further.", Quote: "equity prices dropped", AsOf: "2026-09-20", Volatile: true}}, {"bonds", store.ExtractedClaim{Text: "Bonds rallied.", Quote: "bonds rallied strongly"}}},
	} {
		e := store.Extraction{TextSHA: store.HashText(d.text), Extractor: claimExtractor, Model: "test", Chunks: 1, Attempts: 1,
			ExtractedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
		for i, c := range claims {
			e.Claims = append(e.Claims, c.ExtractedClaim)
			f.ids[c.name] = index.ClaimID(f.byURL[d.url], claimExtractor, 0, i)
		}
		if err := f.st.PutExtraction(e); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := f.st.AppendLinks([]store.Link{
		{From: f.ids["chapter"], To: f.ids["cat"], Relation: store.RelSame, Method: store.LinkModel, Model: "claude/sonnet", Confidence: 0.9, CreatedAt: at},
		{From: f.ids["dog"], To: f.ids["cat"], Relation: store.RelContradicts, Method: store.LinkModel, Model: "claude/sonnet", Confidence: 0.7, Note: "dogs, not cats", CreatedAt: at},
		{From: f.ids["dropped"], To: f.ids["fell"], Relation: store.RelSupersedes, Method: store.LinkHuman, CreatedAt: at},
		// Overruled: a person says it's unrelated after all.
		{From: f.ids["dog"], To: f.ids["dropped"], Relation: store.RelSupports, Method: store.LinkModel, CreatedAt: at},
		{From: f.ids["dropped"], To: f.ids["dog"], Relation: store.RelUnrelated, Method: store.LinkHuman, CreatedAt: at},
	}); err != nil {
		t.Fatal(err)
	}
	cs, err := f.ix.SyncClaims(ctx, f.st)
	if err != nil || cs.Claims != 6 || cs.Verified != 5 || cs.Links != 5 {
		t.Fatalf("sync = %+v, %v", cs, err)
	}
	if _, err := f.ix.EmbedClaims(ctx, f.st, fakeEmbedder{}); err != nil {
		t.Fatal(err)
	}
	return f
}

func cardByRef(cards []Card, ref string) *Card {
	for i := range cards {
		if cards[i].Ref == ref {
			return &cards[i]
		}
	}
	return nil
}

func relations(cl *Cluster) []string {
	var out []string
	for _, r := range cl.Related {
		out = append(out, r.Relation+" "+r.Method)
	}
	return out
}

func TestFind_ClaimCardsAndFlags(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()
	res, err := f.r.Find(ctx, Query{Text: "popular animal", Kinds: []string{KindClaim}})
	if err != nil {
		t.Fatal(err)
	}
	cat := cardByRef(res.Cards, ClaimRef(f.ids["cat"]))
	if cat == nil {
		t.Fatalf("cards = %+v", res.Cards)
	}
	if cat.Kind != KindClaim || cat.ClaimID != f.ids["cat"] || !cat.QuoteVerified || cat.Quote != "the cat is a popular animal" ||
		cat.Text != "Cats are popular animals." || cat.URL != catDoc.url || cat.PassageID == 0 {
		t.Errorf("cat card = %+v", cat)
	}
	if !slices.Equal(cat.Flags, []string{FlagReinforced, FlagContested, FlagNewerContradiction}) {
		t.Errorf("cat flags = %v", cat.Flags)
	}
	cl := cat.Cluster
	if cl.Origins != 2 || cl.SupportDates != "2019-2020" || !cl.LastConfirmed.Equal(t2025) {
		t.Errorf("cluster = %+v", cl)
	}
	if got := relations(cl); !slices.Equal(got, []string{"contradicts model", "same model"}) {
		t.Errorf("relations = %v", got)
	}
	if r := cl.Related[0]; r.Ref != ClaimRef(f.ids["dog"]) || r.Model != "claude/sonnet" || math.Abs(r.Confidence-0.7) > 1e-6 || r.Note != "dogs, not cats" ||
		r.Dated != "2024-06-01" || r.Domain != "zoo.example" {
		t.Errorf("contradiction = %+v", r)
	}
	dog := cardByRef(res.Cards, ClaimRef(f.ids["dog"]))
	if dog == nil || !slices.Equal(dog.Flags, []string{FlagSingleOrigin, FlagContested}) {
		t.Errorf("dog = %+v", dog)
	}
	// The person's "unrelated" overrules the model's "supports".
	if dog != nil && slices.ContainsFunc(dog.Cluster.Related, func(r Related) bool { return r.ClaimID == f.ids["dropped"] }) {
		t.Errorf("overruled link shown: %v", relations(dog.Cluster))
	}
	for _, c := range res.Cards {
		if c.Kind != KindClaim || c.ClaimID == f.ids["bonds"] {
			t.Errorf("claim find returned %s %s", c.Kind, c.Ref)
		}
	}

	// Seen from before the dog page was collected, the cat claim isn't
	// contested.
	asOf := t2025.Add(time.Hour)
	early, _ := f.r.Find(ctx, Query{Text: "popular animal", Kinds: []string{KindClaim}, AsOf: &asOf})
	if c := cardByRef(early.Cards, ClaimRef(f.ids["cat"])); c == nil || !slices.Equal(c.Flags, []string{FlagReinforced}) {
		t.Errorf("as of 2025: %+v", c)
	}

	// A superseded claim is collapsed under the claim that supersedes it.
	mk, _ := f.r.Find(ctx, Query{Text: "stock markets fell equity prices dropped", Kinds: []string{KindClaim}})
	if cardByRef(mk.Cards, ClaimRef(f.ids["fell"])) != nil {
		t.Errorf("superseded claim not collapsed: %v", mk.Cards)
	}
	dropped := cardByRef(mk.Cards, ClaimRef(f.ids["dropped"]))
	if dropped == nil || !slices.Equal(relations(dropped.Cluster), []string{"supersedes human"}) || !dropped.Volatile || dropped.AsOf != "2026-09-20" {
		t.Fatalf("dropped = %+v", dropped)
	}
	if !strings.HasPrefix(dropped.Age, "true as of 2026-09-20 (12d ago); undated") {
		t.Errorf("age = %q", dropped.Age)
	}

	// Without kinds, claims come with passages.
	all, _ := f.r.Find(ctx, Query{Text: "popular animal", Limit: 20})
	kinds := map[string]bool{}
	for _, c := range all.Cards {
		kinds[c.Kind] = true
	}
	if !kinds[KindClaim] || !kinds[KindPassage] {
		t.Errorf("default kinds = %v", kinds)
	}
	only, _ := f.r.Find(ctx, Query{Text: "popular animal", Kinds: []string{KindPassage}})
	for _, c := range only.Cards {
		if c.Kind != KindPassage {
			t.Errorf("passage find returned a %s", c.Kind)
		}
	}
}

func TestClaim_Lookup(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()
	fell, err := f.r.Claim(ctx, f.ids["fell"])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fell.Flags, []string{FlagSingleOrigin, FlagSuperseded, FlagPossiblyOutdated}) || fell.Extractor != claimExtractor {
		t.Errorf("fell = %+v", fell)
	}
	if fell.Passage == nil || !strings.Contains(fell.Passage.Text, "Stock markets fell") || fell.Quote != "Stock markets fell" || fell.ModelQuote != "" {
		t.Errorf("fell passage/quote = %+v / %q", fell.Passage, fell.Quote)
	}
	if got := relations(fell.Cluster); !slices.Equal(got, []string{"superseded_by human"}) {
		t.Errorf("fell relations = %v", got)
	}

	bonds, err := f.r.Claim(ctx, f.ids["bonds"])
	if err != nil || bonds.QuoteVerified || bonds.Quote != "" || bonds.ModelQuote != "bonds rallied strongly" {
		t.Errorf("unverified claim = %+v, %v", bonds, err)
	}
	if _, err := f.r.Claim(ctx, 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing claim: %v", err)
	}
}

func TestTimeline(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()
	res, err := f.r.Timeline(ctx, Query{Text: "popular animal", MinSimilarity: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range res.Entries {
		order = append(order, e.Dated+" "+e.Ref)
	}
	want := []string{"2019-01-01 " + ClaimRef(f.ids["chapter"]), "2020-01-15 " + ClaimRef(f.ids["cat"]), "2024-06-01 " + ClaimRef(f.ids["dog"])}
	if !slices.Equal(order, want) {
		t.Fatalf("timeline = %v, want %v", order, want)
	}
	if dog := res.Entries[2]; !slices.Equal(dog.Changes, []string{"contradicts " + ClaimRef(f.ids["cat"]) + " (2020-01-15)"}) || !dog.Matched {
		t.Errorf("dog entry = %+v", dog)
	}

	byRef, err := f.r.Timeline(ctx, Query{Text: ClaimRef(f.ids["fell"])})
	if err != nil || len(byRef.Entries) != 2 {
		t.Fatalf("timeline of a claim = %+v, %v", byRef, err)
	}
	if e := byRef.Entries; e[0].ClaimID != f.ids["fell"] || !e[0].Matched || e[1].ClaimID != f.ids["dropped"] || e[1].Matched ||
		!slices.Equal(e[1].Changes, []string{"supersedes " + ClaimRef(f.ids["fell"])}) {
		t.Errorf("entries = %+v", e)
	}
	if _, err := f.r.Timeline(ctx, Query{Text: "C:12345"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing claim: %v", err)
	}
}

func TestFind_VolatileHalfLife(t *testing.T) {
	f := newClaimFixture(t)
	ctx := context.Background()
	q := Query{Text: "popular animal stock markets equity", Kinds: []string{KindClaim}, Limit: 20}
	scores := func() (cat, dropped float64) {
		t.Helper()
		res, err := f.r.Find(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		c, d := cardByRef(res.Cards, ClaimRef(f.ids["cat"])), cardByRef(res.Cards, ClaimRef(f.ids["dropped"]))
		if c == nil || d == nil {
			t.Fatalf("cards = %+v", res.Cards)
		}
		return c.Score, d.Score
	}
	cat, dropped := scores()
	// Only the volatile claim decays, by its 12 days at a day's half-life.
	f.r.VolatileHalfLife = 24 * time.Hour
	cat2, dropped2 := scores()
	if cat2 != cat || math.Abs(dropped2/dropped-math.Pow(0.5, 12-12.0/365)) > 1e-3 {
		t.Errorf("cat %v -> %v, dropped %v -> %v", cat, cat2, dropped, dropped2)
	}
	// prefer_recent overrides it.
	q.PreferRecent = 365 * 24 * time.Hour
	if _, d := scores(); math.Abs(d/dropped-1) > 1e-3 {
		t.Errorf("prefer_recent: dropped %v -> %v", dropped, d)
	}
}
