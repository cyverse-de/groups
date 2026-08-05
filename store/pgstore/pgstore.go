// Package pgstore implements the group store over the permissions schema of the
// DE database, the same schema the permissions service owns. Groups live beside
// permissions so that expanding a subject to its groups is a join rather than a
// call to another service.
package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cyverse-de/go-mod/logging"
	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

var log = logging.Log.WithFields(logrus.Fields{"package": "pgstore"})

// PostgreSQL error codes we translate into store errors.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
)

var (
	_ store.Store  = (*Store)(nil)
	_ store.Reader = (*reader)(nil)
	_ store.Tx     = (*tx)(nil)
)

// Store is the PostgreSQL implementation of store.Store.
type Store struct {
	*reader
	db         *sql.DB
	userSuffix string
}

// DefaultUserSuffix is the domain appended to a bare username to form the
// public.users.username of the corresponding DE user.
const DefaultUserSuffix = "@iplantcollaborative.org"

// Config carries the settings needed to open the store.
type Config struct {
	URI    string
	Schema string

	// UserSuffix forms a DE username from a bare one when correlating a new
	// subject with its public.users row. Defaults to DefaultUserSuffix.
	UserSuffix string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Open connects to the database and verifies the connection. The schema is
// applied as a connection-level search_path rather than a statement at the top
// of every transaction, which keeps it off the request path.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.URI == "" {
		return nil, errors.New("the database URI must be set in the configuration")
	}
	if cfg.Schema == "" {
		cfg.Schema = "permissions"
	}
	if cfg.UserSuffix == "" {
		cfg.UserSuffix = DefaultUserSuffix
	}

	dsn, err := DSNWithSearchPath(cfg.URI, cfg.Schema)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open the database: %w", err)
	}

	db.SetMaxOpenConns(orDefault(cfg.MaxOpenConns, 10))
	db.SetMaxIdleConns(orDefault(cfg.MaxIdleConns, 5))
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		// The ping failure is the error worth reporting; a close failure on a
		// pool that never connected adds nothing.
		_ = db.Close()
		return nil, fmt.Errorf("could not reach the database; check db.uri and that the service can route to it: %w", err)
	}

	return &Store{reader: &reader{q: db}, db: db, userSuffix: cfg.UserSuffix}, nil
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// DSNWithSearchPath adds search_path to the connection string unless the caller
// already set one. lib/pq forwards unrecognized parameters to the server as
// runtime settings, so this reaches every connection in the pool.
//
// Exported so that tools opening their own pool against the same schema -- the
// Grouper importer -- resolve it identically.
func DSNWithSearchPath(uri, schema string) (string, error) {
	// Keyword/value connection strings are passed through untouched: they have
	// their own syntax, and a caller using one can set search_path themselves.
	if !strings.HasPrefix(uri, "postgres://") && !strings.HasPrefix(uri, "postgresql://") {
		return uri, nil
	}

	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("could not parse the database URI: %w", err)
	}

	q := u.Query()
	if q.Get("search_path") == "" {
		// public stays on the path because uuid_generate_v1, used by the column
		// defaults in the permissions schema, lives there.
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// WithTx runs fn inside a transaction, rolling back if it returns an error or
// panics.
func (s *Store) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("could not begin a transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			// The rollback error is deliberately discarded: fn's error is the one
			// worth reporting, and ErrTxDone here just means the commit won.
			_ = sqlTx.Rollback()
		}
	}()

	if err := fn(&tx{reader: &reader{q: sqlTx}, tx: sqlTx, userSuffix: s.userSuffix}); err != nil {
		return err
	}

	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("could not commit the transaction: %w", err)
	}
	committed = true
	return nil
}

// querier is the subset of database/sql shared by *sql.DB and *sql.Tx, so the
// read queries can be written once and used both inside and outside a
// transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// reader implements the read-only half of the store.
type reader struct{ q querier }

// tx implements the read-write half.
type tx struct {
	*reader
	tx         *sql.Tx
	userSuffix string
}

// translateErr converts a driver error into a store error where there is a
// meaningful equivalent, so handlers can map it to a status code without
// inspecting driver types.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case codeUniqueViolation:
			if pqErr.Constraint == "groups_identity_unique" {
				return fmt.Errorf("%w: a group of that type, owner, and name already exists", store.ErrConflict)
			}
			logWithheldConstraint(pqErr, false)
			return store.ErrConflict
		case codeForeignKeyViolation:
			if pqErr.Constraint == "groups_group_type_owner_present_fkey" {
				return store.ErrOwnerRequired
			}
		case codeCheckViolation:
			msg, known := checkViolationMessages[pqErr.Constraint]
			logWithheldConstraint(pqErr, !known)
			if known {
				return fmt.Errorf("%w: %s", store.ErrInvalid, msg)
			}
			return store.ErrInvalid
		}
	}
	return err
}

// checkViolationMessages restates the schema's value rules in terms a caller can
// act on. Constraint names are deliberately not echoed: they name a table and an
// index the caller cannot do anything about, and they describe the schema to
// anyone who can reach the API.
var checkViolationMessages = map[string]string{
	"groups_name_check":  "a group name cannot be blank",
	"groups_name_check1": "a group name cannot contain ':'",
	"groups_owner_check": "a group owner cannot be blank",
}

// logWithheldConstraint records the constraint the response deliberately omits,
// so a rejection is still traceable from the logs. Rejecting bad input is
// routine; an unexplained constraint is not, and means the schema has gained a
// rule this package cannot describe — the caller got a message naming no
// actionable rule, which is the shape of a support ticket nobody can answer.
func logWithheldConstraint(pqErr *pq.Error, unexplained bool) {
	entry := log.WithFields(logrus.Fields{
		"constraint": pqErr.Constraint,
		"code":       string(pqErr.Code),
		"table":      pqErr.Table,
	})
	if unexplained {
		entry.Warn("rejected a write violating a constraint this package has no message for; " +
			"add it to checkViolationMessages so the caller learns the rule")
		return
	}
	entry.Info("rejected a write violating a schema constraint; the response states the rule, not the constraint")
}

// escapeLike escapes the wildcards in a user-supplied search term so it is
// matched literally.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// nullString maps the empty string to NULL, which is how an absent owner and an
// absent display name are stored.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// groupColumns is the projection every group query shares.
const groupColumns = `
	s.subject_id,
	g.group_type,
	coalesce(g.owner, ''),
	g.name,
	coalesce(g.display_name, ''),
	g.description,
	g.members_public,
	g.created_at,
	g.updated_at`

func scanGroup(row interface{ Scan(...any) error }) (*model.Group, error) {
	var g model.Group
	err := row.Scan(&g.ID, &g.GroupType, &g.Owner, &g.Name, &g.DisplayName,
		&g.Description, &g.MembersPublic, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	return &g, nil
}

func scanGroups(rows *sql.Rows) ([]model.Group, error) {
	// Close reports the same failure rows.Err() does, which is checked below.
	defer func() { _ = rows.Close() }()

	groups := make([]model.Group, 0)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}
	return groups, nil
}
