package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// NormalizeTag lowercases, trims and validates a tag.
func NormalizeTag(s string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(s))
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("db: tag %q must be 1-32 characters of a-z, 0-9, dot, dash or underscore", s)
	}
	return tag, nil
}

// UpsertTag returns the id of a tag, creating it if necessary.
func (s *Store) UpsertTag(ctx context.Context, name string) (int64, error) {
	tag, err := NormalizeTag(name)
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, tag); err != nil {
		return 0, fmt.Errorf("db: upsert tag %q: %w", tag, err)
	}
	var id int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, tag).Scan(&id); err != nil {
		return 0, fmt.Errorf("db: read tag %q: %w", tag, err)
	}
	return id, nil
}

// AddItemTag attaches a tag to an item. Attaching twice is a no-op.
func (s *Store) AddItemTag(ctx context.Context, itemID, name string) error {
	tagID, err := s.UpsertTag(ctx, name)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO item_tags (item_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, itemID, tagID)
	if err != nil {
		return fmt.Errorf("db: tag item %s: %w", itemID, err)
	}
	return nil
}

// RemoveItemTag detaches a tag. Removing an unattached tag is not an error.
func (s *Store) RemoveItemTag(ctx context.Context, itemID, name string) error {
	tag, err := NormalizeTag(name)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`DELETE FROM item_tags WHERE item_id = ?
		 AND tag_id = (SELECT id FROM tags WHERE name = ?)`, itemID, tag)
	if err != nil {
		return fmt.Errorf("db: untag item %s: %w", itemID, err)
	}
	return nil
}

// ItemTags returns one item's tags, sorted.
func (s *Store) ItemTags(ctx context.Context, itemID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.name FROM item_tags it JOIN tags t ON t.id = it.tag_id
		 WHERE it.item_id = ? ORDER BY t.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("db: read item tags: %w", err)
	}
	defer rows.Close()
	return scanTagNames(rows)
}

// TagsForItems reads the tags of many items in one query, so rendering a grid
// page does not issue a query per thumbnail.
func (s *Store) TagsForItems(ctx context.Context, itemIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(itemIDs)), ",")
	args := make([]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		args = append(args, id)
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT it.item_id, t.name FROM item_tags it JOIN tags t ON t.id = it.tag_id
		 WHERE it.item_id IN (`+placeholders+`) ORDER BY it.item_id, t.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("db: read tags for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var itemID, name string
		if err := rows.Scan(&itemID, &name); err != nil {
			return nil, fmt.Errorf("db: scan item tag: %w", err)
		}
		out[itemID] = append(out[itemID], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate item tags: %w", err)
	}
	return out, nil
}

// ListTags returns every tag in use, sorted. Plan 4's filter chips use this.
func (s *Store) ListTags(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT DISTINCT t.name FROM tags t JOIN item_tags it ON it.tag_id = t.id ORDER BY t.name`)
	if err != nil {
		return nil, fmt.Errorf("db: list tags: %w", err)
	}
	defer rows.Close()
	return scanTagNames(rows)
}

func scanTagNames(rows *sql.Rows) ([]string, error) {
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: scan tag: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("db: iterate tags: %w", err)
	}
	return names, nil
}
