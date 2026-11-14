// Package sqlite implements the app.Store port on SQLite through the pure-Go
// modernc.org/sqlite driver, so lanternfolkd stays one static binary
// (CGO_ENABLED=0). WAL mode, foreign keys on, and immediate transactions:
// each unit of work takes the write lock up front, so it never fails halfway
// on a lock upgrade.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/tolaniverse/lanternfolk/internal/core/workspace"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// Store is an app.Store backed by one SQLite database file.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) and migrates the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	// FULL sync is intentional: an acknowledged chat write must survive a
	// power loss, not just a process restart. WAL/NORMAL is faster but may
	// lose the last committed transaction on a hard crash.
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)", "synchronous(FULL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and this keeps every
	// unit of work strictly serialized. Slice 1 load is far below its limit
	// (Slice 1 Design: ~25 writes/s peak).
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is usable (for readiness).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx runs fn in one transaction; any error rolls everything back.
func (s *Store) Tx(ctx context.Context, fn func(app.Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&tx{ctx: ctx, tx: sqlTx}); err != nil {
		sqlTx.Rollback()
		return err
	}
	return sqlTx.Commit()
}

type tx struct {
	ctx context.Context
	tx  *sql.Tx
}

func (t *tx) exec(q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, q, args...)
}
func (t *tx) row(q string, args ...any) *sql.Row { return t.tx.QueryRowContext(t.ctx, q, args...) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrNotFound
	}
	return err
}

func conflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return app.ErrConflict
	}
	return err
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// ---- users and sessions ----

func (t *tx) CreateUser(u app.User, now time.Time) error {
	_, err := t.exec(`INSERT INTO users (id, handle, created_at) VALUES (?, ?, ?)`, u.ID, u.Handle, ms(now))
	return conflict(err)
}

func (t *tx) UserByHandle(handle string) (app.User, error) {
	var u app.User
	err := t.row(`SELECT id, handle FROM users WHERE handle = ?`, handle).Scan(&u.ID, &u.Handle)
	return u, notFound(err)
}

func (t *tx) CreateSession(tokenHash []byte, userID string, expires time.Time) error {
	_, err := t.exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, ms(expires))
	return err
}

