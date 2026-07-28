package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ItemSort selects the browse ordering.
type ItemSort int

const (
	SortNewest ItemSort = iota
	SortOldest
	SortTitle
	SortSize
	SortUploader
)

// DefaultItemLimit and MaxItemLimit bound a single page.
const (
	DefaultItemLimit = 60
	MaxItemLimit     = 200
)

// ParseItemSort maps the API's sort parameter onto an ItemSort. Unknown values
// are rejected rather than defaulted, so a typo is visible instead of silently
// changing the ordering.
func ParseItemSort(s string) (ItemSort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "newest", "date":
		return SortNewest, nil
	case "oldest":
		return SortOldest, nil
	case "name", "title":
		return SortTitle, nil
	case "size":
		return SortSize, nil
	case "uploader":
		return SortUploader, nil
	default:
		return SortNewest, fmt.Errorf("db: unknown sort %q", s)
	}
}

// sortSpec describes how one ItemSort maps onto SQL. The expression is always
// a fixed string chosen from this table, never interpolated user input.
type sortSpec struct {
	expr       string // SQL expression for the sort key
	descending bool
}

func (s ItemSort) spec() sortSpec {
	switch s {
	case SortOldest:
		return sortSpec{expr: "i.created_at"}
	case SortTitle:
		return sortSpec{expr: "lower(i.title)"}
	case SortSize:
		return sortSpec{expr: "i.size", descending: true}
	case SortUploader:
		return sortSpec{expr: "(SELECT lower(u.username) FROM users u WHERE u.id = i.uploader_id)"}
	default: // SortNewest
		return sortSpec{expr: "i.created_at", descending: true}
	}
}

// ItemQuery describes one page of a browse listing.
type ItemQuery struct {
	// FolderID: nil means no folder filter; a pointer to 0 means the root
	// (folder_id IS NULL); a pointer to a positive id means that folder.
	FolderID   *int64
	Tag        string
	UploaderID int64
	Query      string // case-insensitive substring of the title
	MediaType  string // image, video, animated, or one supported extension
	Sort       ItemSort
	Limit      int
	Cursor     string
}

// itemCursor is the opaque position marker handed back to the client.
type itemCursor struct {
	Key string `json:"k"` // the sort key of the last row on the previous page
	ID  string `json:"i"` // that row's item id, breaking ties
}

func encodeCursor(c itemCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("db: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(s string) (*itemCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("db: malformed cursor")
	}
	var c itemCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("db: malformed cursor")
	}
	if c.ID == "" {
		return nil, fmt.Errorf("db: malformed cursor")
	}
	return &c, nil
}

// escapeLike neutralises LIKE metacharacters so a search for "%" matches a
// literal percent sign instead of every row.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ListItems returns one keyset page of live items plus the cursor for the next
// page ("" when the listing is exhausted).
func (s *Store) ListItems(ctx context.Context, q ItemQuery) ([]*Item, string, error) {
	limit := q.Limit
	if limit <= 0 || limit > MaxItemLimit {
		limit = DefaultItemLimit
	}
	spec := q.Sort.spec()

	where := []string{"i.deleted_at IS NULL"}
	args := []any{}

	if q.FolderID != nil {
		if *q.FolderID == 0 {
			where = append(where, "i.folder_id IS NULL")
		} else {
			where = append(where, "i.folder_id = ?")
			args = append(args, *q.FolderID)
		}
	}
	if q.UploaderID != 0 {
		where = append(where, "i.uploader_id = ?")
		args = append(args, q.UploaderID)
	}
	if tag := strings.ToLower(strings.TrimSpace(q.Tag)); tag != "" {
		where = append(where,
			`i.id IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE t.name = ?)`)
		args = append(args, tag)
	}
	if query := strings.TrimSpace(q.Query); query != "" {
		where = append(where, `lower(i.title) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(strings.ToLower(query))+"%")
	}
	switch strings.ToLower(strings.TrimSpace(q.MediaType)) {
	case "":
	case "image":
		where = append(where, `i.mime LIKE 'image/%'`)
	case "video":
		where = append(where, `i.mime LIKE 'video/%'`)
	case "animated":
		where = append(where, `i.mime IN ('image/gif', 'image/webp')`)
	case "gif":
		where = append(where, `i.mime = 'image/gif'`)
	case "webp":
		where = append(where, `i.mime = 'image/webp'`)
	case "jpg", "jpeg":
		where = append(where, `i.mime = 'image/jpeg'`)
	case "png":
		where = append(where, `i.mime = 'image/png'`)
	case "avif":
		where = append(where, `i.mime = 'image/avif'`)
	case "mp4":
		where = append(where, `i.mime = 'video/mp4'`)
	case "webm":
		where = append(where, `i.mime = 'video/webm'`)
	default:
		return nil, "", fmt.Errorf("db: unknown media type %q", q.MediaType)
	}

	comparison := ">"
	direction := "ASC"
	if spec.descending {
		comparison = "<"
		direction = "DESC"
	}
	if q.Cursor != "" {
		cursor, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, fmt.Sprintf("(%s, i.id) %s (?, ?)", spec.expr, comparison))
		args = append(args, cursor.Key, cursor.ID)
	}

	// Fetch one extra row to learn whether another page exists.
	args = append(args, limit+1)
	query := fmt.Sprintf(
		`SELECT %s, %s FROM items i WHERE %s ORDER BY %s %s, i.id %s LIMIT ?`,
		itemColumns, spec.expr,
		strings.Join(where, " AND "),
		spec.expr, direction, direction)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("db: list items: %w", err)
	}
	defer rows.Close()

	var (
		items []*Item
		keys  []string
	)
	for rows.Next() {
		item, key, err := scanItemWithSortKey(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, item)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("db: iterate items: %w", err)
	}

	if len(items) <= limit {
		return items, "", nil
	}
	items = items[:limit]
	next, err := encodeCursor(itemCursor{Key: keys[limit-1], ID: items[limit-1].ID})
	if err != nil {
		return nil, "", err
	}
	return items, next, nil
}

// scanItemWithSortKey scans the item columns plus the trailing sort-key column.
func scanItemWithSortKey(rows interface{ Scan(...any) error }) (*Item, string, error) {
	var (
		item      Item
		deletedAt string
		createdAt string
		sortKey   sortKeyValue
	)
	err := rows.Scan(&item.ID, &item.ContentHash, &item.Title, &item.Ext, &item.Mime, &item.Size,
		&item.Width, &item.Height, &item.Duration,
		&item.UploaderID, &item.FolderID, &item.SourceURL, &item.JobID,
		&item.ShareRevoked, &deletedAt, &createdAt, &sortKey)
	if err != nil {
		return nil, "", fmt.Errorf("db: scan item row: %w", err)
	}
	parsed, err := finishItem(&item, deletedAt, createdAt)
	if err != nil {
		return nil, "", err
	}
	return parsed, sortKey.String(), nil
}

// sortKeyValue accepts whichever SQLite type the sort expression produces
// (text for timestamps and titles, integer for size) and renders it back as
// the exact text the next comparison needs.
type sortKeyValue struct{ raw any }

func (v *sortKeyValue) Scan(src any) error {
	v.raw = src
	return nil
}

func (v sortKeyValue) String() string {
	switch t := v.raw.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// ListDeletedItems returns the trash, newest deletion first. Plan 4's admin
// page renders this.
func (s *Store) ListDeletedItems(ctx context.Context, limit int) ([]*Item, error) {
	if limit <= 0 || limit > MaxItemLimit {
		limit = DefaultItemLimit
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list deleted items: %w", err)
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate deleted items: %w", err)
	}
	return items, nil
}
