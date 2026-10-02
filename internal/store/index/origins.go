package index

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/bits"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
)

// OriginStats is what grouping origins found.
type OriginStats struct {
	Documents int            `json:"documents"`
	Origins   int            `json:"origins"`
	Joined    map[string]int `json:"joined,omitempty"` // documents joined to another's origin, by reason
}

// Why two documents are one origin, strongest first. Each document's
// reason is the first one that joined it to its group.
var originReasons = []string{"source", "final-url", "doi", "same-link", "near-duplicate"}

// nearDuplicateBits is how many of a SimHash's 64 bits two near-duplicate
// documents may differ in; nearDuplicateChars is the shortest text
// compared, since short texts' hashes are noisy.
const (
	nearDuplicateBits  = 3
	nearDuplicateChars = 1000
)

// sameLinkPairs is how many `same` claims two documents must share to be
// one origin.
const sameLinkPairs = 2

// groupOrigins regroups every primary document into origins: versions of
// one source, documents fetched from the same final URL, papers with the
// same DOI, documents sharing sameLinkPairs `same` claims, and near-
// duplicate texts. It's a heuristic: a press release rewritten by
// different outlets won't always be caught.
func (ix *Index) groupOrigins(ctx context.Context, q querier, st *store.Store) (OriginStats, error) {
	stats := OriginStats{Joined: map[string]int{}}
	if err := ix.fillSimHashes(ctx, q, st); err != nil {
		return stats, err
	}
	type doc struct {
		id     int64
		hash   *int64
		chars  int
		parent int
		reason string
	}
	var docs []doc
	at := map[int64]int{}
	rows, err := q.Query(ctx, `SELECT id, simhash, text_chars FROM documents WHERE origin = 'primary' ORDER BY id`)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.id, &d.hash, &d.chars); err != nil {
			rows.Close()
			return stats, err
		}
		d.parent = len(docs)
		at[d.id] = len(docs)
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}
	stats.Documents = len(docs)

	var find func(i int) int
	find = func(i int) int {
		for docs[i].parent != i {
			docs[i].parent = docs[docs[i].parent].parent
			i = docs[i].parent
		}
		return i
	}
	// union joins b's group to a's, keeping the lower id as the root, and
	// records the reason on the document that joined.
	union := func(a, b int64, reason string) {
		i, ok1 := at[a]
		j, ok2 := at[b]
		if !ok1 || !ok2 {
			return
		}
		ri, rj := find(i), find(j)
		if ri == rj {
			return
		}
		if docs[rj].id < docs[ri].id {
			ri, rj = rj, ri
		}
		docs[rj].parent = ri
		if docs[rj].reason == "" {
			docs[rj].reason = reason
		}
	}
	groups := map[string]string{
		"source": `SELECT array_agg(id) FROM documents WHERE origin = 'primary' GROUP BY source_id HAVING count(*) > 1`,
		"final-url": `SELECT array_agg(DISTINCT f.document_id) FROM fetches f JOIN documents d ON d.id = f.document_id
			WHERE d.origin = 'primary' AND f.final_url <> '' GROUP BY f.final_url HAVING count(DISTINCT f.document_id) > 1`,
		"doi": `SELECT array_agg(d.id) FROM documents d JOIN sources s ON s.id = d.source_id
			WHERE d.origin = 'primary' AND s.doi IS NOT NULL GROUP BY s.doi HAVING count(*) > 1`,
		"same-link": `SELECT ARRAY[LEAST(a.document_id, b.document_id), GREATEST(a.document_id, b.document_id)]
			FROM claim_relations l JOIN claims a ON a.id = l.from_claim JOIN claims b ON b.id = l.to_claim
			WHERE l.relation = 'same' AND a.document_id <> b.document_id
			GROUP BY 1 HAVING count(*) >= ` + fmt.Sprint(sameLinkPairs),
	}
	for _, reason := range originReasons {
		if reason == "near-duplicate" {
			for _, p := range nearDuplicates(func(yield func(int64, uint64)) {
				for _, d := range docs {
					if d.hash != nil && d.chars >= nearDuplicateChars {
						yield(d.id, uint64(*d.hash))
					}
				}
			}) {
				union(p[0], p[1], reason)
			}
			continue
		}
		rows, err := q.Query(ctx, groups[reason])
		if err != nil {
			return stats, fmt.Errorf("%s origins: %w", reason, err)
		}
		for rows.Next() {
			var ids []int64
			if err := rows.Scan(&ids); err != nil {
				rows.Close()
				return stats, err
			}
			for _, id := range ids[1:] {
				union(ids[0], id, reason)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return stats, err
		}
	}

	ids := make([]int64, len(docs))
	origins := make([]int64, len(docs))
	reasons := make([]string, len(docs))
	roots := map[int]bool{}
	for i := range docs {
		r := find(i)
		roots[r] = true
		ids[i], origins[i] = docs[i].id, docs[r].id
		reasons[i] = "self"
		if r != i {
			reasons[i] = docs[i].reason
			stats.Joined[reasons[i]]++
		}
	}
	stats.Origins = len(roots)
	err = pgx.BeginFunc(ctx, q, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM origins`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO origins (document_id, origin_id, reason)
			SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::text[])`, ids, origins, reasons)
		return err
	})
	return stats, err
}

