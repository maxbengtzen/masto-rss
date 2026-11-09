// Package store är databasen: SQLite (modernc.org/sqlite, utan cgo), en fil i DATA_DIR.
//
// `feed` har ett tillstånd per flöde (ETag, senaste hämtning, fel i rad, senaste
// publiceringsdatum). `seen` har varje nyckel (GUID eller normaliserad länk) som setts i ett
// flöde, med när den senast fanns kvar i flödet, så att gamla nycklar kan rensas utan att något
// som fortfarande finns i flödet glöms bort.
//
// `post` är utkorgen: ett inlägg sparas som "pending" i samma transaktion som posten markeras som
// sedd, och blir "posted" eller "failed" när Mastodon svarat. Id:t skickas som Idempotency-Key.
// `instance` minns varje instans version, så att en uppgradering märks. `notice` håller reda på
// aviseringar som bara ska skickas en gång.
//
// Schemat versioneras med PRAGMA user_version och är detsamma som i TypeScript-versionen, så att
// en befintlig databas öppnas som den är. Allt som hör till ett varv skrivs i en transaktion; i
// torrläge rullas den tillbaka.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

var migrations = []string{
	`create table feed (
     name text primary key,
     url text not null,
     etag text,
     last_modified text,
     primed_at integer,
     checked_at integer,
     ok_at integer,
     failures integer not null default 0
   ) strict;
   create table seen (
     feed text not null,
     key text not null,
     first_seen integer not null,
     last_seen integer not null,
     primary key (feed, key)
   ) strict, without rowid;`,
	`create table post (
     id text primary key,
     feed text not null,
     title text not null,
     link text,
     status text not null,
     language text,
     state text not null default 'pending',
     attempts integer not null default 0,
     next_attempt_at integer not null,
     created_at integer not null,
     posted_at integer,
     status_url text,
     error text
   ) strict;
   create index post_due on post (feed, state, next_attempt_at);
   create table instance (
     url text primary key,
     version text not null,
     api_version integer not null,
     checked_at integer not null
   ) strict;
   create table notice (
     key text primary key,
     sent_at integer not null
   ) strict;`,
	`alter table feed add column newest_published integer;`,
}

// Post är ett inlägg i utkorgen.
type Post struct {
	ID            string
	Feed          string
	Title         string
	Link          string // tom = ingen
	Status        string
	Language      string // tom = inget
	State         string // pending, posted eller failed
	Attempts      int
	NextAttemptAt int64
	CreatedAt     int64
	PostedAt      *int64
	StatusURL     string
	Error         string
}

// FeedState är ett flödes tillstånd.
type FeedState struct {
	Name         string
	URL          string
	ETag         string
	LastModified string
	// PrimedAt är när flödet lästes in första gången eller importerades. nil = nytt flöde.
	PrimedAt  *int64
	CheckedAt *int64
	OkAt      *int64
	// Failures är misslyckade hämtningar i rad.
	Failures int
	// NewestPublished är det senaste publiceringsdatumet bland flödets poster hittills (ms, aldrig
	// senare än hämtningen).
	NewestPublished *int64
}

// Store är databasen. Alla metoder går att anropa från flera goroutiner: de tar en i taget, och
// en transaktion håller låset tills den är klar, som när TypeScript-versionen körde allt i en tråd.
type Store struct {
	mu     *sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	locked bool // sant inuti en transaktion, där låset redan är taget
}

