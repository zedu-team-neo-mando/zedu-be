package test_admin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stripe/stripe-go/v72"
	"github.com/stripe/stripe-go/v72/webhook"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/config"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/subscriptions"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

func TestCreditTopupWebhook_Success(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)
	db := storage.Connection()
	validator := validator.New()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())

	webhookController := subscriptions.Controller{
		Db:                  db,
		Validator:           validator,
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(orgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}

	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", webhookController.HandleStripeWebhook)

	webhookEvent := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	req := newSignedWebhookRequest(t, webhookEvent, testWebhookSecret)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	t.Cleanup(func() {
		CleanupSpecificTestData(db.Postgresql, "", []string{orgID})
	})

	assert.Equal(t, http.StatusOK, rr.Code)

	response := tst.ParseResponse(rr)
	assert.Equal(t, "success", response["status"])

	var transaction models.CreditTransaction
	err := db.Postgresql.Where("organisation_id = ? AND type = ?", orgID, "Top-up").Order("created_at DESC").First(&transaction).Error
	if err != nil {
		t.Fatalf("Failed to find credit transaction: %v. Response body: %s", err, rr.Body.String())
	}
	assert.Equal(t, float64(1000), transaction.Amount)
	assert.Equal(t, float64(100.00), transaction.BalanceBefore)
	assert.Equal(t, float64(1100.00), transaction.BalanceAfter)

	var org models.Organisation
	err = db.Postgresql.Where("id = ?", orgID).First(&org).Error
	assert.NoError(t, err)
	assert.Equal(t, float64(1100.00), org.CreditBalance)
}

func TestCreditTopupWebhook_InvalidPackageID(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)
	db := storage.Connection()
	validator := validator.New()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 0.00)
	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())

	webhookController := subscriptions.Controller{
		Db:                  db,
		Validator:           validator,
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(orgID, "00000000-0000-0000-0000-000000000000", sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}

	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", webhookController.HandleStripeWebhook)

	webhookEvent := CreateTestStripeWebhookEvent(orgID, "00000000-0000-0000-0000-000000000000", sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	req := newSignedWebhookRequest(t, webhookEvent, testWebhookSecret)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	t.Cleanup(func() {
		CleanupSpecificTestData(db.Postgresql, "", []string{orgID})
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)

	response := tst.ParseResponse(rr)
	assert.NotEqual(t, "success", response["status"])

	var transactionCount int64
	db.Postgresql.Model(&models.CreditTransaction{}).Where("organisation_id = ?", orgID).Count(&transactionCount)
	assert.Equal(t, int64(0), transactionCount)
}

func TestCreditTopupWebhook_InvalidOrgID(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)
	db := storage.Connection()
	validator := validator.New()

	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())

	webhookController := subscriptions.Controller{
		Db:                  db,
		Validator:           validator,
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession("00000000-0000-0000-0000-000000000000", packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}

	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", webhookController.HandleStripeWebhook)

	// Use valid UUID format but non-existent ID to avoid PostgreSQL parse errors
	webhookEvent := CreateTestStripeWebhookEvent("00000000-0000-0000-0000-000000000000", packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	req := newSignedWebhookRequest(t, webhookEvent, testWebhookSecret)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	t.Cleanup(func() {
		// No org created in this test, just clean package
		db.Postgresql.Exec("DELETE FROM credit_packages WHERE id = ?", packageID)
	})

	assert.Equal(t, http.StatusNotFound, rr.Code)

	response := tst.ParseResponse(rr)
	assert.NotEqual(t, "success", response["status"])
}

func TestCreditTopupWebhook_WithExistingCreditUsage(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)
	db := storage.Connection()
	validator := validator.New()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 500, 10.00)
	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())

	CreateCreditUsage(t, db.Postgresql, orgID, 20.00)
	CreateCreditUsage(t, db.Postgresql, orgID, 30.00)

	webhookController := subscriptions.Controller{
		Db:                  db,
		Validator:           validator,
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(orgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}

	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", webhookController.HandleStripeWebhook)

	webhookEvent := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	req := newSignedWebhookRequest(t, webhookEvent, testWebhookSecret)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	t.Cleanup(func() {
		CleanupSpecificTestData(db.Postgresql, "", []string{orgID})
	})

	assert.Equal(t, http.StatusOK, rr.Code)

	var transaction models.CreditTransaction
	err := db.Postgresql.Where("organisation_id = ?", orgID).Order("created_at DESC").First(&transaction).Error
	if err != nil {
		t.Fatalf("Failed to find credit transaction: %v. Response body: %s", err, rr.Body.String())
	}
	assert.Equal(t, float64(500), transaction.Amount)
	assert.Equal(t, float64(100.00), transaction.BalanceBefore)
	assert.Equal(t, float64(600.00), transaction.BalanceAfter)

	var org models.Organisation
	err = db.Postgresql.Where("id = ?", orgID).First(&org).Error
	assert.NoError(t, err)
	assert.Equal(t, float64(550.00), org.CreditBalance)
}

