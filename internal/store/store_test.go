package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

func memory(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seen(t *testing.T, s *Store, feed string, keys ...string) bool {
	t.Helper()
	ok, err := s.IsSeen(feed, keys)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func i64(v int64) *int64 { return &v }

func raw(t *testing.T, path, script string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(script); err != nil {
		t.Fatal(err)
	}
}

func TestCreatedVersionedAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Transaction(false, func(tx *Store) error { return tx.MarkSeen("news", []string{"g:1"}, 1000) })
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !seen(t, s, "news", "g:1") {
		t.Error("nyckeln försvann")
	}
	if v, _ := s.UserVersion(); v != 3 {
		t.Errorf("schemaversion %d", v)
	}
	if mode, _ := s.JournalMode(); mode != "wal" {
		t.Errorf("journalläge %q", mode)
	}
}

func TestNewerDatabaseRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	raw(t, path, "pragma user_version = 99")
	_, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "schemaversion 99") {
		t.Errorf("fick %v", err)
	}
}

func TestTransactionsRollBack(t *testing.T) {
	s := memory(t)
	s.Transaction(true, func(tx *Store) error { return tx.MarkSeen("news", []string{"g:1"}, 1000) })
	if seen(t, s, "news", "g:1") {
		t.Error("torrläget sparade")
	}
	err := s.Transaction(false, func(tx *Store) error {
		tx.MarkSeen("news", []string{"g:2"}, 1000)
		return errors.New("avbrutet")
	})
	if err == nil || seen(t, s, "news", "g:2") {
		t.Error("felet rullade inte tillbaka")
	}
}

func TestMarkSeenAndPrune(t *testing.T) {
	s := memory(t)
	s.MarkSeen("news", []string{"g:1", "l:a"}, 1000)
	s.MarkSeen("news", []string{"g:2"}, 1000)
	s.MarkSeen("news", []string{"g:1"}, 5000)
	s.MarkSeen("other", []string{"g:9"}, 1000)
	if n, _ := s.Prune("news", 2000); n != 2 {
		t.Errorf("prune tog %d", n)
	}
	if !seen(t, s, "news", "g:1") || seen(t, s, "news", "l:a", "g:2") || !seen(t, s, "other", "g:9") {
		t.Error("fel nycklar kvar")
	}
}

func TestImportKeys(t *testing.T) {
	s := memory(t)
	if n, _ := s.ImportKeys("news", "https://example.com/feed", []string{"l:a", "l:b"}, 1000); n != 2 {
		t.Errorf("första importen: %d", n)
	}
	if n, _ := s.ImportKeys("news", "https://example.com/feed", []string{"l:b", "l:c"}, 2000); n != 1 {
		t.Errorf("andra importen: %d", n)
	}
	st, _ := s.GetFeed("news")
	if *st.PrimedAt != 1000 || st.URL != "https://example.com/feed" {
		t.Errorf("fick %+v", st)
	}
}

func TestOrphansAndForget(t *testing.T) {
	s := memory(t)
	s.SaveFeed(FeedState{Name: "news", URL: "https://example.com/a", CheckedAt: i64(5000)})
	s.SaveFeed(FeedState{Name: "old", URL: "https://example.com/b", PrimedAt: i64(1000)})
	s.MarkSeen("old", []string{"g:1"}, 1000)
	if o, _ := s.Orphans([]string{"news"}); !reflect.DeepEqual(o, []Orphan{{"old", 1000}}) {
		t.Errorf("fick %+v", o)
	}
	s.Forget("old")
	if o, _ := s.Orphans([]string{"news"}); len(o) != 0 {
		t.Errorf("fick %+v", o)
	}
	if seen(t, s, "old", "g:1") {
		t.Error("nycklarna finns kvar")
	}
}

func TestUpgradeFromVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	raw(t, path, `create table feed (name text primary key, url text not null, etag text, last_modified text, primed_at integer,
             checked_at integer, ok_at integer, failures integer not null default 0) strict;
           create table seen (feed text not null, key text not null, first_seen integer not null, last_seen integer not null,
             primary key (feed, key)) strict, without rowid;
           insert into seen values ('news', 'g:1', 1, 1);
           insert into feed (name, url, primed_at, failures) values ('news', 'https://example.com/feed/', 1, 0);
           pragma user_version = 1;`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(); v != 3 {
		t.Errorf("schemaversion %d", v)
	}
	st, _ := s.GetFeed("news")
	if !seen(t, s, "news", "g:1") || *st.PrimedAt != 1 || st.NewestPublished != nil {
		t.Errorf("fick %+v", st)
	}
	s.SaveFeed(FeedState{Name: "news", URL: "https://example.com/feed/", PrimedAt: i64(1), NewestPublished: i64(5000)})
	if st, _ := s.GetFeed("news"); *st.NewestPublished != 5000 {
		t.Errorf("fick %+v", st)
	}
}

