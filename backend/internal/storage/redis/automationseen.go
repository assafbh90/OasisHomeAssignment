package redis

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// RedisAutomationSeenSet tracks which post URLs an automation has already turned
// into tickets, at `automation:seen:{id}`. A URL is added only after its ticket
// is created, so a failed post retries on the next run.
type RedisAutomationSeenSet struct {
	client *goredis.Client
}

// NewRedisAutomationSeenSet constructs the set adapter.
func NewRedisAutomationSeenSet(client *goredis.Client) *RedisAutomationSeenSet {
	return &RedisAutomationSeenSet{client: client}
}

func automationSeenKey(id uuid.UUID) string { return "automation:seen:" + id.String() }

// Unseen returns the subset of urls not yet recorded for the automation,
// preserving input order (newest-first from the discoverer).
func (s *RedisAutomationSeenSet) Unseen(ctx context.Context, automationID uuid.UUID, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	members, err := s.client.SMembers(ctx, automationSeenKey(automationID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read seen set: %w", err)
	}
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		seen[m] = struct{}{}
	}
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if _, ok := seen[u]; !ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// Add records a processed URL.
func (s *RedisAutomationSeenSet) Add(ctx context.Context, automationID uuid.UUID, url string) error {
	if err := s.client.SAdd(ctx, automationSeenKey(automationID), url).Err(); err != nil {
		return fmt.Errorf("add seen url: %w", err)
	}
	return nil
}

// Clear deletes the automation's seen set (called on delete).
func (s *RedisAutomationSeenSet) Clear(ctx context.Context, automationID uuid.UUID) error {
	if err := s.client.Del(ctx, automationSeenKey(automationID)).Err(); err != nil {
		return fmt.Errorf("clear seen set: %w", err)
	}
	return nil
}