func CreateTestStripeWebhookEvent(orgID, packageID, sessionID, successURL, cancelURL string) map[string]any {
	checkoutSession := map[string]any{
		"id":          sessionID,
		"object":      "checkout.session",
		"success_url": successURL,
		"cancel_url":  cancelURL,
		"metadata": map[string]string{
			"flow":    "credit_topup",
			"org_id":  orgID,
			"plan_id": packageID,
		},
		"customer_details": map[string]any{
			"email": fmt.Sprintf("test%s@qa.team", utility.RandomString(5)),
		},
		"customer": map[string]any{
			"email": fmt.Sprintf("test%s@qa.team", utility.RandomString(5)),
		},
	}

	return map[string]any{
		"id":     fmt.Sprintf("evt_test_%s", utility.GenerateUUID()),
		"type":   "checkout.session.completed",
		"object": "event",
		"data": map[string]any{
			"object": checkoutSession,
		},
	}
}

// ---------------------------------------------------------------------------
// Signature and payment verification
// ---------------------------------------------------------------------------

const testWebhookSecret = "whsec_test_secret_used_only_in_tests"

func signatureHeader(payload []byte, secret string, at time.Time) string {
	sig := webhook.ComputeSignature(at, payload, secret)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(sig))
}

func newWebhookRequest(t *testing.T, payload []byte, signature string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewBuffer(payload))
	if err != nil {
		t.Fatalf("failed to create webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set("Stripe-Signature", signature)
	}
	return req
}

func newSignedWebhookRequest(t *testing.T, event map[string]any, secret string) *http.Request {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal webhook event: %v", err)
	}
	return newWebhookRequest(t, payload, signatureHeader(payload, secret, time.Now()))
}

// fakeStripeSession stands in for the session Stripe returns when we ask it directly.
func fakeStripeSession(orgID, packageID, sessionID string, status stripe.CheckoutSessionPaymentStatus) func(string) (*stripe.CheckoutSession, error) {
	return func(string) (*stripe.CheckoutSession, error) {
		return &stripe.CheckoutSession{
			ID:            sessionID,
			PaymentStatus: status,
			Metadata: map[string]string{
				"flow":    "credit_topup",
				"org_id":  orgID,
				"plan_id": packageID,
			},
		}, nil
	}
}

func setupWebhookRouter(controller subscriptions.Controller) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", controller.HandleStripeWebhook)
	return r
}

// countCreditTransactions counts every credit transaction of an organisation. The test
// fixture already creates one starting "Top-up" transaction, so tests compare the count
// before and after the request instead of expecting zero.
func countCreditTransactions(t *testing.T, db *storage.Database, orgID string) int64 {
	t.Helper()
	var count int64
	err := db.Postgresql.Model(&models.CreditTransaction{}).Where("organisation_id = ?", orgID).Count(&count).Error
	if err != nil {
		t.Fatalf("failed to count credit transactions: %v", err)
	}
	return count
}

func assertNoTopup(t *testing.T, db *storage.Database, orgID string, transactionsBefore int64, expectedBalance float64) {
	t.Helper()
	assert.Equal(t, transactionsBefore, countCreditTransactions(t, db, orgID), "no credit transaction should be created")

	var org models.Organisation
	assert.NoError(t, db.Postgresql.Where("id = ?", orgID).First(&org).Error)
	assert.Equal(t, expectedBalance, org.CreditBalance, "credit balance must not change")
}

func TestStripeWebhook_RejectsUnverifiedRequests(t *testing.T) {
	_ = tst.Setup()
	db := storage.Connection()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	transactionsBefore := countCreditTransactions(t, db, orgID)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{orgID}) })

	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	successURL := "https://zedu.chat/client/settings/organisation/billing?session_id=" + sessionID
	event := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, successURL, "https://zedu.chat/client/settings/organisation/billing")
	payload, _ := json.Marshal(event)

	// A tampered body that carries a signature made for a different body
	tamperedEvent := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, successURL, "https://zedu.chat/other")
	tamperedPayload, _ := json.Marshal(tamperedEvent)

	cases := []struct {
		name      string
		payload   []byte
		signature string
	}{
		{"missing signature header", payload, ""},
		{"made-up signature", payload, "test_signature"},
		{"signed with the wrong secret", payload, signatureHeader(payload, "whsec_some_other_secret", time.Now())},
		{"body changed after signing", tamperedPayload, signatureHeader(payload, testWebhookSecret, time.Now())},
		{"signature older than the allowed window", payload, signatureHeader(payload, testWebhookSecret, time.Now().Add(-10*time.Minute))},
	}

	// The Stripe lookup would say "paid", so only the signature check can stop these
	controller := subscriptions.Controller{
		Db:                  db,
		Validator:           validator.New(),
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(orgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}
	r := setupWebhookRouter(controller)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, newWebhookRequest(t, tc.payload, tc.signature))

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assertNoTopup(t, db, orgID, transactionsBefore, 100.00)
		})
	}
}