// nearDuplicates returns the pairs of documents whose SimHashes differ in
// at most nearDuplicateBits bits. By pigeonhole, two such hashes agree on
// at least one of four 16-bit bands, so only documents sharing a band are
// compared.
func nearDuplicates(each func(yield func(int64, uint64))) [][2]int64 {
	type entry struct {
		id   int64
		hash uint64
	}
	bands := [4]map[uint16][]entry{{}, {}, {}, {}}
	var out [][2]int64
	seen := map[[2]int64]bool{}
	each(func(id int64, h uint64) {
		for b := range 4 {
			k := uint16(h >> (16 * b))
			for _, e := range bands[b][k] {
				pair := [2]int64{min(e.id, id), max(e.id, id)}
				if !seen[pair] && bits.OnesCount64(e.hash^h) <= nearDuplicateBits {
					seen[pair] = true
					out = append(out, pair)
				}
			}
			bands[b][k] = append(bands[b][k], entry{id, h})
		}
	})
	slices.SortFunc(out, func(a, b [2]int64) int {
		if a[0] != b[0] {
			return compare(a[0], b[0])
		}
		return compare(a[1], b[1])
	})
	return out
}

func compare(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// fillSimHashes hashes the text of primary documents that have no SimHash
// yet. A document whose text is missing from the store keeps none.
func (ix *Index) fillSimHashes(ctx context.Context, q querier, st *store.Store) error {
	rows, err := q.Query(ctx, `SELECT id, sha256 FROM documents WHERE simhash IS NULL AND origin = 'primary'`)
	if err != nil {
		return err
	}
	var ids []int64
	var hashes []int64
	type todo struct {
		id  int64
		sha string
	}
	var todos []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.id, &t.sha); err != nil {
			rows.Close()
			return err
		}
		todos = append(todos, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range todos {
		text, err := st.ReadText(t.sha)
		if err != nil {
			continue
		}
		ids = append(ids, t.id)
		hashes = append(hashes, int64(SimHash(text)))
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = q.Exec(ctx, `UPDATE documents d SET simhash = u.h FROM unnest($1::bigint[], $2::bigint[]) AS u(id, h) WHERE d.id = u.id`, ids, hashes)
	return err
}

// SimHash is a 64-bit SimHash of text's word 3-shingles: texts that share
// most of their shingles get hashes that differ in few bits. A text of
// under 3 words is one shingle.
func SimHash(text string) uint64 {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return 0
	}
	var counts [64]int
	for i := 0; i == 0 || i+3 <= len(words); i++ {
		h := fnv.New64a()
		h.Write([]byte(strings.Join(words[i:min(i+3, len(words))], " ")))
		v := h.Sum64()
		for b := range 64 {
			if v&(1<<b) != 0 {
				counts[b]++
			} else {
				counts[b]--
			}
		}
	}
	var out uint64
	for b := range 64 {
		if counts[b] > 0 {
			out |= 1 << b
		}
	}
	return out
}
