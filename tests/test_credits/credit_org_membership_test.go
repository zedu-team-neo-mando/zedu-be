package test_credits

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

const (
	creditsMemberTopUpAmount = 7331.77
	creditsMemberUsageAmount = 4242.42
)

// creditsMemberFixture holds an organisation that owns credit data, a user who
// belongs to it, and a second logged-in user who does not.
type creditsMemberFixture struct {
	router          *gin.Engine
	orgID           string
	memberToken     string
	outsiderToken   string
	stripeSessionID string
}

func creditsMemberSetup(t *testing.T) *creditsMemberFixture {
	t.Helper()
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)

	r, authCtl, _, _, db := SetupCreditTestRouter()

	memberID, orgID, memberToken, _ := CreateUserWithOrganization(t, r, authCtl, db)
	memberPersonalOrgs := creditsMemberOtherOrgIDs(db.Postgresql, memberID, orgID)
	t.Cleanup(func() {
		CleanupCreditTestData(db.Postgresql, memberID, orgID)
		creditsMemberDeleteOrgs(t, db.Postgresql, memberPersonalOrgs)
	})

	outsiderID, outsiderOrgID, outsiderToken, _ := CreateUserWithOrganization(t, r, authCtl, db)
	outsiderPersonalOrgs := creditsMemberOtherOrgIDs(db.Postgresql, outsiderID, outsiderOrgID)
	t.Cleanup(func() {
		CleanupCreditTestData(db.Postgresql, outsiderID, outsiderOrgID)
		creditsMemberDeleteOrgs(t, db.Postgresql, outsiderPersonalOrgs)
	})

	sessionID := "cs_test_creditsmember_" + utility.GenerateUUID()
	topUp := models.CreditTransaction{
		ID:              utility.GenerateUUID(),
		OrganisationID:  orgID,
		Amount:          creditsMemberTopUpAmount,
		BalanceBefore:   0,
		BalanceAfter:    creditsMemberTopUpAmount,
		Type:            "topup",
		StripeSessionID: &sessionID,
	}
	require.NoError(t, db.Postgresql.Create(&topUp).Error, "failed to create credit transaction fixture")
	t.Cleanup(func() {
		db.Postgresql.Where("id = ?", topUp.ID).Delete(&models.CreditTransaction{})
	})

	usage := models.CreditUsage{
		ID:             utility.GenerateUUID(),
		OrganisationID: orgID,
		Amount:         creditsMemberUsageAmount,
		AgentID:        utility.GenerateUUID(),
		UserID:         &memberID,
	}
	require.NoError(t, db.Postgresql.Create(&usage).Error, "failed to create credit usage fixture")
	t.Cleanup(func() {
		db.Postgresql.Where("id = ?", usage.ID).Delete(&models.CreditUsage{})
	})

	return &creditsMemberFixture{
		router:          r,
		orgID:           orgID,
		memberToken:     memberToken,
		outsiderToken:   outsiderToken,
		stripeSessionID: sessionID,
	}
}

// creditsMemberOtherOrgIDs lists the organisations a freshly registered user
// belongs to or owns, other than the one the fixture created explicitly (these
// are the personal organisations created at registration).
func creditsMemberOtherOrgIDs(db *gorm.DB, userID, exceptOrgID string) []string {
	var ids []string
	db.Raw(`SELECT organisation_id FROM user_organisations WHERE user_id = ? AND organisation_id <> ?
		UNION SELECT id FROM organisations WHERE owner_id = ? AND id <> ?`,
		userID, exceptOrgID, userID, exceptOrgID).Scan(&ids)
	return ids
}

