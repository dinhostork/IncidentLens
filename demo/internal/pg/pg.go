// Package pg wraps a pgx connection pool so that every statement gets a
// database client span with the attributes docs/telemetry-conventions.md
// relies on (db.system.name, server.address), and runs through the fault hook.
//
// Spans start before a connection is taken from the pool, so time spent
// waiting for a free connection is part of the caller's view of the call.
package pg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
)

const scope = "github.com/dinhostork/IncidentLens/demo/internal/pg"

// Dependency is the name the fault hook sees for Postgres calls.
const Dependency = "postgres"

// Querier runs statements. DB and the transaction passed to DB.InTx both
// implement it.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB is an instrumented connection pool.
type DB struct {
	conn
	pool *pgxpool.Pool
}

// Open creates a pool for dsn and checks that the database answers.
func Open(ctx context.Context, dsn string, hook fault.Hook) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s: %w", cfg.ConnConfig.Host, err)
	}

	cc := cfg.ConnConfig
	in := &instr{
		tracer: otel.Tracer(scope),
		hook:   hook,
		attrs: []attribute.KeyValue{
			semconv.DBSystemNamePostgreSQL,
			semconv.DBNamespace(cc.Database),
			semconv.ServerAddress(cc.Host),
			semconv.ServerPort(int(cc.Port)),
		},
	}
	if err := registerPoolMetrics(pool, cc.Host+"/"+cc.Database); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{conn: conn{q: pool, in: in}, pool: pool}, nil
}

// Close closes the pool.
func (db *DB) Close() { db.pool.Close() }

// InTx runs fn in a transaction, committing if fn returns nil.
func (db *DB) InTx(ctx context.Context, fn func(Querier) error) (err error) {
	var tx pgx.Tx
	err = db.in.run(ctx, "BEGIN", func(ctx context.Context) (err error) {
		tx, err = db.pool.Begin(ctx)
		return err
	})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(conn{q: tx, in: db.in}); err != nil {
		return err
	}
	return db.in.run(ctx, "COMMIT", tx.Commit)
}

type pgxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type conn struct {
	q  pgxQuerier
	in *instr
}

func (c conn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := c.in.run(ctx, sql, func(ctx context.Context) (err error) {
		tag, err = c.q.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

func (c conn) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	ctx, span := c.in.start(ctx, sql)
	if err := c.in.before(ctx, sql); err != nil {
		c.in.end(span, err)
		return nil, err
	}
	rows, err := c.q.Query(ctx, sql, args...)
	if err != nil {
		c.in.end(span, err)
		return nil, err
	}
	return &tracedRows{Rows: rows, end: func(err error) { c.in.end(span, err) }}, nil
}

func (c conn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := c.Query(ctx, sql, args...)
	return &row{rows: rows, err: err}
}

type instr struct {
	tracer trace.Tracer
	hook   fault.Hook
	attrs  []attribute.KeyValue
}

func (in *instr) start(ctx context.Context, sql string) (context.Context, trace.Span) {
	op, coll := summarize(sql)
	name := op
	attrs := append(make([]attribute.KeyValue, 0, len(in.attrs)+3), in.attrs...)
	attrs = append(attrs, semconv.DBQueryText(sql))
	if op != "" {
		attrs = append(attrs, semconv.DBOperationName(op))
	}
	if coll != "" {
		attrs = append(attrs, semconv.DBCollectionName(coll))
		name += " " + coll
	}
	if name == "" {
		name = "postgresql"
	}
	return in.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

func (in *instr) before(ctx context.Context, sql string) error {
	op, _ := summarize(sql)
	return in.hook.Before(ctx, fault.Point{Kind: fault.Outbound, Dependency: Dependency, Operation: op})
}

func (in *instr) run(ctx context.Context, sql string, fn func(context.Context) error) error {
	ctx, span := in.start(ctx, sql)
	err := in.before(ctx, sql)
	if err == nil {
		err = fn(ctx)
	}
	in.end(span, err)
	return err
}

func (in *instr) end(span trace.Span, err error) {
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr):
			span.SetAttributes(semconv.DBResponseStatusCode(pgErr.Code), semconv.ErrorTypeKey.String(pgErr.Code))
		case errors.Is(err, context.DeadlineExceeded):
			span.SetAttributes(semconv.ErrorTypeKey.String("timeout"))
		case errors.Is(err, context.Canceled):
			span.SetAttributes(semconv.ErrorTypeKey.String("canceled"))
		default:
			span.SetAttributes(semconv.ErrorTypeKey.String(fmt.Sprintf("%T", err)))
		}
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// tracedRows ends the span when the rows are closed.
type tracedRows struct {
	pgx.Rows
	once sync.Once
	end  func(error)
}

func (r *tracedRows) Close() {
	r.Rows.Close()
	r.once.Do(func() { r.end(r.Err()) })
}

// row mirrors pgx's QueryRow semantics on top of tracedRows.
type row struct {
	rows pgx.Rows
	err  error
}

func (r *row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	r.rows.Close()
	return r.rows.Err()
}

func registerPoolMetrics(pool *pgxpool.Pool, poolName string) error {
	m := otel.Meter(scope)
	count, err := m.Int64ObservableUpDownCounter("db.client.connection.count",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Connections in the pool, by state."))
	if err != nil {
		return err
	}
	maxConns, err := m.Int64ObservableUpDownCounter("db.client.connection.max",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Maximum number of connections the pool allows."))
	if err != nil {
		return err
	}
	name := attribute.String("db.client.connection.pool.name", poolName)
	idle := metric.WithAttributes(name, attribute.String("db.client.connection.state", "idle"))
	used := metric.WithAttributes(name, attribute.String("db.client.connection.state", "used"))
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		o.ObserveInt64(count, int64(s.IdleConns()), idle)
		o.ObserveInt64(count, int64(s.AcquiredConns()), used)
		o.ObserveInt64(maxConns, int64(s.MaxConns()), metric.WithAttributes(name))
		return nil
	}, count, maxConns)
	return err
}