func TestStripeWebhook_RejectsEverythingWhenSecretIsNotConfigured(t *testing.T) {
	_ = tst.Setup()
	db := storage.Connection()

	cfg := config.GetConfig()
	originalSecret := cfg.Stripe.STRIPE_WEBHOOK_SECRET
	cfg.Stripe.STRIPE_WEBHOOK_SECRET = ""
	t.Cleanup(func() { cfg.Stripe.STRIPE_WEBHOOK_SECRET = originalSecret })

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	transactionsBefore := countCreditTransactions(t, db, orgID)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{orgID}) })

	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	event := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	// No StripeWebhookSecret override, so the controller falls back to the (empty) config value
	controller := subscriptions.Controller{
		Db:                 db,
		Validator:          validator.New(),
		Logger:             utility.NewLogger(),
		ExtReq:             request.ExternalRequest{},
		GetCheckoutSession: fakeStripeSession(orgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}
	r := setupWebhookRouter(controller)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, newSignedWebhookRequest(t, event, testWebhookSecret))

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assertNoTopup(t, db, orgID, transactionsBefore, 100.00)
}

func TestCreditTopupWebhook_SessionNotPaid(t *testing.T) {
	_ = tst.Setup()
	db := storage.Connection()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	transactionsBefore := countCreditTransactions(t, db, orgID)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{orgID}) })

	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	event := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	controller := subscriptions.Controller{
		Db:                  db,
		Validator:           validator.New(),
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(orgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusUnpaid),
	}
	r := setupWebhookRouter(controller)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, newSignedWebhookRequest(t, event, testWebhookSecret))

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assertNoTopup(t, db, orgID, transactionsBefore, 100.00)
}

func TestCreditTopupWebhook_StripeLookupFails(t *testing.T) {
	_ = tst.Setup()
	db := storage.Connection()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	transactionsBefore := countCreditTransactions(t, db, orgID)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{orgID}) })

	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	event := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	controller := subscriptions.Controller{
		Db:                  db,
		Validator:           validator.New(),
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession: func(string) (*stripe.CheckoutSession, error) {
			return nil, errors.New("stripe is unreachable")
		},
	}
	r := setupWebhookRouter(controller)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, newSignedWebhookRequest(t, event, testWebhookSecret))

	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assertNoTopup(t, db, orgID, transactionsBefore, 100.00)
}

// The ids in the signed event are ignored: credits go to the organisation Stripe has on the session.
func TestCreditTopupWebhook_UsesStripeCopyOfTheSession(t *testing.T) {
	_ = tst.Setup()
	db := storage.Connection()

	paidOrgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	otherOrgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{paidOrgID, otherOrgID}) })

	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	// The event body names otherOrgID, but Stripe says the paid session belongs to paidOrgID
	event := CreateTestStripeWebhookEvent(otherOrgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")

	controller := subscriptions.Controller{
		Db:                  db,
		Validator:           validator.New(),
		Logger:              utility.NewLogger(),
		ExtReq:              request.ExternalRequest{},
		StripeWebhookSecret: testWebhookSecret,
		GetCheckoutSession:  fakeStripeSession(paidOrgID, packageID, sessionID, stripe.CheckoutSessionPaymentStatusPaid),
	}
	r := setupWebhookRouter(controller)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, newSignedWebhookRequest(t, event, testWebhookSecret))

	assert.Equal(t, http.StatusOK, rr.Code)

	var paidOrg, otherOrg models.Organisation
	assert.NoError(t, db.Postgresql.Where("id = ?", paidOrgID).First(&paidOrg).Error)
	assert.NoError(t, db.Postgresql.Where("id = ?", otherOrgID).First(&otherOrg).Error)
	assert.Equal(t, float64(1100.00), paidOrg.CreditBalance)
	assert.Equal(t, float64(100.00), otherOrg.CreditBalance)
}
