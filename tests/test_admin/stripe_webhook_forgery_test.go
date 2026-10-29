package test_admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/subscriptions"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

// A request that did not come from Stripe must not be able to add credits.
// It sends a made-up Stripe-Signature and uses only the controller's normal setup,
// so it runs the same way before and after the signature check was added.
func TestStripeWebhook_ForgedRequestIsRejected(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)
	db := storage.Connection()

	orgID := CreateOrganizationWithCredit(t, db.Postgresql, 100.00)
	transactionsBefore := countCreditTransactions(t, db, orgID)
	packageID := CreateTestCreditPackage(t, db.Postgresql, 1000, 5.00)
	sessionID := fmt.Sprintf("cs_test_%s", utility.GenerateUUID())
	t.Cleanup(func() { CleanupSpecificTestData(db.Postgresql, "", []string{orgID}) })

	controller := subscriptions.Controller{
		Db:        db,
		Validator: validator.New(),
		Logger:    utility.NewLogger(),
		ExtReq:    request.ExternalRequest{},
	}
	r := gin.New()
	r.POST("/api/v1/subscriptions/webhook", controller.HandleStripeWebhook)

	event := CreateTestStripeWebhookEvent(orgID, packageID, sessionID, "https://zedu.chat/client/settings/organisation/billing?session_id="+sessionID, "https://zedu.chat/client/settings/organisation/billing")
	payload, _ := json.Marshal(event)

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "forged_signature_made_up_by_an_attacker")

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.NotEqual(t, http.StatusOK, rr.Code, "a request with a made-up signature must not be accepted")

	// The organisation already has one starting transaction from the fixture, so compare with the count before
	assert.Equal(t, transactionsBefore, countCreditTransactions(t, db, orgID), "a forged request must not create a credit transaction")

	var org models.Organisation
	assert.NoError(t, db.Postgresql.Where("id = ?", orgID).First(&org).Error)
	assert.Equal(t, float64(100.00), org.CreditBalance, "a forged request must not change the credit balance")
}
