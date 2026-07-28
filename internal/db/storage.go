package db

import "context"

// MediaStorageBytes returns the bytes occupied by unique original media
// blobs. Multiple item rows may reference one content hash, so summing item
// sizes directly would overstate deduplicated storage.
func (s *Store) MediaStorageBytes(ctx context.Context) (int64, error) {
	var total int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM (
			SELECT content_hash, MAX(size) AS size
			FROM items
			GROUP BY content_hash
		)`).Scan(&total)
	return total, err
}
