package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type GuestQuotaStore struct {
	rdb    *redis.Client
	ttl    time.Duration
	prefix string
	limits TierLimits
}

type GuestReservation struct {
	ID        string    `json:"id"`
	GuestID   string    `json:"guest_id"`
	ToolName  string    `json:"tool_name"`
	Units     int       `json:"units"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type guestState struct {
	Used3H       int
	UsedDay      int
	UsedMonth    int
	Pending3H    int
	PendingDay   int
	PendingMonth int

	Window3HResetAt      time.Time
	WindowDailyResetAt   time.Time
	WindowMonthlyResetAt time.Time
}

func NewGuestQuotaStore(rdb *redis.Client, ttl time.Duration) *GuestQuotaStore {
	return &GuestQuotaStore{
		rdb:    rdb,
		ttl:    ttl,
		prefix: "platen:guestquota:",
		limits: TierLimits{
			Units3H:    4,
			UnitsDay:   10,
			UnitsMonth: 30,
		},
	}
}

func (s *GuestQuotaStore) stateKey(guestID string) string {
	return s.prefix + "state:" + guestID
}

func (s *GuestQuotaStore) resKey(reservationID string) string {
	return s.prefix + "res:" + reservationID
}

func (s *GuestQuotaStore) Reserve(ctx context.Context, guestID string, tool Tool, pages, images int, requestPath string) (*GuestReservation, error) {
	if strings.TrimSpace(guestID) == "" {
		return nil, NewBillingError(
			ErrUnknownBilling,
			"Billing error",
			"Unable to process request.",
			"",
			"",
			0,
		)
	}

	units := tool.Units(pages, images)
	now := time.Now()
	stateKey := s.stateKey(guestID)
	reservationID := uuid.NewString()
	reservation := &GuestReservation{
		ID:        reservationID,
		GuestID:   guestID,
		ToolName:  tool.Name,
		Units:     units,
		CreatedAt: now,
		ExpiresAt: now.Add(6 * time.Hour),
	}

	var limitErr error

	err := s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		state, err := s.loadState(ctx, tx, guestID)
		if err != nil {
			return err
		}
		s.syncWindows(&state, now)

		available3H := s.limits.Units3H - (state.Used3H + state.Pending3H)
		availableDay := s.limits.UnitsDay - (state.UsedDay + state.PendingDay)
		availableMonth := s.limits.UnitsMonth - (state.UsedMonth + state.PendingMonth)

		if available3H < 0 {
			available3H = 0
		}
		if availableDay < 0 {
			availableDay = 0
		}
		if availableMonth < 0 {
			availableMonth = 0
		}

		state.Pending3H += units
		state.PendingDay += units
		state.PendingMonth += units

		if units > available3H {
			limitErr = HourlyLimitError(units)
			return limitErr
		}
		if units > availableDay {
			limitErr = DailyLimitError(units)
			return limitErr
		}
		if units > availableMonth {
			limitErr = MonthlyLimitError(units)
			return limitErr
		}

		rawRes, _ := json.Marshal(reservation)
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, stateKey, state.toMap()...)
			pipe.Expire(ctx, stateKey, s.ttl)
			pipe.Set(ctx, s.resKey(reservationID), rawRes, 6*time.Hour)
			return nil
		})
		return err
	}, stateKey)

	if limitErr != nil {
		return nil, limitErr
	}
	if err != nil {
		return nil, err
	}
	return reservation, nil
}

func (s *GuestQuotaStore) Commit(ctx context.Context, reservationID string) error {
	res, err := s.loadReservation(ctx, reservationID)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}

	now := time.Now()
	stateKey := s.stateKey(res.GuestID)

	return s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		state, err := s.loadState(ctx, tx, res.GuestID)
		if err != nil {
			return err
		}
		s.syncWindows(&state, now)

		state.Pending3H -= res.Units
		state.PendingDay -= res.Units
		state.PendingMonth -= res.Units

		if state.Pending3H < 0 {
			state.Pending3H = 0
		}
		if state.PendingDay < 0 {
			state.PendingDay = 0
		}
		if state.PendingMonth < 0 {
			state.PendingMonth = 0
		}

		state.Used3H += res.Units
		state.UsedDay += res.Units
		state.UsedMonth += res.Units

		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, stateKey, state.toMap()...)
			pipe.Expire(ctx, stateKey, s.ttl)
			pipe.Del(ctx, s.resKey(reservationID))
			return nil
		})
		return err
	}, stateKey)
}

func (s *GuestQuotaStore) Release(ctx context.Context, reservationID string) error {
	res, err := s.loadReservation(ctx, reservationID)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}

	now := time.Now()
	stateKey := s.stateKey(res.GuestID)

	return s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		state, err := s.loadState(ctx, tx, res.GuestID)
		if err != nil {
			return err
		}
		s.syncWindows(&state, now)

		state.Pending3H -= res.Units
		state.PendingDay -= res.Units
		state.PendingMonth -= res.Units

		if state.Pending3H < 0 {
			state.Pending3H = 0
		}
		if state.PendingDay < 0 {
			state.PendingDay = 0
		}
		if state.PendingMonth < 0 {
			state.PendingMonth = 0
		}

		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, stateKey, state.toMap()...)
			pipe.Expire(ctx, stateKey, s.ttl)
			pipe.Del(ctx, s.resKey(reservationID))
			return nil
		})
		return err
	}, stateKey)
}

func (s *GuestQuotaStore) loadReservation(ctx context.Context, reservationID string) (*GuestReservation, error) {
	raw, err := s.rdb.Get(ctx, s.resKey(reservationID)).Bytes()
	if err != nil {
		return nil, err
	}
	var res GuestReservation
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *GuestQuotaStore) loadState(ctx context.Context, tx redis.Cmdable, guestID string) (guestState, error) {
	key := s.stateKey(guestID)
	raw, err := tx.HGetAll(ctx, key).Result()
	if err != nil {
		return guestState{}, err
	}

	state := guestState{
		Window3HResetAt:      time.Now().Truncate(3 * time.Hour).Add(3 * time.Hour),
		WindowDailyResetAt:   nextMidnight(time.Now()),
		WindowMonthlyResetAt: nextMonthStart(time.Now()),
	}

	if len(raw) == 0 {
		return state, nil
	}

	state.Used3H = atoiOrZero(raw["used_3h"])
	state.UsedDay = atoiOrZero(raw["used_day"])
	state.UsedMonth = atoiOrZero(raw["used_month"])
	state.Pending3H = atoiOrZero(raw["pending_3h"])
	state.PendingDay = atoiOrZero(raw["pending_day"])
	state.PendingMonth = atoiOrZero(raw["pending_month"])

	if v := strings.TrimSpace(raw["window_3h_reset_at"]); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			state.Window3HResetAt = t
		}
	}
	if v := strings.TrimSpace(raw["window_daily_reset_at"]); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			state.WindowDailyResetAt = t
		}
	}
	if v := strings.TrimSpace(raw["window_monthly_reset_at"]); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			state.WindowMonthlyResetAt = t
		}
	}

	return state, nil
}

func (s *GuestQuotaStore) syncWindows(state *guestState, now time.Time) {
	if state.Window3HResetAt.IsZero() || !now.Before(state.Window3HResetAt) {
		state.Used3H = 0
		state.Pending3H = 0
		state.Window3HResetAt = now.Truncate(3 * time.Hour).Add(3 * time.Hour)
	}
	if state.WindowDailyResetAt.IsZero() || !now.Before(state.WindowDailyResetAt) {
		state.UsedDay = 0
		state.PendingDay = 0
		state.WindowDailyResetAt = nextMidnight(now)
	}
	if state.WindowMonthlyResetAt.IsZero() || !now.Before(state.WindowMonthlyResetAt) {
		state.UsedMonth = 0
		state.PendingMonth = 0
		state.WindowMonthlyResetAt = nextMonthStart(now)
	}
}

func (s guestState) toMap() []interface{} {
	return []interface{}{
		"used_3h", s.Used3H,
		"used_day", s.UsedDay,
		"used_month", s.UsedMonth,
		"pending_3h", s.Pending3H,
		"pending_day", s.PendingDay,
		"pending_month", s.PendingMonth,
		"window_3h_reset_at", s.Window3HResetAt.Format(time.RFC3339Nano),
		"window_daily_reset_at", s.WindowDailyResetAt.Format(time.RFC3339Nano),
		"window_monthly_reset_at", s.WindowMonthlyResetAt.Format(time.RFC3339Nano),
	}
}

func atoiOrZero(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}

func nextMidnight(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

func nextMonthStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
}
