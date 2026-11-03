package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ClusterRunRecord stores the state of continual clustering runs for a repository.
type ClusterRunRecord struct {
	RepoKey          string
	EmbedderFP       string
	ComputedAt       time.Time
	LastReassignedAt time.Time
	NextClusterSeq   int
}

// ClusterRecord stores an identified cluster within a repo's fork ecosystem.
type ClusterRecord struct {
	RepoKey        string
	ClusterID      string
	Centroid       []byte
	AnchorCentroid []byte
	MemberCount    int
	Label          string
	Summary        string
	LineageID      string
	StableSince    time.Time
}

// ClusterMemberRecord maps a fork to its assigned cluster (or empty for noise)
// and caches its raw vector embedding so subsequent Reassign passes are instant.
type ClusterMemberRecord struct {
	RepoKey   string
	ForkKey   string
	HeadSHA   string
	ClusterID string // empty or "noise" means outlier/noise
	Embedding []byte
}

// GlobalLineageRecord stores cross-repo cluster prototypes.
type GlobalLineageRecord struct {
	LineageID       string
	EmbedderFP      string
	Centroid        []byte
	Label           string
	OriginRepo      string
	OriginClusterID string
	CreatedAt       time.Time
	LastSeenAt      time.Time
}

// LineageOccurrenceRecord tracks repos/clusters that instantiate a global lineage.
type LineageOccurrenceRecord struct {
	LineageID   string
	RepoKey     string
	ClusterID   string
	MemberCount int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// UpsertClusterRun records or updates the clustering run metadata for a repository.
func (s *Store) UpsertClusterRun(ctx context.Context, run ClusterRunRecord) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const query = `INSERT INTO cluster_runs (repo_key, embedder_fp, computed_at, last_reassigned_at, next_cluster_seq)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repo_key) DO UPDATE SET
			embedder_fp = excluded.embedder_fp,
			computed_at = excluded.computed_at,
			last_reassigned_at = excluded.last_reassigned_at,
			next_cluster_seq = excluded.next_cluster_seq`

	_, err = tx.ExecContext(ctx, query,
		run.RepoKey,
		run.EmbedderFP,
		run.ComputedAt.UTC().Format(time.RFC3339Nano),
		run.LastReassignedAt.UTC().Format(time.RFC3339Nano),
		run.NextClusterSeq,
	)
	if err != nil {
		return fmt.Errorf("upsert cluster_run %s: %w", run.RepoKey, err)
	}
	return tx.Commit(ctx)
}

// GetClusterRun loads the clustering run metadata for a repository.
func (s *Store) GetClusterRun(ctx context.Context, repoKey string) (*ClusterRunRecord, error) {
	const query = `SELECT repo_key, embedder_fp, computed_at, last_reassigned_at, next_cluster_seq
		FROM cluster_runs WHERE repo_key = ?`

	var run ClusterRunRecord
	var computedAt, lastReassignedAt string
	err := s.db.QueryRowContext(ctx, query, repoKey).Scan(
		&run.RepoKey,
		&run.EmbedderFP,
		&computedAt,
		&lastReassignedAt,
		&run.NextClusterSeq,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query cluster_run %s: %w", repoKey, err)
	}

	if t, perr := time.Parse(time.RFC3339Nano, computedAt); perr == nil {
		run.ComputedAt = t
	} else if t, perr := time.Parse(time.RFC3339, computedAt); perr == nil {
		run.ComputedAt = t
	}
	if t, perr := time.Parse(time.RFC3339Nano, lastReassignedAt); perr == nil {
		run.LastReassignedAt = t
	} else if t, perr := time.Parse(time.RFC3339, lastReassignedAt); perr == nil {
		run.LastReassignedAt = t
	}
	return &run, nil
}

