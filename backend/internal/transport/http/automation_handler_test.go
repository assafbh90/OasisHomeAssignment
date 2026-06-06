package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// fakeAutomationSvc is a stub satisfying automationService for tests.
type fakeAutomationSvc struct {
	created   automation.CreateInput
	createErr error

	getErr error
}

func (f *fakeAutomationSvc) Create(_ context.Context, _ domain.Identity, in automation.CreateInput) (domain.Automation, error) {
	f.created = in
	if f.createErr != nil {
		return domain.Automation{}, f.createErr
	}
	now := time.Now().UTC()
	return domain.Automation{
		ID:         uuid.New(),
		TenantID:   uuid.New(),
		Name:       in.Name,
		SiteURL:    in.SiteURL,
		Provider:   domain.ProviderJira,
		ProjectKey: in.ProjectKey,
		Interval:   in.Interval,
		Enabled:    in.Enabled,
		Status:     domain.AutomationIdle,
		NextScanAt: now,
		CreatedAt:  now,
	}, nil
}

func (f *fakeAutomationSvc) List(_ context.Context, _ domain.Identity) ([]domain.Automation, error) {
	return nil, nil
}

func (f *fakeAutomationSvc) Get(_ context.Context, _ domain.Identity, _ uuid.UUID) (domain.Automation, error) {
	if f.getErr != nil {
		return domain.Automation{}, f.getErr
	}
	now := time.Now().UTC()
	return domain.Automation{
		ID:         uuid.New(),
		TenantID:   uuid.New(),
		Name:       "test",
		SiteURL:    "https://blog.example.com",
		Provider:   domain.ProviderJira,
		ProjectKey: "NHI",
		Interval:   time.Hour,
		Enabled:    true,
		Status:     domain.AutomationIdle,
		NextScanAt: now,
		CreatedAt:  now,
	}, nil
}

func (f *fakeAutomationSvc) Update(_ context.Context, _ domain.Identity, _ uuid.UUID, _ automation.UpdateInput) (domain.Automation, error) {
	return domain.Automation{}, nil
}

func (f *fakeAutomationSvc) Delete(_ context.Context, _ domain.Identity, _ uuid.UUID) error {
	return nil
}

func (f *fakeAutomationSvc) RunNow(_ context.Context, _ domain.Identity, _ uuid.UUID) error {
	return nil
}

func automationRouter(id domain.Identity, svc automationService) http.Handler {
	return newRouter(RouterDeps{
		Resolver:   fakeResolver{id: id},
		Auth:       minimalAuth(),
		Tokens:     minimalTokens(),
		Automation: NewAutomationHandler(svc),
	})
}

func TestAutomation_Create_ValidBody_Returns201(t *testing.T) {
	id := sessionID()
	svc := &fakeAutomationSvc{}
	r := automationRouter(id, svc)
	cookies, headers := csrf()

	body := automationRequest{
		Name:            "My Blog",
		SiteURL:         "https://blog.example.com",
		ProjectKey:      "NHI",
		IntervalSeconds: 3600,
	}
	rec := doJSON(r, http.MethodPost, "/v1/automations", body, cookies, headers)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// Verify the service received the right CreateInput.
	require.Equal(t, "My Blog", svc.created.Name)
	require.Equal(t, "https://blog.example.com", svc.created.SiteURL)
	require.Equal(t, "NHI", svc.created.ProjectKey)
	require.Equal(t, time.Duration(3600)*time.Second, svc.created.Interval)
	require.True(t, svc.created.Enabled) // default when Enabled field is nil

	// Verify the response structure.
	var resp automationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "My Blog", resp.Name)
	require.Equal(t, 3600, resp.IntervalSeconds)
}

func TestAutomation_Create_BadSiteURL_Returns400(t *testing.T) {
	id := sessionID()
	svc := &fakeAutomationSvc{}
	r := automationRouter(id, svc)
	cookies, headers := csrf()

	body := automationRequest{
		Name:       "Bad",
		SiteURL:    "ftp://x",
		ProjectKey: "NHI",
	}
	rec := doJSON(r, http.MethodPost, "/v1/automations", body, cookies, headers)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, errCodeInvalidRequest, resp.Error)
}

func TestAutomation_Get_NotFound_Returns404(t *testing.T) {
	id := sessionID()
	svc := &fakeAutomationSvc{getErr: domain.ErrAutomationNotFound}
	r := automationRouter(id, svc)

	aid := uuid.New()
	rec := doJSON(r, http.MethodGet, "/v1/automations/"+aid.String(), nil, nil, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	var resp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, errCodeNotFound, resp.Error)
}
