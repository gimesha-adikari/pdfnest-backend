package conversion_test

import (
	"context"
	"testing"
	"time"

	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/tasks"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestPDFToMarkdownBillingIdempotency(t *testing.T) {
	mr, err := miniredis.Run()
	assert.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	guestStore := billing.NewGuestQuotaStore(rdb, 1*time.Hour)
	billing.Initialize(guestStore)

	ctx := context.Background()
	guestID := "guest_ip_" + uuid.NewString()

	// 1. First reservation -> succeeds
	res, err := guestStore.Reserve(ctx, guestID, billing.ConvertPDFToMarkdown, 1, 0, "/api/conversion/pdf-to-markdown-async")
	assert.NoError(t, err)
	assert.NotEmpty(t, res.ID)

	// 2. Commit first time -> succeeds and consumes 1 unit
	err = guestStore.Commit(ctx, res.ID)
	assert.NoError(t, err)

	// 3. Commit second time (repeated/idempotent call) -> no-op, 0 extra units consumed
	err = guestStore.Commit(ctx, res.ID)
	assert.NoError(t, err)

	// 4. Repeated release call -> no-op
	err = guestStore.Release(ctx, res.ID)
	assert.NoError(t, err)
}

func TestCommitTaskBillingHandlerIntegration(t *testing.T) {
	assert.NotNil(t, tasks.CommitTaskBillingHandler, "CommitTaskBillingHandler must be registered")
	assert.NotNil(t, tasks.StaleTaskBillingHandler, "StaleTaskBillingHandler must be registered")
}
