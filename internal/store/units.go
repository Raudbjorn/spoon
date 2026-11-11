package store

import (
	"context"
	"database/sql"
	"fmt"
)

// TouchedUnitRecord stores unit-level attribution (functions, rules, exports) for modified paths.
type TouchedUnitRecord struct {
	ForkKey       string
	Path          string
	BaseSHA       string
	HeadSHA       string
	Tier          int // 0 = hunk-context / AST, 1 = AST declaration diff
	UnitsJSON     string
	SkippedReason string
}

// UpsertTouchedUnits writes or updates touched unit records.
func (s *Store) UpsertTouchedUnits(ctx context.Context, units []TouchedUnitRecord) error {
	if len(units) == 0 {
		return nil
	}

	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const query = `INSERT INTO touched_units (
		fork_key, path, base_sha, head_sha, tier, units_json, skipped_reason
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(fork_key, path, base_sha, head_sha, tier) DO UPDATE SET
		units_json = excluded.units_json,
		skipped_reason = excluded.skipped_reason`

	for _, u := range units {
		var skippedVal sql.NullString
		if u.SkippedReason != "" {
			skippedVal = sql.NullString{String: u.SkippedReason, Valid: true}
		}
		_, err := tx.ExecContext(ctx, query,
			u.ForkKey,
			u.Path,
			u.BaseSHA,
			u.HeadSHA,
			u.Tier,
			u.UnitsJSON,
			skippedVal,
		)
		if err != nil {
			return fmt.Errorf("upsert touched_unit %s/%s: %w", u.ForkKey, u.Path, err)
		}
	}

	return tx.Commit(ctx)
}

// GetTouchedUnits retrieves all touched unit records for a given forkKey.
func (s *Store) GetTouchedUnits(ctx context.Context, forkKey string) ([]TouchedUnitRecord, error) {
	const query = `SELECT fork_key, path, base_sha, head_sha, tier, units_json, skipped_reason
		FROM touched_units WHERE fork_key = ? ORDER BY path ASC, tier ASC`

	rows, err := s.db.QueryContext(ctx, query, forkKey)
	if err != nil {
		return nil, fmt.Errorf("query touched_units %s: %w", forkKey, err)
	}
	defer rows.Close()

	var out []TouchedUnitRecord
	for rows.Next() {
		var u TouchedUnitRecord
		var skippedVal sql.NullString
		if err := rows.Scan(
			&u.ForkKey,
			&u.Path,
			&u.BaseSHA,
			&u.HeadSHA,
			&u.Tier,
			&u.UnitsJSON,
			&skippedVal,
		); err != nil {
			return nil, err
		}
		if skippedVal.Valid {
			u.SkippedReason = skippedVal.String
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
