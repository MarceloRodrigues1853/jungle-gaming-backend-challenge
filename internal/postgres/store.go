// Package postgres implementa a persistência financeira em PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrWalletNotFound indica que a carteira da operação não existe no banco.
var ErrWalletNotFound = errors.New("wallet not found")

// ErrIdempotencyConflict indica que uma identidade externa foi reutilizada com conteúdo diferente.
var ErrIdempotencyConflict = errors.New("idempotency conflict")

// Store agrupa o pool PostgreSQL usado pelos repositórios.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore cria o adaptador a partir de um pool já configurado e validado.
func NewStore(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &Store{pool: pool}, nil
}

// OpenPool cria um pool e confirma a conexão antes de devolvê-lo.
func OpenPool(ctx context.Context, connectionString string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("parse postgres connection string: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// Close libera as conexões mantidas pelo pool.
func (store *Store) Close() {
	if store != nil && store.pool != nil {
		store.pool.Close()
	}
}
