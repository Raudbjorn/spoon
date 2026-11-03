package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClustersAndLineageStorage(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test-clusters.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	repoKey := "github.com/testowner/testrepo"

	// 1. Cluster Run
	now := time.Now().UTC().Truncate(time.Millisecond)
	run := ClusterRunRecord{
		RepoKey:          repoKey,
		EmbedderFP:       "fp-sha256-abc123",
		ComputedAt:       now,
		LastReassignedAt: now,
		NextClusterSeq:   5,
	}
	if err := db.UpsertClusterRun(ctx, run); err != nil {
		t.Fatalf("UpsertClusterRun failed: %v", err)
	}

	loadedRun, err := db.GetClusterRun(ctx, repoKey)
	if err != nil {
		t.Fatalf("GetClusterRun failed: %v", err)
	}
	if loadedRun == nil {
		t.Fatal("expected non-nil ClusterRunRecord")
	}
	if loadedRun.EmbedderFP != run.EmbedderFP || loadedRun.NextClusterSeq != 5 {
		t.Fatalf("loadedRun mismatch: got %+v, want %+v", loadedRun, run)
	}

	// 2. Clusters & Members
	centroid := []byte{0x01, 0x02, 0x03, 0x04}
	anchor := []byte{0x05, 0x06, 0x07, 0x08}
	clusters := []ClusterRecord{
		{
			RepoKey:        repoKey,
			ClusterID:      "c0",
			Centroid:       centroid,
			AnchorCentroid: anchor,
			MemberCount:    2,
			Label:          "feature: auth",
			Summary:        "Adds OAuth PKCE support",
			LineageID:      "L0001",
			StableSince:    now,
		},
	}
	members := []ClusterMemberRecord{
		{
			RepoKey:   repoKey,
			ForkKey:   repoKey + "/fork1",
			HeadSHA:   "sha111",
			ClusterID: "c0",
			Embedding: []byte{0x10, 0x20, 0x30, 0x40},
		},
		{
			RepoKey:   repoKey,
			ForkKey:   repoKey + "/fork2",
			HeadSHA:   "sha222",
			ClusterID: "noise",
			Embedding: []byte{0x50, 0x60, 0x70, 0x80},
		},
	}

	if err := db.SaveClusters(ctx, repoKey, clusters, members); err != nil {
		t.Fatalf("SaveClusters failed: %v", err)
	}

	loadedClusters, err := db.GetClusters(ctx, repoKey)
	if err != nil {
		t.Fatalf("GetClusters failed: %v", err)
	}
	if len(loadedClusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(loadedClusters))
	}
	if !bytes.Equal(loadedClusters[0].Centroid, centroid) || !bytes.Equal(loadedClusters[0].AnchorCentroid, anchor) {
		t.Fatal("centroid byte mismatch")
	}
	if loadedClusters[0].Label != "feature: auth" || loadedClusters[0].Summary != "Adds OAuth PKCE support" {
		t.Fatalf("cluster metadata mismatch: %+v", loadedClusters[0])
	}

	loadedMembers, err := db.GetClusterMembers(ctx, repoKey)
	if err != nil {
		t.Fatalf("GetClusterMembers failed: %v", err)
	}
	if len(loadedMembers) != 2 {
		t.Fatalf("expected 2 members, got %d", len(loadedMembers))
	}

	// 3. Global Lineage & Occurrences
	lineage := GlobalLineageRecord{
		LineageID:       "L0001",
		EmbedderFP:      "fp-sha256-abc123",
		Centroid:        centroid,
		Label:           "archetype: auth-pkce",
		OriginRepo:      repoKey,
		OriginClusterID: "c0",
		CreatedAt:       now,
		LastSeenAt:      now,
	}
	occurrences := []LineageOccurrenceRecord{
		{
			LineageID:   "L0001",
			RepoKey:     repoKey,
			ClusterID:   "c0",
			MemberCount: 2,
			FirstSeenAt: now,
			LastSeenAt:  now,
		},
	}

	if err := db.UpsertGlobalLineage(ctx, lineage, occurrences); err != nil {
		t.Fatalf("UpsertGlobalLineage failed: %v", err)
	}

	lineages, err := db.GetGlobalLineages(ctx)
	if err != nil {
		t.Fatalf("GetGlobalLineages failed: %v", err)
	}
	if len(lineages) != 1 || lineages[0].LineageID != "L0001" {
		t.Fatalf("unexpected global lineages: %+v", lineages)
	}

	occs, err := db.GetLineageOccurrences(ctx, "L0001")
	if err != nil {
		t.Fatalf("GetLineageOccurrences failed: %v", err)
	}
	if len(occs) != 1 || occs[0].RepoKey != repoKey {
		t.Fatalf("unexpected lineage occurrences: %+v", occs)
	}
}