func TestOutbox(t *testing.T) {
	s := memory(t)
	post := func(id string) Post {
		return Post{ID: id, Feed: "news", Title: id, Link: "https://example.com/" + id, Status: id, Language: "sv"}
	}
	ids := func(posts []*Post) []string {
		var out []string
		for _, p := range posts {
			out = append(out, p.ID)
		}
		return out
	}
	if ok, _ := s.Enqueue(post("b"), 2000); !ok {
		t.Error("b")
	}
	if ok, _ := s.Enqueue(post("a"), 1000); !ok {
		t.Error("a")
	}
	if ok, _ := s.Enqueue(post("a"), 3000); ok {
		t.Error("a två gånger")
	}
	if due, _ := s.DuePosts("news", 5000); !reflect.DeepEqual(ids(due), []string{"a", "b"}) {
		t.Errorf("fick %v", ids(due))
	}
	if due, _ := s.DuePosts("news", 1500); !reflect.DeepEqual(ids(due), []string{"a"}) {
		t.Errorf("fick %v", ids(due))
	}
	s.MarkRetry("a", 9000, "timeout")
	s.MarkPosted("b", "https://mastodon.example/@news/1", 6000)
	if due, _ := s.DuePosts("news", 5000); len(due) != 0 {
		t.Errorf("fick %v", ids(due))
	}
	a, _ := s.GetPost("a")
	if a.Attempts != 1 {
		t.Errorf("försök %d", a.Attempts)
	}
	if last, _ := s.LastPosted("news"); *last != 6000 {
		t.Errorf("senast %d", *last)
	}
	if n, _ := s.PostedSince(5000); n != 1 {
		t.Errorf("sedan %d", n)
	}
	if n, _ := s.PrunePosts(2500); n != 1 {
		t.Errorf("rensade %d", n)
	}
	if b, _ := s.GetPost("b"); b != nil {
		t.Error("b finns kvar")
	}
	if a, _ := s.GetPost("a"); a.State != "pending" {
		t.Errorf("a: %s", a.State)
	}
}

func TestInstancesAndNotices(t *testing.T) {
	s := memory(t)
	if in, _ := s.GetInstance("https://mastodon.example"); in != nil {
		t.Error("borde saknas")
	}
	s.SaveInstance("https://mastodon.example", "4.7.3", 11, 1000)
	s.SaveInstance("https://mastodon.example", "4.8.0", 12, 2000)
	if in, _ := s.GetInstance("https://mastodon.example"); *in != (Instance{"4.8.0", 12}) {
		t.Errorf("fick %+v", in)
	}
	if ok, _ := s.FirstNotice("deprecation GET /x", 1000); !ok {
		t.Error("första")
	}
	if ok, _ := s.FirstNotice("deprecation GET /x", 2000); ok {
		t.Error("andra")
	}
}

// Schemat ska bli exakt detsamma som TypeScript-versionens, så att databaserna går att byta mellan.
func TestSchemaGolden(t *testing.T) {
	var golden struct {
		UserVersion int
		Objects     []struct {
			Type, Name string
			TblName    string `json:"tbl_name"`
			SQL        *string
		}
	}
	testutil.Golden(t, "schema.json", &golden)
	path := filepath.Join(t.TempDir(), "schema.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UserVersion(); v != golden.UserVersion {
		t.Errorf("schemaversion %d, väntade %d", v, golden.UserVersion)
	}
	s.Close()
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	rows, err := db.Query("select type, name, tbl_name, sql from sqlite_schema order by name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for ; rows.Next(); i++ {
		var typ, name, tbl string
		var text sql.NullString
		rows.Scan(&typ, &name, &tbl, &text)
		if i >= len(golden.Objects) {
			t.Fatalf("fler objekt än i TypeScript-versionen: %s", name)
		}
		w := golden.Objects[i]
		if typ != w.Type || name != w.Name || tbl != w.TblName || text.Valid != (w.SQL != nil) || w.SQL != nil && text.String != *w.SQL {
			t.Errorf("%s %s skiljer sig:\nfick    %q\nväntade %v", typ, name, text.String, w.SQL)
		}
	}
	if i != len(golden.Objects) {
		t.Errorf("%d objekt, väntade %d", i, len(golden.Objects))
	}
}
