// Package store persists pages and API keys in SQLite and HTML on disk.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrSlugExists = errors.New("slug already exists")
	ErrExpired    = errors.New("page already expired")
)

type Page struct {
	Slug         string
	Title        string
	FileName     string
	PasswordHash string
	OwnerKeyID   *int64
	Size         int64
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

type Key struct {
	ID        int64
	Name      string
	Role      string
	CreatedAt time.Time
	RevokedAt *time.Time
}

type Stats struct {
	Pages     int64
	Active    int64
	Expired   int64
	Permanent int64
	Bytes     int64
	Keys      int64
}

type Store struct {
	db       *sql.DB
	pagesDir string
}

func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pagesDir := filepath.Join(abs, "pages")
	if err := os.MkdirAll(pagesDir, 0o750); err != nil {
		return nil, fmt.Errorf("create pages dir: %w", err)
	}
	dbPath := filepath.Join(abs, "flyer.db")
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db, pagesDir: pagesDir}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	key_hash TEXT NOT NULL UNIQUE,
	role TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS pages (
	slug TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	file_name TEXT NOT NULL,
	password_hash TEXT NOT NULL DEFAULT '',
	owner_key_id INTEGER REFERENCES api_keys(id),
	size INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_pages_expires_at ON pages(expires_at);
CREATE INDEX IF NOT EXISTS idx_pages_owner_created ON pages(owner_key_id, created_at DESC, slug DESC);
`
	_, err := s.db.ExecContext(context.Background(), schema)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreatePage(ctx context.Context, p Page, html []byte) error {
	if filepath.Base(p.FileName) != p.FileName || p.FileName == "" {
		return errors.New("invalid page file name")
	}
	tmp, err := os.CreateTemp(s.pagesDir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(html); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	dest := filepath.Join(s.pagesDir, p.FileName)
	if err := os.Link(tmpName, dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrSlugExists
		}
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO pages (slug, title, file_name, password_hash, owner_key_id, size, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Slug, p.Title, p.FileName, p.PasswordHash, p.OwnerKeyID, p.Size,
		p.CreatedAt.Unix(), unixPtr(p.ExpiresAt))
	if err != nil {
		_ = os.Remove(dest)
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed: pages.slug") {
			return ErrSlugExists
		}
		return err
	}
	return nil
}

func (s *Store) OpenPage(fileName string) (*os.File, error) {
	if filepath.Base(fileName) != fileName || fileName == "" {
		return nil, errors.New("invalid page file name")
	}
	//nolint:gosec // fileName 已由 Base == fileName 校验，限定在 pagesDir 内
	return os.Open(filepath.Join(s.pagesDir, fileName))
}

const pageColumns = `slug, title, file_name, password_hash, owner_key_id, size, created_at, expires_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPage(row rowScanner) (*Page, error) {
	var (
		p       Page
		owner   sql.NullInt64
		created int64
		expires sql.NullInt64
	)
	if err := row.Scan(&p.Slug, &p.Title, &p.FileName, &p.PasswordHash, &owner, &p.Size, &created, &expires); err != nil {
		return nil, err
	}
	if owner.Valid {
		p.OwnerKeyID = &owner.Int64
	}
	p.CreatedAt = time.Unix(created, 0)
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		p.ExpiresAt = &t
	}
	return &p, nil
}

func (s *Store) GetPage(ctx context.Context, slug string) (*Page, error) {
	p, err := scanPage(s.db.QueryRowContext(ctx, `
SELECT `+pageColumns+`
FROM pages WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListPages returns pages newest first. A nil owner lists every page.
func (s *Store) ListPages(ctx context.Context, ownerKeyID *int64, limit, offset int) ([]Page, int64, error) {
	where, args := "", []any{}
	if ownerKeyID != nil {
		where, args = " WHERE owner_key_id = ?", []any{*ownerKeyID}
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages`+where, args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}
	//nolint:gosec // where 只可能是空串或包内常量，所有值都走占位符
	rows, err := s.db.QueryContext(ctx, `
SELECT `+pageColumns+`
FROM pages`+where+`
ORDER BY created_at DESC, slug DESC
LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	pages := make([]Page, 0, limit)
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, 0, err
		}
		pages = append(pages, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return pages, total, nil
}

// ExpirePage makes a page expire at now without deleting its file.
func (s *Store) ExpirePage(ctx context.Context, slug string, now time.Time) error {
	ts := now.Unix()
	res, err := s.db.ExecContext(ctx, `
UPDATE pages SET expires_at = ?
WHERE slug = ? AND (expires_at IS NULL OR expires_at > ?)`, ts, slug, ts)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE slug = ?`, slug).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrExpired
}

func (s *Store) DeletePage(ctx context.Context, slug string) error {
	var fileName string
	err := s.db.QueryRowContext(ctx, `SELECT file_name FROM pages WHERE slug = ?`, slug).Scan(&fileName)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pages WHERE slug = ?`, slug); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.pagesDir, fileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN expires_at IS NULL OR expires_at > ? THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN expires_at IS NULL THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(size), 0)
FROM pages`, now.Unix(), now.Unix()).
		Scan(&st.Pages, &st.Active, &st.Expired, &st.Permanent, &st.Bytes)
	if err != nil {
		return Stats{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&st.Keys); err != nil {
		return Stats{}, err
	}
	return st, nil
}

// Cleanup deletes pages whose grace period has elapsed.
func (s *Store) Cleanup(ctx context.Context, now time.Time, grace time.Duration) (int, error) {
	cutoff := now.Add(-grace).Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT slug, file_name FROM pages WHERE expires_at IS NOT NULL AND expires_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	type victim struct{ slug, file string }
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.slug, &v.file); err != nil {
			return 0, err
		}
		victims = append(victims, v)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	deleted := 0
	for _, v := range victims {
		if err := os.Remove(filepath.Join(s.pagesDir, v.file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM pages WHERE slug = ?`, v.slug); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (s *Store) CreateKey(ctx context.Context, name, role, keyHash string, now time.Time) (*Key, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO api_keys (name, key_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		name, keyHash, role, now.Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Key{ID: id, Name: name, Role: role, CreatedAt: time.Unix(now.Unix(), 0)}, nil
}

func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, role, created_at, revoked_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []Key
	for rows.Next() {
		var (
			k       Key
			created int64
			revoked sql.NullInt64
		)
		if err := rows.Scan(&k.ID, &k.Name, &k.Role, &created, &revoked); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(created, 0)
		if revoked.Valid {
			t := time.Unix(revoked.Int64, 0)
			k.RevokedAt = &t
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) GetKeyByHash(ctx context.Context, hash string) (*Key, error) {
	var (
		k       Key
		created int64
		revoked sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, role, created_at, revoked_at FROM api_keys WHERE key_hash = ?`, hash).
		Scan(&k.ID, &k.Name, &k.Role, &created, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.CreatedAt = time.Unix(created, 0)
	if revoked.Valid {
		t := time.Unix(revoked.Int64, 0)
		k.RevokedAt = &t
	}
	return &k, nil
}

func (s *Store) RevokeKey(ctx context.Context, id int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now.Unix(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}