// Open öppnar (och skapar eller uppgraderar) databasen.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// En enda anslutning: ":memory:" är en databas per anslutning, och allt ska ske i ordning.
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{mu: &sync.Mutex{}, db: db, conn: conn}
	if _, err := conn.ExecContext(context.Background(), "pragma journal_mode = wal; pragma synchronous = normal; pragma busy_timeout = 5000;"); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) lock() func() {
	if s.locked {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	return s.conn.ExecContext(context.Background(), query, args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	return s.conn.QueryRowContext(context.Background(), query, args...)
}

func (s *Store) migrate() error {
	var version int
	if err := s.queryRow("pragma user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("Databasen har schemaversion %d, men den här versionen av masto-rss känner bara till %d", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		err := s.Transaction(false, func(tx *Store) error {
			if _, err := tx.exec(migrations[v]); err != nil {
				return err
			}
			_, err := tx.exec(fmt.Sprintf("pragma user_version = %d", v+1))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Transaction kör fn i en transaktion. Med rollback sparas ingenting (torrläge). fn får en Store
// som delar transaktionen.
func (s *Store) Transaction(rollback bool, fn func(tx *Store) error) error {
	defer s.lock()()
	if _, err := s.exec("begin immediate"); err != nil {
		return err
	}
	tx := &Store{mu: s.mu, db: s.db, conn: s.conn, locked: true}
	err := fn(tx)
	if err != nil || rollback {
		if _, rerr := s.exec("rollback"); rerr != nil && err == nil {
			return rerr
		}
		return err
	}
	_, err = s.exec("commit")
	return err
}

// UserVersion är schemaversionen.
func (s *Store) UserVersion() (int, error) {
	defer s.lock()()
	var v int
	err := s.queryRow("pragma user_version").Scan(&v)
	return v, err
}

// JournalMode är databasens journalläge (wal).
func (s *Store) JournalMode() (string, error) {
	defer s.lock()()
	var mode string
	err := s.queryRow("pragma journal_mode").Scan(&mode)
	return mode, err
}

func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func ptrInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// GetFeed ger flödets tillstånd, eller nil om flödet är okänt.
func (s *Store) GetFeed(name string) (*FeedState, error) {
	defer s.lock()()
	var st FeedState
	var etag, lastModified sql.NullString
	var primed, checked, ok, newest sql.NullInt64
	err := s.queryRow("select name, url, etag, last_modified, primed_at, checked_at, ok_at, failures, newest_published from feed where name = ?", name).
		Scan(&st.Name, &st.URL, &etag, &lastModified, &primed, &checked, &ok, &st.Failures, &newest)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.ETag, st.LastModified = etag.String, lastModified.String
	st.PrimedAt, st.CheckedAt, st.OkAt, st.NewestPublished = ptrInt(primed), ptrInt(checked), ptrInt(ok), ptrInt(newest)
	return &st, nil
}

// SaveFeed sparar flödets tillstånd.
func (s *Store) SaveFeed(st FeedState) error {
	defer s.lock()()
	_, err := s.exec(`insert into feed (name, url, etag, last_modified, primed_at, checked_at, ok_at, failures, newest_published)
       values (?, ?, ?, ?, ?, ?, ?, ?, ?)
       on conflict (name) do update set url = excluded.url, etag = excluded.etag,
         last_modified = excluded.last_modified, primed_at = excluded.primed_at,
         checked_at = excluded.checked_at, ok_at = excluded.ok_at, failures = excluded.failures,
         newest_published = excluded.newest_published`,
		st.Name, st.URL, nullStr(st.ETag), nullStr(st.LastModified), nullInt(st.PrimedAt),
		nullInt(st.CheckedAt), nullInt(st.OkAt), st.Failures, nullInt(st.NewestPublished))
	return err
}

// IsSeen är sant om någon av nycklarna har setts i flödet.
func (s *Store) IsSeen(feed string, keys []string) (bool, error) {
	defer s.lock()()
	for _, key := range keys {
		var one int
		err := s.queryRow("select 1 from seen where feed = ? and key = ?", feed, key).Scan(&one)
		if err == nil {
			return true, nil
		}
		if err != sql.ErrNoRows {
			return false, err
		}
	}
	return false, nil
}

// MarkSeen sparar nycklarna som sedda nu. Finns de redan flyttas bara "senast sedd" fram.
func (s *Store) MarkSeen(feed string, keys []string, now int64) error {
	defer s.lock()()
	for _, key := range keys {
		if _, err := s.exec(`insert into seen (feed, key, first_seen, last_seen) values (?, ?, ?, ?)
       on conflict (feed, key) do update set last_seen = excluded.last_seen`, feed, key, now, now); err != nil {
			return err
		}
	}
	return nil
}

func changes(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Prune tar bort nycklar som inte funnits i flödet sedan before. Returnerar antalet.
func (s *Store) Prune(feed string, before int64) (int, error) {
	defer s.lock()()
	return changes(s.exec("delete from seen where feed = ? and last_seen < ?", feed, before))
}

// ImportKeys importerar nycklar (till exempel länkar från den gamla botens tillståndsfil) och
// markerar flödet som inläst, så att första hämtningen inte behandlas som ett nytt flöde.
// Returnerar antalet nycklar som inte fanns sedan tidigare.
func (s *Store) ImportKeys(feed, url string, keys []string, now int64) (int, error) {
	state, err := s.GetFeed(feed)
	if err != nil {
		return 0, err
	}
	next := FeedState{Name: feed, URL: url, PrimedAt: &now}
	if state != nil {
		next = *state
		if next.PrimedAt == nil {
			next.PrimedAt = &now
		}
	}
	if err := s.SaveFeed(next); err != nil {
		return 0, err
	}
	defer s.lock()()
	added := 0
	for _, key := range keys {
		n, err := changes(s.exec("insert into seen (feed, key, first_seen, last_seen) values (?, ?, ?, ?) on conflict do nothing", feed, key, now, now))
		if err != nil {
			return added, err
		}
		added += n
	}
	return added, nil
}

// Orphan är ett flöde i databasen som inte finns i konfigurationen.
type Orphan struct {
	Name     string
	LastUsed int64
}

// Orphans ger flöden i databasen som inte finns i konfigurationen, med när de senast hämtades.
func (s *Store) Orphans(configured []string) ([]Orphan, error) {
	defer s.lock()()
	if configured == nil {
		configured = []string{}
	}
	names, _ := json.Marshal(configured)
	rows, err := s.conn.QueryContext(context.Background(), `select name, coalesce(checked_at, primed_at, 0) as last_used from feed
         where name not in (select value from json_each(?)) order by name`, string(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Orphan
	for rows.Next() {
		var o Orphan
		if err := rows.Scan(&o.Name, &o.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Forget glömmer ett flöde helt: tillståndet och alla nycklar.
func (s *Store) Forget(feed string) error {
	defer s.lock()()
	if _, err := s.exec("delete from seen where feed = ?", feed); err != nil {
		return err
	}
	_, err := s.exec("delete from feed where name = ?", feed)
	return err
}

// Enqueue lägger ett inlägg i utkorgen. Ett id som redan finns lämnas orört. Sant om det lades till.
func (s *Store) Enqueue(p Post, now int64) (bool, error) {
	defer s.lock()()
	n, err := changes(s.exec(`insert into post (id, feed, title, link, status, language, next_attempt_at, created_at)
         values (?, ?, ?, ?, ?, ?, ?, ?) on conflict do nothing`,
		p.ID, p.Feed, p.Title, nullStr(p.Link), p.Status, nullStr(p.Language), now, now))
	return n > 0, err
}

const postColumns = "id, feed, title, link, status, language, state, attempts, next_attempt_at, created_at, posted_at, status_url, error"

func scanPost(scan func(...any) error) (*Post, error) {
	var p Post
	var link, language, statusURL, errText sql.NullString
	var posted sql.NullInt64
	if err := scan(&p.ID, &p.Feed, &p.Title, &link, &p.Status, &language, &p.State, &p.Attempts, &p.NextAttemptAt, &p.CreatedAt, &posted, &statusURL, &errText); err != nil {
		return nil, err
	}
	p.Link, p.Language, p.StatusURL, p.Error = link.String, language.String, statusURL.String, errText.String
	p.PostedAt = ptrInt(posted)
	return &p, nil
}

// DuePosts ger väntande inlägg för flödet som får skickas nu, äldst först.
func (s *Store) DuePosts(feed string, now int64) ([]*Post, error) {
	defer s.lock()()
	rows, err := s.conn.QueryContext(context.Background(),
		"select "+postColumns+" from post where feed = ? and state = 'pending' and next_attempt_at <= ? order by created_at, rowid", feed, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPost ger ett inlägg, eller nil.
func (s *Store) GetPost(id string) (*Post, error) {
	defer s.lock()()
	p, err := scanPost(s.queryRow("select "+postColumns+" from post where id = ?", id).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// MarkPosted markerar ett inlägg som postat.
func (s *Store) MarkPosted(id, statusURL string, now int64) error {
	defer s.lock()()
	_, err := s.exec("update post set state = 'posted', posted_at = ?, status_url = ?, error = null, attempts = attempts + 1 where id = ?", now, statusURL, id)
	return err
}

// MarkRetry låter ett inlägg vänta till nextAttemptAt.
func (s *Store) MarkRetry(id string, nextAttemptAt int64, errText string) error {
	defer s.lock()()
	_, err := s.exec("update post set attempts = attempts + 1, next_attempt_at = ?, error = ? where id = ?", nextAttemptAt, errText, id)
	return err
}

// MarkFailed markerar ett inlägg som misslyckat för gott.
func (s *Store) MarkFailed(id, errText string) error {
	defer s.lock()()
	_, err := s.exec("update post set state = 'failed', attempts = attempts + 1, error = ? where id = ?", errText, id)
	return err
}

// LastPosted är senaste lyckade inlägget för flödet (ms), eller nil.
func (s *Store) LastPosted(feed string) (*int64, error) {
	defer s.lock()()
	var at sql.NullInt64
	err := s.queryRow("select max(posted_at) as at from post where feed = ? and state = 'posted'", feed).Scan(&at)
	return ptrInt(at), err
}

// PostedSince är antalet lyckade inlägg sedan since (ms), för alla flöden.
func (s *Store) PostedSince(since int64) (int, error) {
	defer s.lock()()
	var n int
	err := s.queryRow("select count(*) as n from post where state = 'posted' and posted_at >= ?", since).Scan(&n)
	return n, err
}

// PrunePosts tar bort skickade och misslyckade inlägg som är äldre än before.
func (s *Store) PrunePosts(before int64) (int, error) {
	defer s.lock()()
	return changes(s.exec("delete from post where state != 'pending' and created_at < ?", before))
}

// Instance är vad som senast lästes om en instans.
type Instance struct {
	Version    string
	APIVersion int
}

// GetInstance ger det sparade om instansen, eller nil.
func (s *Store) GetInstance(url string) (*Instance, error) {
	defer s.lock()()
	var in Instance
	err := s.queryRow("select version, api_version from instance where url = ?", url).Scan(&in.Version, &in.APIVersion)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &in, err
}

// SaveInstance sparar instansens version.
func (s *Store) SaveInstance(url, version string, apiVersion int, now int64) error {
	defer s.lock()()
	_, err := s.exec(`insert into instance (url, version, api_version, checked_at) values (?, ?, ?, ?)
         on conflict (url) do update set version = excluded.version, api_version = excluded.api_version, checked_at = excluded.checked_at`,
		url, version, apiVersion, now)
	return err
}

// FirstNotice är sant första gången för en nyckel; den sparar att aviseringen skickats.
func (s *Store) FirstNotice(key string, now int64) (bool, error) {
	defer s.lock()()
	n, err := changes(s.exec("insert into notice (key, sent_at) values (?, ?) on conflict do nothing", key, now))
	return n > 0, err
}

// Close stänger databasen.
func (s *Store) Close() error {
	if s.conn != nil {
		s.conn.Close()
	}
	return s.db.Close()
}
