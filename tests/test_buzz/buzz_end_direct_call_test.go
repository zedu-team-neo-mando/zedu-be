package test_buzz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/buzz"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
)

func setupDirectCallEndRouter(buzzCtrl buzz.Controller, db *storage.Database) *gin.Engine {
	r := gin.Default()
	buzzURL := r.Group("/api/v1/buzz", middleware.Authorize(db.Postgresql), middleware.CheckIsDeactivated(db.Postgresql))
	{
		buzzURL.POST("/direct-call", buzzCtrl.InitiateDirectCall)
		buzzURL.POST("/:id/end", buzzCtrl.EndBuzz)
	}
	return r
}

func startDirectCall(t *testing.T, r *gin.Engine, channelID, token string) string {
	var b bytes.Buffer
	json.NewEncoder(&b).Encode(models.InitiateDirectCallRequest{ChannelID: channelID})

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/buzz/direct-call", &b)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	tst.AssertStatusCode(t, rr.Code, http.StatusCreated)

	data, ok := tst.ParseResponse(rr)["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data in direct-call response, got %s", rr.Body.String())
	}
	buzzID, _ := data["buzz_id"].(string)
	if buzzID == "" {
		t.Fatalf("expected buzz_id in direct-call response, got %s", rr.Body.String())
	}
	return buzzID
}

func endBuzzAs(r *gin.Engine, buzzID, token string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/buzz/%s/end", buzzID), nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func assertBuzzStillLive(t *testing.T, db *storage.Database, buzzID string) {
	var b models.Buzz
	if err := db.Postgresql.Where("id = ?", buzzID).First(&b).Error; err != nil {
		t.Fatalf("failed to reload buzz %s: %v", buzzID, err)
	}
	if b.Status != models.BuzzStatusActive || !b.IsLiveStatus {
		t.Errorf("expected buzz %s to stay live, got status %s", buzzID, b.Status)
	}
}

func TestEndBuzz_DirectCall_NonHostForbidden(t *testing.T) {
	logger := tst.Setup()
	gin.SetMode(gin.TestMode)
	validatorRef := validator.New()
	db := storage.Connection()
	buzzCtrl := buzz.Controller{Db: db, Validator: validatorRef, Logger: logger}
	r := setupDirectCallEndRouter(buzzCtrl, db)

	t.Run("DM: recipient and outsider get 403, host can cancel", func(t *testing.T) {
		// users[0] calls, users[1] is the recipient, users[2] is not in the DM.
		users, tokens, orgID := setupDirectCallUsers(t, db, logger, validatorRef, 3)
		dmChannelID := createTestDMChannel(t, db, users[0], users[1], orgID)
		buzzID := startDirectCall(t, r, dmChannelID, tokens[0])

		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[1]).Code, http.StatusForbidden)
		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[2]).Code, http.StatusForbidden)
		assertBuzzStillLive(t, db, buzzID)

		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[0]).Code, http.StatusOK)
	})

	t.Run("Group DM: participant and outsider get 403, host can cancel", func(t *testing.T) {
		// users[0] calls, users[1] and users[2] are in the group DM, users[3] is not.
		users, tokens, orgID := setupDirectCallUsers(t, db, logger, validatorRef, 4)
		groupDMChannelID := createTestGroupDMChannel(t, db, users[:3], orgID)
		buzzID := startDirectCall(t, r, groupDMChannelID, tokens[0])

		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[1]).Code, http.StatusForbidden)
		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[3]).Code, http.StatusForbidden)
		assertBuzzStillLive(t, db, buzzID)

		tst.AssertStatusCode(t, endBuzzAs(r, buzzID, tokens[0]).Code, http.StatusOK)
	})
}
