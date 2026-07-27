package db

import (
	"context"
)

// RandomItem returns a uniformly random item that is live and not
// share-revoked, so a link the bot posts is always servable. An optional tag
// narrows the pool to items carrying that tag.
//
// This selects the full row in one statement, reusing the shared itemColumns
// list and scanItem helper that ItemByID also uses, so the two paths cannot
// drift. A single query is not just an optimisation here: it is what makes
// the exclusion race-free. An earlier version selected an id with
// ORDER BY RANDOM() LIMIT 1 and then re-fetched the row through ItemByID,
// which only filters deleted_at. database/sql returns the connection to the
// pool between those two calls, so another request could revoke the chosen
// item in the gap and ItemByID would still hand back a share_revoked row.
// Selecting the whole row here means the predicate and the data it guards
// come from the same statement; there is no gap for a concurrent write to
// land in.
//
// Two round trips (count then OFFSET) were also rejected for the same reason
// this codebase's query conventions avoid OFFSET everywhere else: it does not
// compose safely with concurrent writes.
func (s *Store) RandomItem(ctx context.Context, tag string) (*Item, error) {
	if tag == "" {
		return scanItem(s.DB.QueryRowContext(ctx,
			`SELECT `+itemColumns+` FROM items
			 WHERE deleted_at IS NULL AND share_revoked = 0
			 ORDER BY RANDOM() LIMIT 1`))
	}

	norm, err := NormalizeTag(tag)
	if err != nil {
		// An unusable tag cannot match any row.
		return nil, ErrNotFound
	}
	return scanItem(s.DB.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM items
		 WHERE deleted_at IS NULL AND share_revoked = 0
		 AND id IN (
			SELECT it.item_id FROM item_tags it
			JOIN tags t ON t.id = it.tag_id
			WHERE t.name = ?
		 )
		 ORDER BY RANDOM() LIMIT 1`, norm))
}
