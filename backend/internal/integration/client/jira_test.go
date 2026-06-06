package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/integration/client"
)

// auth points the client at the test server: NewJiraClient(srv.URL, ...) plus
// cloudid "cloud-1" makes requests go to <srv>/ex/jira/cloud-1/<path>.
func testAuth() domain.ClientAuth {
	return domain.ClientAuth{AccessToken: "access-tok", ExternalAccountID: "cloud-1", SiteURL: "https://acme.atlassian.net"}
}

func TestJiraClient_CreateIssue(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/ex/jira/cloud-1/rest/api/3/issue", r.URL.Path)
		assert.Equal(t, "Bearer access-tok", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		assert.NoError(t, json.Unmarshal(body, &captured))
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "10001", "key": "NHI-1", "self": "https://api/issue/10001"})
	}))
	defer srv.Close()

	c := client.NewJiraClient(srv.URL, 5*time.Second)
	payload := domain.TicketPayload{
		ProjectKey:  "NHI",
		Title:       "Stale Service Account: svc-deploy-prod",
		Description: "Detected an unused service account.",
		Labels:      []string{"nhi finding", "stale"},
	}

	ref, err := c.CreateIssue(context.Background(), testAuth(), payload)
	require.NoError(t, err)

	// Assert mapped response (complex result -> cmp.Diff).
	wantRef := domain.TicketRef{
		Provider: domain.ProviderJira,
		IssueKey: "NHI-1",
		URL:      "https://acme.atlassian.net/browse/NHI-1",
	}
	if diff := cmp.Diff(wantRef, ref); diff != "" {
		t.Fatalf("ticket ref mismatch (-want +got):\n%s", diff)
	}

	// Assert the request body shape.
	fields, ok := captured["fields"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Stale Service Account: svc-deploy-prod", fields["summary"])
	require.Equal(t, map[string]any{"key": "NHI"}, fields["project"])
	require.Equal(t, map[string]any{"name": "Task"}, fields["issuetype"])
	require.Equal(t, []any{"nhi-finding", "stale"}, fields["labels"]) // spaces sanitized
	require.NotNil(t, fields["description"])                          // ADF doc present
}

func TestJiraClient_ListProjects(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ex/jira/cloud-1/rest/api/3/project/search", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"values": []map[string]string{
				{"key": "NHI", "name": "NHI Findings"},
				{"key": "OPS", "name": "Operations"},
			},
		})
	}))
	defer srv.Close()

	c := client.NewJiraClient(srv.URL, 5*time.Second)
	got, err := c.ListProjects(context.Background(), testAuth())
	require.NoError(t, err)
	want := []domain.ProjectRef{
		{Key: "NHI", Name: "NHI Findings"},
		{Key: "OPS", Name: "Operations"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("projects mismatch (-want +got):\n%s", diff)
	}
}

func TestJiraClient_VerifyConnectionAndName(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ex/jira/cloud-1/rest/api/3/myself", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.NewJiraClient(srv.URL, 5*time.Second)
	require.Equal(t, "jira", c.Name())
	require.NoError(t, c.VerifyConnection(context.Background(), testAuth()))
}

func TestJiraClient_CreateIssue_ErrorMapping(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":{"project":"project is required"}}`))
	}))
	defer srv.Close()

	c := client.NewJiraClient(srv.URL, 5*time.Second)
	_, err := c.CreateIssue(context.Background(), testAuth(), domain.TicketPayload{ProjectKey: "X", Title: "t"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")
}
