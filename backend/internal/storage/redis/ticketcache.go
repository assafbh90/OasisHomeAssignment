package redis

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/domain"
)

const (
	ticketCacheKeyPrefix = "tickets:"
	// ticketCacheMax bounds the cached set per tenant.
	ticketCacheMax = 200
)

// RedisTicketCache caches a tenant's IdentityHub tickets (a mirror of the Jira
// label search) at `tickets:{tenant}` as a JSON list with a TTL. Jira is the
// source of truth, so a cold/expired cache self-heals on the next reconcile.
type RedisTicketCache struct {
	client *goredis.Client
	ttl    time.Duration
}

// NewRedisTicketCache constructs the cache with the given entry TTL.
func NewRedisTicketCache(client *goredis.Client, ttl time.Duration) *RedisTicketCache {
	return &RedisTicketCache{client: client, ttl: ttl}
}

func ticketCacheKey(tenantID uuid.UUID) string { return ticketCacheKeyPrefix + tenantID.String() }

// Replace overwrites the tenant's cached set (newest-first, capped). This is the
// reconcile write: the Jira search result becomes the whole new set, so tickets
// gone from Jira simply disappear.
func (c *RedisTicketCache) Replace(ctx context.Context, tenantID uuid.UUID, tickets []domain.CreatedTicket) error {
	return setJSON(ctx, c.client, ticketCacheKey(tenantID), capNewest(tickets), c.ttl)
}

// Add inserts one ticket (best-effort) so a just-created ticket shows up
// immediately, without waiting for a reconcile. A concurrent reconcile may
// overwrite it; the next reconcile heals any divergence.
func (c *RedisTicketCache) Add(ctx context.Context, tenantID uuid.UUID, ticket domain.CreatedTicket) error {
	var existing []domain.CreatedTicket
	if _, err := getJSON(ctx, c.client, ticketCacheKey(tenantID), &existing); err != nil {
		return err
	}
	deduped := lo.Filter(existing, func(existingTicket domain.CreatedTicket, _ int) bool {
		return existingTicket.IssueKey != ticket.IssueKey
	})
	return c.Replace(ctx, tenantID, append([]domain.CreatedTicket{ticket}, deduped...))
}

// ListByProject returns the cached tickets for a project, newest-first, limited.
// A missing cache yields an empty list (no error) — it populates on reconcile.
func (c *RedisTicketCache) ListByProject(ctx context.Context, tenantID uuid.UUID, projectKey string, limit int) ([]domain.CreatedTicket, error) {
	var tickets []domain.CreatedTicket
	if _, err := getJSON(ctx, c.client, ticketCacheKey(tenantID), &tickets); err != nil {
		return nil, err
	}
	out := lo.Filter(tickets, func(ticket domain.CreatedTicket, _ int) bool {
		return ticket.ProjectKey == projectKey
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func capNewest(tickets []domain.CreatedTicket) []domain.CreatedTicket {
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].CreatedAt.After(tickets[j].CreatedAt) })
	if len(tickets) > ticketCacheMax {
		tickets = tickets[:ticketCacheMax]
	}
	return tickets
}
