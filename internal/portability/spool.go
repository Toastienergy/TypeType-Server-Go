package portability

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	_ "modernc.org/sqlite"
)

// Limits are PortabilityLimits.
const (
	MaxUploadBytes          = 512 << 20
	MaxArchiveEntries       = 10_000
	MaxArchiveExpandedBytes = 2 << 30
	MaxArchiveEntryBytes    = 512 << 20
	MaxCompressionRatio     = 200
	ProbeBytes              = 64 << 10
	MaxRecords              = 2_000_000
	MaxRecordJSONBytes      = 2 << 20
	MaxContainerRecords     = 100_000
	commitInterval          = 500
)

// Sink receives decoded records; Source gives them back by category in insertion order.
type Sink interface {
	MarkCategory(Category) error
	Write(*Record) (bool, error)
	Issue(Issue) error
	PutLookup(namespace, key, value string) error
	Lookup(namespace, key string) (string, error)
}

type Source interface {
	Counts() (map[Category]int64, error)
	ForEach(Category, func(*Record) error) error
	ForEachChild(c Category, parent string, fn func(*Record) error) error
}

func stableHash(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:])
}

// Spool is PortabilitySpool: a temporary SQLite file holding the records of one job, unique per category and
// stable key, committed every 500 writes.
type Spool struct {
	path     string
	db       *sql.DB
	tx       *sql.Tx
	pending  int
	attempts int64
	marked   map[Category]bool
}

func CreateSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "portability-*.sqlite")
	if err != nil {
		return nil, err
	}
	f.Close()
	d, err := sql.Open("sqlite", "file:"+f.Name()+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(4)
	for _, q := range []string{
		`CREATE TABLE records(ordinal INTEGER PRIMARY KEY AUTOINCREMENT, category TEXT NOT NULL, stable_key TEXT NOT NULL,
parent_key TEXT, payload TEXT NOT NULL, UNIQUE(category, stable_key))`,
		"CREATE INDEX records_parent_idx ON records(category, parent_key, ordinal)",
		"CREATE TABLE categories(category TEXT PRIMARY KEY)",
		"CREATE TABLE lookups(namespace TEXT NOT NULL, lookup_key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(namespace, lookup_key))",
		"CREATE TABLE issues(category TEXT NOT NULL, code TEXT NOT NULL, message TEXT NOT NULL, item_count INTEGER NOT NULL, UNIQUE(category, code, message))",
	} {
		if _, err := d.Exec(q); err != nil {
			d.Close()
			return nil, err
		}
	}
	return &Spool{path: f.Name(), db: d, marked: map[Category]bool{}}, nil
}

func (s *Spool) exec(q string, args ...any) (sql.Result, error) {
	if s.tx == nil {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		s.tx = tx
	}
	r, err := s.tx.Exec(q, args...)
	if err != nil {
		return nil, err
	}
	s.pending++
	if s.pending >= commitInterval {
		return r, s.Flush()
	}
	return r, nil
}

func (s *Spool) Flush() error {
	if s.tx == nil {
		return nil
	}
	err := s.tx.Commit()
	s.tx, s.pending = nil, 0
	return err
}

func (s *Spool) MarkCategory(c Category) error {
	if s.marked[c] {
		return nil
	}
	s.marked[c] = true
	_, err := s.exec("INSERT OR IGNORE INTO categories(category) VALUES (?)", string(c))
	return err
}

func (s *Spool) Write(r *Record) (bool, error) {
	if err := s.MarkCategory(r.Category()); err != nil {
		return false, err
	}
	if s.attempts >= MaxRecords {
		return false, invalid("Backup contains too many records")
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if len(payload) > MaxRecordJSONBytes {
		return false, invalid("Backup record is too large")
	}
	var parent any
	if p := r.ParentKey(); p != "" || r.Type == TypeGroupMembership || r.Type == TypePlaylistVideo {
		parent = stableHash(p)
	}
	s.attempts++
	res, err := s.exec("INSERT OR IGNORE INTO records(category, stable_key, parent_key, payload) VALUES (?, ?, ?, ?)",
		string(r.Category()), stableHash(r.StableKey()), parent, string(payload))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Spool) Issue(i Issue) error {
	if i.Code == "" || i.Message == "" || i.Count <= 0 {
		return invalid("Invalid portability issue")
	}
	_, err := s.exec(`INSERT INTO issues(category, code, message, item_count) VALUES (?, ?, ?, ?)
ON CONFLICT(category, code, message) DO UPDATE SET item_count = item_count + excluded.item_count`, string(i.Category), i.Code, i.Message, i.Count)
	return err
}

func (s *Spool) PutLookup(namespace, key, value string) error {
	if namespace == "" || lower(key) == "" {
		return invalid("Invalid lookup")
	}
	if len(value) > MaxRecordJSONBytes {
		return invalid("Lookup value is too large")
	}
	_, err := s.exec("INSERT OR REPLACE INTO lookups(namespace, lookup_key, value) VALUES (?, ?, ?)", namespace, stableHash(lower(key)), value)
	return err
}

func (s *Spool) Lookup(namespace, key string) (string, error) {
	if err := s.Flush(); err != nil {
		return "", err
	}
	var v string
	err := s.db.QueryRow("SELECT value FROM lookups WHERE namespace = ? AND lookup_key = ?", namespace, stableHash(lower(key))).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Spool) Counts() (map[Category]int64, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT categories.category, COUNT(records.ordinal) FROM categories
LEFT JOIN records ON records.category = categories.category GROUP BY categories.category ORDER BY categories.category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Category]int64{}
	for rows.Next() {
		var c string
		var n int64
		if err := rows.Scan(&c, &n); err != nil {
			return nil, err
		}
		out[Category(c)] = n
	}
	return out, rows.Err()
}

func (s *Spool) Duplicates() (int64, error) {
	counts, err := s.Counts()
	if err != nil {
		return 0, err
	}
	total := int64(0)
	for _, n := range counts {
		total += n
	}
	return s.attempts - total, nil
}

func (s *Spool) Issues() ([]Issue, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT category, code, message, item_count FROM issues ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		var i Issue
		var c string
		if err := rows.Scan(&c, &i.Code, &i.Message, &i.Count); err != nil {
			return nil, err
		}
		i.Category = Category(c)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Spool) each(q string, fn func(*Record) error, args ...any) error {
	if err := s.Flush(); err != nil {
		return err
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var r Record
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			return err
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Spool) ForEach(c Category, fn func(*Record) error) error {
	return s.each("SELECT payload FROM records WHERE category = ? ORDER BY ordinal", fn, string(c))
}

func (s *Spool) ForEachChild(c Category, parent string, fn func(*Record) error) error {
	return s.each("SELECT payload FROM records WHERE category = ? AND parent_key = ? ORDER BY ordinal", fn, string(c), stableHash(lower(parent)))
}

func (s *Spool) Close() error {
	s.Flush()
	return s.db.Close()
}

func (s *Spool) Delete() {
	s.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(s.path + suffix)
	}
}