// SaveClusters replaces all clusters for repoKey and upserts cluster member assignments and embeddings.
func (s *Store) SaveClusters(ctx context.Context, repoKey string, clusters []ClusterRecord, members []ClusterMemberRecord) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Replace existing clusters for this repo
	if _, err := tx.ExecContext(ctx, `DELETE FROM clusters WHERE repo_key = ?`, repoKey); err != nil {
		return fmt.Errorf("delete old clusters %s: %w", repoKey, err)
	}

	for _, c := range clusters {
		var summaryVal, lineageVal sql.NullString
		if c.Summary != "" {
			summaryVal = sql.NullString{String: c.Summary, Valid: true}
		}
		if c.LineageID != "" {
			lineageVal = sql.NullString{String: c.LineageID, Valid: true}
		}

		stable := c.StableSince
		if stable.IsZero() {
			stable = time.Now().UTC()
		}

		const insertCluster = `INSERT INTO clusters (
			repo_key, cluster_id, centroid, anchor_centroid, member_count, label, summary, lineage_id, stable_since
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

		_, err := tx.ExecContext(ctx, insertCluster,
			c.RepoKey,
			c.ClusterID,
			c.Centroid,
			c.AnchorCentroid,
			c.MemberCount,
			c.Label,
			summaryVal,
			lineageVal,
			stable.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("insert cluster %s/%s: %w", c.RepoKey, c.ClusterID, err)
		}
	}

	// Upsert members
	const insertMember = `INSERT INTO cluster_members (repo_key, fork_key, head_sha, cluster_id, embedding)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repo_key, fork_key) DO UPDATE SET
			head_sha = excluded.head_sha,
			cluster_id = excluded.cluster_id,
			embedding = excluded.embedding`

	for _, m := range members {
		var clusterIDVal sql.NullString
		if m.ClusterID != "" && m.ClusterID != "noise" {
			clusterIDVal = sql.NullString{String: m.ClusterID, Valid: true}
		}
		_, err := tx.ExecContext(ctx, insertMember,
			m.RepoKey,
			m.ForkKey,
			m.HeadSHA,
			clusterIDVal,
			m.Embedding,
		)
		if err != nil {
			return fmt.Errorf("upsert cluster_member %s/%s: %w", m.RepoKey, m.ForkKey, err)
		}
	}

	return tx.Commit(ctx)
}

// GetClusters loads all clusters for a repoKey.
func (s *Store) GetClusters(ctx context.Context, repoKey string) ([]ClusterRecord, error) {
	const query = `SELECT repo_key, cluster_id, centroid, anchor_centroid, member_count, label, summary, lineage_id, stable_since
		FROM clusters WHERE repo_key = ? ORDER BY cluster_id ASC`

	rows, err := s.db.QueryContext(ctx, query, repoKey)
	if err != nil {
		return nil, fmt.Errorf("query clusters %s: %w", repoKey, err)
	}
	defer rows.Close()

	var out []ClusterRecord
	for rows.Next() {
		var c ClusterRecord
		var summaryVal, lineageVal sql.NullString
		var stableSince string

		if err := rows.Scan(
			&c.RepoKey,
			&c.ClusterID,
			&c.Centroid,
			&c.AnchorCentroid,
			&c.MemberCount,
			&c.Label,
			&summaryVal,
			&lineageVal,
			&stableSince,
		); err != nil {
			return nil, err
		}

		if summaryVal.Valid {
			c.Summary = summaryVal.String
		}
		if lineageVal.Valid {
			c.LineageID = lineageVal.String
		}
		if t, perr := time.Parse(time.RFC3339Nano, stableSince); perr == nil {
			c.StableSince = t
		} else if t, perr := time.Parse(time.RFC3339, stableSince); perr == nil {
			c.StableSince = t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetClusterMembers loads all cluster members for a repoKey.
func (s *Store) GetClusterMembers(ctx context.Context, repoKey string) ([]ClusterMemberRecord, error) {
	const query = `SELECT repo_key, fork_key, head_sha, cluster_id, embedding
		FROM cluster_members WHERE repo_key = ?`

	rows, err := s.db.QueryContext(ctx, query, repoKey)
	if err != nil {
		return nil, fmt.Errorf("query cluster_members %s: %w", repoKey, err)
	}
	defer rows.Close()

	var out []ClusterMemberRecord
	for rows.Next() {
		var m ClusterMemberRecord
		var clusterIDVal sql.NullString
		if err := rows.Scan(&m.RepoKey, &m.ForkKey, &m.HeadSHA, &clusterIDVal, &m.Embedding); err != nil {
			return nil, err
		}
		if clusterIDVal.Valid {
			m.ClusterID = clusterIDVal.String
		} else {
			m.ClusterID = "noise"
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertGlobalLineage creates or updates a global lineage prototype and records occurrences.
func (s *Store) UpsertGlobalLineage(ctx context.Context, lineage GlobalLineageRecord, occurrences []LineageOccurrenceRecord) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const insertLineage = `INSERT INTO global_lineage (
		lineage_id, embedder_fp, centroid, label, origin_repo, origin_cluster_id, created_at, last_seen_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(lineage_id) DO UPDATE SET
		embedder_fp = excluded.embedder_fp,
		centroid = excluded.centroid,
		label = excluded.label,
		last_seen_at = excluded.last_seen_at`

	created := lineage.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	lastSeen := lineage.LastSeenAt
	if lastSeen.IsZero() {
		lastSeen = created
	}

	_, err = tx.ExecContext(ctx, insertLineage,
		lineage.LineageID,
		lineage.EmbedderFP,
		lineage.Centroid,
		lineage.Label,
		lineage.OriginRepo,
		lineage.OriginClusterID,
		created.UTC().Format(time.RFC3339Nano),
		lastSeen.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert global_lineage %s: %w", lineage.LineageID, err)
	}

	const insertOccurrence = `INSERT INTO lineage_occurrences (
		lineage_id, repo_key, cluster_id, member_count, first_seen_at, last_seen_at
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(lineage_id, repo_key, cluster_id) DO UPDATE SET
		member_count = excluded.member_count,
		last_seen_at = excluded.last_seen_at`

	for _, occ := range occurrences {
		first := occ.FirstSeenAt
		if first.IsZero() {
			first = time.Now().UTC()
		}
		last := occ.LastSeenAt
		if last.IsZero() {
			last = first
		}
		_, err := tx.ExecContext(ctx, insertOccurrence,
			occ.LineageID,
			occ.RepoKey,
			occ.ClusterID,
			occ.MemberCount,
			first.UTC().Format(time.RFC3339Nano),
			last.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("upsert lineage_occurrence %s/%s: %w", occ.LineageID, occ.RepoKey, err)
		}
	}

	return tx.Commit(ctx)
}

// GetGlobalLineages loads all global lineages.
func (s *Store) GetGlobalLineages(ctx context.Context) ([]GlobalLineageRecord, error) {
	const query = `SELECT lineage_id, embedder_fp, centroid, label, origin_repo, origin_cluster_id, created_at, last_seen_at
		FROM global_lineage ORDER BY lineage_id ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query global_lineage: %w", err)
	}
	defer rows.Close()

	var out []GlobalLineageRecord
	for rows.Next() {
		var l GlobalLineageRecord
		var createdAt, lastSeenAt string
		if err := rows.Scan(
			&l.LineageID,
			&l.EmbedderFP,
			&l.Centroid,
			&l.Label,
			&l.OriginRepo,
			&l.OriginClusterID,
			&createdAt,
			&lastSeenAt,
		); err != nil {
			return nil, err
		}
		if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
			l.CreatedAt = t
		}
		if t, perr := time.Parse(time.RFC3339Nano, lastSeenAt); perr == nil {
			l.LastSeenAt = t
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLineageOccurrences loads all occurrences for a specific lineage ID.
func (s *Store) GetLineageOccurrences(ctx context.Context, lineageID string) ([]LineageOccurrenceRecord, error) {
	const query = `SELECT lineage_id, repo_key, cluster_id, member_count, first_seen_at, last_seen_at
		FROM lineage_occurrences WHERE lineage_id = ? ORDER BY repo_key ASC, cluster_id ASC`

	rows, err := s.db.QueryContext(ctx, query, lineageID)
	if err != nil {
		return nil, fmt.Errorf("query lineage_occurrences %s: %w", lineageID, err)
	}
	defer rows.Close()

	var out []LineageOccurrenceRecord
	for rows.Next() {
		var o LineageOccurrenceRecord
		var firstSeenAt, lastSeenAt string
		if err := rows.Scan(
			&o.LineageID,
			&o.RepoKey,
			&o.ClusterID,
			&o.MemberCount,
			&firstSeenAt,
			&lastSeenAt,
		); err != nil {
			return nil, err
		}
		if t, perr := time.Parse(time.RFC3339Nano, firstSeenAt); perr == nil {
			o.FirstSeenAt = t
		}
		if t, perr := time.Parse(time.RFC3339Nano, lastSeenAt); perr == nil {
			o.LastSeenAt = t
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