func creditsMemberDeleteOrgs(t *testing.T, db *gorm.DB, orgIDs []string) {
	t.Helper()
	for _, id := range orgIDs {
		db.Exec("DELETE FROM user_organisations WHERE organisation_id = ?", id)
		db.Exec("DELETE FROM credit_transactions WHERE organisation_id = ?", id)
		db.Exec("DELETE FROM credit_usages WHERE organisation_id = ?", id)
		db.Exec("DELETE FROM organisation_integrations WHERE org_id = ?", id)
		db.Exec("DELETE FROM org_user_managements WHERE organisation_id = ?", id)
		if err := db.Unscoped().Where("id = ?", id).Delete(&models.Organisation{}).Error; err != nil {
			t.Logf("could not delete personal organisation %s: %v", id, err)
		}
	}
}

func (f *creditsMemberFixture) get(path, token string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func TestCreditsMemberOrgScopedReads(t *testing.T) {
	f := creditsMemberSetup(t)

	routes := []struct {
		name   string
		prefix string
		// assertData checks that a successful body carries the fixture rows.
		assertData func(t *testing.T, f *creditsMemberFixture, body map[string]any)
	}{
		{
			name:   "usage-report",
			prefix: "/api/v1/credits/usage-report/",
			assertData: func(t *testing.T, f *creditsMemberFixture, body map[string]any) {
				data, ok := body["data"].(map[string]any)
				require.True(t, ok, "data should be an object")
				assert.Equal(t, f.orgID, data["organisation_id"])
				assert.InDelta(t, creditsMemberTopUpAmount, data["total_credited"], 0.001)
				assert.InDelta(t, creditsMemberUsageAmount, data["total_used"], 0.001)
				assert.Len(t, data["recent_topups"], 1)
				assert.Len(t, data["recent_usages"], 1)
			},
		},
		{
			name:   "transactions",
			prefix: "/api/v1/credits/transactions/",
			assertData: func(t *testing.T, f *creditsMemberFixture, body map[string]any) {
				data, ok := body["data"].([]any)
				require.True(t, ok, "data should be an array")
				require.Len(t, data, 1)
				row, ok := data[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, f.orgID, row["organisation_id"])
				assert.InDelta(t, creditsMemberTopUpAmount, row["amount"], 0.001)
				assert.Equal(t, f.stripeSessionID, row["stripe_session_id"])
			},
		},
	}

	for _, route := range routes {
		t.Run(route.name+"/member", func(t *testing.T) {
			rr := f.get(route.prefix+f.orgID, f.memberToken)

			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			body := tst.ParseResponse(rr)
			assert.Equal(t, "success", body["status"])
			route.assertData(t, f, body)
		})

		t.Run(route.name+"/non_member", func(t *testing.T) {
			rr := f.get(route.prefix+f.orgID, f.outsiderToken)

			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			creditsMemberAssertNoCreditData(t, f, rr)
		})

		t.Run(route.name+"/unknown_org", func(t *testing.T) {
			rr := f.get(route.prefix+utility.GenerateUUID(), f.outsiderToken)

			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			creditsMemberAssertNoCreditData(t, f, rr)
		})

		t.Run(route.name+"/malformed_org_id", func(t *testing.T) {
			rr := f.get(route.prefix+"not-a-uuid", f.outsiderToken)

			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			creditsMemberAssertNoCreditData(t, f, rr)
		})

		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, route.prefix+f.orgID, nil)
			rr := httptest.NewRecorder()
			f.router.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
			creditsMemberAssertNoCreditData(t, f, rr)
		})
	}
}

func creditsMemberAssertNoCreditData(t *testing.T, f *creditsMemberFixture, rr *httptest.ResponseRecorder) {
	t.Helper()

	raw := rr.Body.String()
	assert.NotContains(t, raw, fmt.Sprint(creditsMemberTopUpAmount), "response leaked the top-up amount")
	assert.NotContains(t, raw, fmt.Sprint(creditsMemberUsageAmount), "response leaked the usage amount")
	assert.NotContains(t, raw, f.stripeSessionID, "response leaked the stripe session id")
	assert.NotContains(t, raw, f.orgID, "response echoed the organisation id")

	body := tst.ParseResponse(rr)
	assert.NotEqual(t, "success", body["status"])
	assert.Nil(t, body["data"], "error response must not carry data")
}
