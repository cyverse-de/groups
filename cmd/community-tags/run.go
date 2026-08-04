package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	_ "github.com/lib/pq"
)

// report accumulates what a run did. Every counter prints even when zero: a run
// that reports nothing is indistinguishable from one that did nothing.
type report struct {
	CommunitiesKnown int
	ValuesSeen       int
	ValuesRewritten  int
	RowsRewritten    int64
	RowsDeduplicated int64
	Orphans          []string
}

func (r *report) Print() {
	logf("communities known: %d", r.CommunitiesKnown)
	logf("distinct tag values: %d, of which %d rewritten", r.ValuesSeen, r.ValuesRewritten)
	logf("tag rows: %d rewritten, %d removed as duplicates", r.RowsRewritten, r.RowsDeduplicated)

	if len(r.Orphans) == 0 {
		logf("tag values naming no community (left as they are): none")
		return
	}
	logf("tag values naming no community (left as they are): %d", len(r.Orphans))
	for _, value := range r.Orphans {
		logf("  %s", value)
	}
}

func run(ctx context.Context, cfg *config) (*report, error) {
	rep := &report{}

	deDB, err := sql.Open("postgres", cfg.DatabaseURI)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the DE database: %w", err)
	}
	defer func() { _ = deDB.Close() }()

	metaDB, err := sql.Open("postgres", cfg.MetadataURI)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the metadata database: %w", err)
	}
	defer func() { _ = metaDB.Close() }()

	communities, err := readCommunities(ctx, deDB, cfg.Schema)
	if err != nil {
		return rep, err
	}
	rep.CommunitiesKnown = len(communities.byID)

	values, err := readTagValues(ctx, metaDB, cfg.Attribute)
	if err != nil {
		return rep, err
	}
	rep.ValuesSeen = len(values)

	rewrite, orphans := classifyTagValues(values, communities)
	rep.ValuesRewritten = len(rewrite)
	rep.Orphans = orphans

	if cfg.DryRun || len(rewrite) == 0 {
		return rep, nil
	}
	return rep, applyRewrites(ctx, metaDB, cfg.Attribute, rewrite, rep)
}

// readCommunities indexes every community by the three ways a stored tag might
// name one.
func readCommunities(ctx context.Context, db *sql.DB, schema string) (communityIndex, error) {
	index := communityIndex{
		byID:         map[string]bool{},
		byLegacyName: map[string]string{},
		byName:       map[string]string{},
	}

	query := fmt.Sprintf(`
		SELECT s.subject_id, g.name, coalesce(g.legacy_name, '')
		  FROM %s.groups g
		  JOIN %s.subjects s ON s.id = g.subject_id
		 WHERE g.group_type = 'community'`, schema, schema)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return index, fmt.Errorf("could not read communities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, name, legacyName string
		if err := rows.Scan(&id, &name, &legacyName); err != nil {
			return index, fmt.Errorf("could not read a community: %w", err)
		}
		index.byID[id] = true
		index.byName[name] = id
		if legacyName != "" {
			index.byLegacyName[legacyName] = id
		}
	}
	return index, rows.Err()
}

// readTagValues returns the distinct values stored under the community
// attribute.
func readTagValues(ctx context.Context, db *sql.DB, attribute string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT value FROM avus WHERE attribute = $1 AND value IS NOT NULL`, attribute)
	if err != nil {
		return nil, fmt.Errorf("could not read community tags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("could not read a community tag: %w", err)
		}
		values = append(values, value)
	}
	sort.Strings(values)
	return values, rows.Err()
}

// applyRewrites rewrites every tag value in one transaction, so a failure part
// way through leaves the tags exactly as they were.
func applyRewrites(ctx context.Context, db *sql.DB, attribute string,
	rewrite map[string]string, rep *report) error {

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("could not begin the rewrite transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Deterministic order, so two runs against the same data do the same thing
	// in the same sequence and a failure is reproducible.
	from := make([]string, 0, len(rewrite))
	for value := range rewrite {
		from = append(from, value)
	}
	sort.Strings(from)

	for _, old := range from {
		id := rewrite[old]

		// An app already carrying the target ID -- tagged again after the
		// cutover, say -- would collide with avus_unique on rewrite. Drop the
		// row being migrated rather than fail the run: the tag it expresses is
		// already present, so nothing is lost.
		dropped, err := tx.ExecContext(ctx, `
			DELETE FROM avus a
			 WHERE a.attribute = $1 AND a.value = $2
			   AND EXISTS (
			       SELECT 1 FROM avus b
			        WHERE b.attribute = a.attribute AND b.value = $3
			          AND b.unit = a.unit
			          AND b.target_id = a.target_id
			          AND b.target_type = a.target_type)`, attribute, old, id)
		if err != nil {
			return fmt.Errorf("could not remove duplicate tags for %q: %w", old, err)
		}
		n, err := dropped.RowsAffected()
		if err != nil {
			return fmt.Errorf("could not count duplicate tags for %q: %w", old, err)
		}
		rep.RowsDeduplicated += n

		updated, err := tx.ExecContext(ctx,
			`UPDATE avus SET value = $1, modified_on = now()
			  WHERE attribute = $2 AND value = $3`, id, attribute, old)
		if err != nil {
			return fmt.Errorf("could not rewrite the tag %q: %w", old, err)
		}
		n, err = updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("could not count rewritten tags for %q: %w", old, err)
		}
		rep.RowsRewritten += n
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("could not commit the rewrite: %w", err)
	}
	committed = true
	return nil
}
