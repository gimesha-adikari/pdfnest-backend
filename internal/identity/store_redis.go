package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	rdb    *redis.Client
	ttl    time.Duration
	prefix string
}

var DefaultStore *Store

func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}

	s := &Store{
		rdb:    rdb,
		ttl:    ttl,
		prefix: "platen:guest:",
	}
	DefaultStore = s
	return s
}

func GetStore() *Store {
	return DefaultStore
}

func (s *Store) guestKey(id string) string {
	return s.prefix + id
}

func (s *Store) fpKey(fpHash string) string {
	return s.prefix + "fp:" + fpHash
}

func (s *Store) Save(ctx context.Context, g *GuestRecord) error {
	if g == nil || g.ID == "" {
		return fmt.Errorf("invalid guest record")
	}

	g.LastSeenAt = time.Now()

	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, s.guestKey(g.ID), raw, s.ttl)

	if g.FingerprintHash != "" {
		pipe.Set(ctx, s.fpKey(g.FingerprintHash), g.ID, s.ttl)
	}

	_, err = pipe.Exec(ctx)
	return err
}

func (s *Store) LoadByID(ctx context.Context, id string) (*GuestRecord, error) {
	if s == nil || s.rdb == nil {
		return nil, errors.New("redis client not configured")
	}
	raw, err := s.rdb.Get(ctx, s.guestKey(id)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var g GuestRecord
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}

	return &g, nil
}

func (s *Store) LoadByFingerprint(ctx context.Context, fpHash string) (*GuestRecord, error) {
	if fpHash == "" {
		return nil, ErrNotFound
	}
	if s == nil || s.rdb == nil {
		return nil, errors.New("redis client not configured")
	}

	id, err := s.rdb.Get(ctx, s.fpKey(fpHash)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return s.LoadByID(ctx, id)
}

func (s *Store) Touch(ctx context.Context, g *GuestRecord) error {
	if g == nil || g.ID == "" {
		return fmt.Errorf("invalid guest record")
	}

	g.LastSeenAt = time.Now()
	return s.Save(ctx, g)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	g, err := s.LoadByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, s.guestKey(id))
	if g.FingerprintHash != "" {
		pipe.Del(ctx, s.fpKey(g.FingerprintHash))
	}

	_, err = pipe.Exec(ctx)
	return err
}
