package test_subscription

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	tst "github.com/hngprojects/telex_be/tests"
)

func TestGetCurrentSubscription_RequiresOrgMembership(t *testing.T) {
	_ = tst.Setup()
	gin.SetMode(gin.TestMode)

	router, authCtl, _, _, db := SetupSubscriptionTestRouter()

	memberID, memberOrgID, memberToken, _ := CreateUserWithOrganization(t, router, authCtl, db)
	defer CleanupSubscriptionTestData(db.Postgresql, memberID, memberOrgID)

	otherID, otherOrgID, otherToken, _ := CreateUserWithOrganization(t, router, authCtl, db)
	defer CleanupSubscriptionTestData(db.Postgresql, otherID, otherOrgID)

	getCurrent := func(orgID, token string) *httptest.ResponseRecorder {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/subscriptions/current/%s", orgID), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("MemberCanReadOwnOrganisationSubscription", func(t *testing.T) {
		rr := getCurrent(memberOrgID, memberToken)
		tst.AssertStatusCode(t, rr.Code, http.StatusOK)
	})

	t.Run("NonMemberCannotReadAnotherOrganisationSubscription", func(t *testing.T) {
		rr := getCurrent(memberOrgID, otherToken)
		tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)

		if _, ok := tst.ParseResponse(rr)["data"]; ok {
			t.Errorf("expected no data for a non-member, got %s", rr.Body.String())
		}
	})

	t.Run("NonMemberIsForbiddenEvenForUnknownOrganisation", func(t *testing.T) {
		rr := getCurrent("00000000-0000-0000-0000-000000000001", memberToken)
		tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
	})
}