func (t *tx) UserBySession(tokenHash []byte, now time.Time) (app.User, error) {
	var u app.User
	err := t.row(`SELECT u.id, u.handle FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, ms(now)).Scan(&u.ID, &u.Handle)
	return u, notFound(err)
}

// ---- workspaces, gates, members ----

func (t *tx) CreateWorkspace(w app.Workspace, now time.Time) error {
	_, err := t.exec(`INSERT INTO workspaces (id, name, slug, created_at) VALUES (?, ?, ?, ?)`, w.ID, w.Name, w.Slug, ms(now))
	return conflict(err)
}

func (t *tx) workspace(where string, arg any) (app.Workspace, error) {
	var w app.Workspace
	err := t.row(`SELECT id, name, slug, seq FROM workspaces WHERE `+where+` = ?`, arg).Scan(&w.ID, &w.Name, &w.Slug, &w.Seq)
	return w, notFound(err)
}

func (t *tx) WorkspaceBySlug(slug string) (app.Workspace, error) { return t.workspace("slug", slug) }
func (t *tx) WorkspaceByID(id string) (app.Workspace, error)     { return t.workspace("id", id) }

func (t *tx) AddGates(workspaceID string, gates []workspace.Gate) error {
	for i, g := range gates {
		if _, err := t.exec(`INSERT INTO gates (workspace_id, ord, kind, prompt, salt, hash) VALUES (?, ?, ?, ?, ?, ?)`,
			workspaceID, i, g.Kind, g.Prompt, g.Salt, g.Hash); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Gates(workspaceID string) ([]workspace.Gate, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT kind, prompt, salt, hash FROM gates WHERE workspace_id = ? ORDER BY ord`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workspace.Gate
	for rows.Next() {
		var g workspace.Gate
		if err := rows.Scan(&g.Kind, &g.Prompt, &g.Salt, &g.Hash); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (t *tx) AddMember(workspaceID, userID string, role workspace.Role, now time.Time) error {
	_, err := t.exec(`INSERT INTO members (workspace_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		workspaceID, userID, string(role), ms(now))
	return conflict(err)
}

func (t *tx) Role(workspaceID, userID string) (workspace.Role, error) {
	var r string
	err := t.row(`SELECT role FROM members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&r)
	return workspace.Role(r), notFound(err)
}

func (t *tx) Members(workspaceID string) ([]protocol.Member, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT u.id, u.handle, m.role FROM members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = ? ORDER BY m.joined_at, u.handle`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Member{}
	for rows.Next() {
		var m protocol.Member
		var role string
		if err := rows.Scan(&m.UserID, &m.Handle, &role); err != nil {
			return nil, err
		}
		m.Role = protocol.Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- channels and messages ----

func (t *tx) CreateChannel(workspaceID string, c protocol.Channel) error {
	_, err := t.exec(`INSERT INTO channels (workspace_id, id, name) VALUES (?, ?, ?)`, workspaceID, c.ID, c.Name)
	return conflict(err)
}

func (t *tx) ChannelExists(workspaceID, channelID string) (bool, error) {
	var one int
	err := t.row(`SELECT 1 FROM channels WHERE workspace_id = ? AND id = ?`, workspaceID, channelID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (t *tx) Channels(workspaceID string) ([]protocol.Channel, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, name FROM channels WHERE workspace_id = ? ORDER BY name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Channel{}
	for rows.Next() {
		var c protocol.Channel
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const messageCols = `id, channel_id, author_id, body, client_cmd_id, created_at`

func scanMessage(sc interface{ Scan(...any) error }) (protocol.Message, error) {
	var m protocol.Message
	var created int64
	err := sc.Scan(&m.ID, &m.ChannelID, &m.AuthorID, &m.Body, &m.ClientCmdID, &created)
	m.CreatedAt = time.UnixMilli(created).UTC()
	return m, err
}

func (t *tx) MessageByClientCmd(workspaceID, authorID, clientCmdID string) (protocol.Message, uint64, error) {
	r := t.row(`SELECT `+messageCols+`, seq FROM messages WHERE workspace_id = ? AND author_id = ? AND client_cmd_id = ?`,
		workspaceID, authorID, clientCmdID)
	var m protocol.Message
	var created int64
	var seq uint64
	err := r.Scan(&m.ID, &m.ChannelID, &m.AuthorID, &m.Body, &m.ClientCmdID, &created, &seq)
	m.CreatedAt = time.UnixMilli(created).UTC()
	return m, seq, notFound(err)
}

func (t *tx) InsertMessage(workspaceID string, m protocol.Message, seq uint64) error {
	_, err := t.exec(`INSERT INTO messages (workspace_id, id, channel_id, author_id, body, client_cmd_id, seq, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, workspaceID, m.ID, m.ChannelID, m.AuthorID, m.Body, m.ClientCmdID, seq, ms(m.CreatedAt))
	return conflict(err)
}

// RecentMessages returns up to perChannel newest messages of each channel,
// oldest first overall.
func (t *tx) RecentMessages(workspaceID string, perChannel int) ([]protocol.Message, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+messageCols+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY channel_id ORDER BY id DESC) AS rn
			FROM messages WHERE workspace_id = ?)
		WHERE rn <= ? ORDER BY id`, workspaceID, perChannel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- outbox ----

func (t *tx) Append(workspaceID, typ string, payload any) (uint64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var seq uint64
	if err := t.row(`UPDATE workspaces SET seq = seq + 1 WHERE id = ? RETURNING seq`, workspaceID).Scan(&seq); err != nil {
		return 0, notFound(err)
	}
	_, err = t.exec(`INSERT INTO outbox (workspace_id, seq, type, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		workspaceID, seq, typ, string(b), ms(time.Now()))
	return seq, err
}

func (t *tx) events(q string, args ...any) ([]app.Event, error) {
	rows, err := t.tx.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Event
	for rows.Next() {
		var e app.Event
		var p string
		if err := rows.Scan(&e.WorkspaceID, &e.Seq, &e.Type, &p); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(p)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *tx) EventsSince(workspaceID string, since uint64, limit int) ([]app.Event, error) {
	return t.events(`SELECT workspace_id, seq, type, payload FROM outbox WHERE workspace_id = ? AND seq > ?
		ORDER BY seq LIMIT ?`, workspaceID, since, limit)
}

func (t *tx) Unpublished(limit int) ([]app.Event, error) {
	return t.events(`SELECT workspace_id, seq, type, payload FROM outbox WHERE published = 0
		ORDER BY workspace_id, seq LIMIT ?`, limit)
}

func (t *tx) MarkPublished(events []app.Event) error {
	for _, e := range events {
		if _, err := t.exec(`UPDATE outbox SET published = 1 WHERE workspace_id = ? AND seq = ?`, e.WorkspaceID, e.Seq); err != nil {
			return err
		}
	}
	return nil
}
