package diff

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"lzsync/internal/hub"
)

func TestCompareForksRejectsSameFork(t *testing.T) {
	s, err := hub.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := CompareForks(context.Background(), s, "a", "a", []hub.EntityKind{hub.KindCollection}); err == nil {
		t.Fatal("same fork should fail")
	}
}

func TestCompareForksMatchesBeatmapVersionsByOnlineID(t *testing.T) {
	s, err := hub.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	source := hub.BeatmapSet{Hash: "source-hash", OnlineID: 42, Artist: "artist", Title: "title", AuthorUsername: "author"}
	target := hub.BeatmapSet{Hash: "target-hash", OnlineID: 42, Artist: "artist", Title: "title", AuthorUsername: "author"}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		fork string
		key  string
		json []byte
	}{
		{fork: "source", key: source.Hash, json: sourceJSON},
		{fork: "target", key: target.Hash, json: targetJSON},
	} {
		if _, err := s.DB().Exec("INSERT INTO presence(fork_id, kind, content_key, local_values, collected_at) VALUES(?,?,?,?,?)", row.fork, hub.KindBeatmapSet, row.key, row.json, 1); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := CompareForks(context.Background(), s, "source", "target", []hub.EntityKind{hub.KindBeatmapSet})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want one version update", entries)
	}
	entry := entries[0]
	if entry.Category != CategoryVersionUpdate || entry.ContentKey != source.Hash || entry.IdentityKey != "online:42" || entry.Evidence != EvidenceStrong {
		t.Fatalf("entry = %#v, want strong version update", entry)
	}
}
