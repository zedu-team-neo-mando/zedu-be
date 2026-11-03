package test_agents

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
	"github.com/stretchr/testify/require"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/agents"
	"github.com/hngprojects/telex_be/pkg/controller/auth"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

const (
	settingsAddOrgPath     = "/api/v1/organisations/%s/agents/%s/settings"
	settingsAddChannelPath = "/api/v1/organisations/%s/agents/%s/channels/%s/settings"
)

type settingsAddFixture struct {
	router        *gin.Engine
	db            *storage.Database
	memberToken   string
	outsiderToken string
	orgID         string
	agentID       string
	channelID     string
	userIDs       []string
	orgIDs        []string
}

func setupSettingsAddFixture(t *testing.T) *settingsAddFixture {
	t.Helper()
	logger := tst.Setup()
	gin.SetMode(gin.TestMode)

	db := storage.Connection()
	validate := validator.New()
	extReq := request.ExternalRequest{Logger: logger, Test: true}

	authCtrl := auth.Controller{Db: db, Validator: validate, Logger: logger, ExtReq: extReq}
	agentsCtrl := agents.Controller{Db: db, Validator: validate, Logger: logger, ExtReq: extReq}

	r := gin.New()
	r.POST("/api/v1/organisations/:org_id/agents/:agent_id/settings",
		middleware.Authorize(db.Postgresql), agentsCtrl.AddAgentSetting)
	r.POST("/api/v1/organisations/:org_id/agents/:agent_id/channels/:channel_id/settings",
		middleware.Authorize(db.Postgresql), agentsCtrl.AddChannelAgentSetting)

	f := &settingsAddFixture{router: r, db: db}

	memberToken, memberID := settingsAddCreateUser(t, r, authCtrl, db, "member")
	outsiderToken, outsiderID := settingsAddCreateUser(t, r, authCtrl, db, "outsider")
	f.memberToken, f.outsiderToken = memberToken, outsiderToken
	f.userIDs = []string{memberID, outsiderID}

	f.orgID = settingsAddCreateOrg(t, db, memberID)
	f.orgIDs = append(f.orgIDs, f.orgID)
	f.agentID = settingsAddCreateAgent(t, db, f.orgID, memberID)
	f.channelID = settingsAddCreateChannel(t, db, f.orgID, memberID)

	t.Cleanup(f.cleanup)
	return f
}

func settingsAddCreateUser(t *testing.T, r *gin.Engine, authCtrl auth.Controller, db *storage.Database, label string) (string, string) {
	t.Helper()
	id := utility.GenerateUUID()
	user := models.CreateUserRequestModel{
		Email:       fmt.Sprintf("settingsadd%s%s@qa.team", label, id),
		PhoneNumber: fmt.Sprintf("+234%v", utility.GetRandomNumbersInRange(7000000000, 9099999999)),
		FirstName:   "Settings",
		LastName:    label,
		Password:    "password",
		UserName:    fmt.Sprintf("settingsadd%s%s", label, id),
	}
	tst.SignupUser(t, r, authCtrl, user, false)
	token := tst.GetLoginToken(t, r, authCtrl, models.LoginRequestModel{Email: user.Email, Password: user.Password})
	return token, tst.GetUserIDFromToken(t, token, db)
}

func settingsAddCreateOrg(t *testing.T, db *storage.Database, ownerID string) string {
	t.Helper()
	orgID := utility.GenerateUUID()
	org := models.Organisation{
		ID:      orgID,
		Name:    fmt.Sprintf("Settings Add Org %s", utility.RandomString(5)),
		Email:   fmt.Sprintf("settingsaddorg%s@qa.team", utility.RandomString(5)),
		OwnerID: ownerID,
		Type:    "test",
		Country: "NG",
	}
	require.NoError(t, db.Postgresql.Create(&org).Error)
	require.NoError(t, db.Postgresql.Exec(
		"INSERT INTO user_organisations (user_id, organisation_id) VALUES (?, ?)", ownerID, orgID).Error)
	return orgID
}

func settingsAddCreateAgent(t *testing.T, db *storage.Database, orgID, ownerID string) string {
	t.Helper()
	agent := models.OrganisationIntegrations{
		ID:            utility.GenerateUUID(),
		OrgID:         orgID,
		IntegrationID: utility.GenerateUUID(),
		OwnerID:       ownerID,
		AppName:       "settings-add-test-agent",
		IsActive:      true,
	}
	require.NoError(t, db.Postgresql.Create(&agent).Error)
	return agent.IntegrationID
}

func settingsAddCreateChannel(t *testing.T, db *storage.Database, orgID, ownerID string) string {
	t.Helper()
	channel := models.Channels{
		ID:             utility.GenerateUUID(),
		Name:           "settings_add_" + utility.RandomString(8),
		OrganisationID: orgID,
		OwnerId:        ownerID,
	}
	require.NoError(t, db.Postgresql.Create(&channel).Error)
	return channel.ID
}

func (f *settingsAddFixture) cleanup() {
	for _, orgID := range f.orgIDs {
		f.db.Postgresql.Exec("DELETE FROM integration_settings WHERE org_id = ?", orgID)
		f.db.Postgresql.Exec("DELETE FROM channel_integration_settings WHERE org_id = ?", orgID)
		f.db.Postgresql.Exec("DELETE FROM channels WHERE organisation_id = ?", orgID)
		f.db.Postgresql.Exec("DELETE FROM organisation_integrations WHERE org_id = ?", orgID)
		f.db.Postgresql.Exec("DELETE FROM user_organisations WHERE organisation_id = ?", orgID)
		f.db.Postgresql.Exec("DELETE FROM organisations WHERE id = ?", orgID)
	}
	for _, userID := range f.userIDs {
		f.db.Postgresql.Exec("DELETE FROM user_organisations WHERE user_id = ?", userID)
		f.db.Postgresql.Exec("DELETE FROM access_tokens WHERE owner_id = ?", userID)
		f.db.Postgresql.Exec("DELETE FROM profiles WHERE userid = ?", userID)
		f.db.Postgresql.Exec("DELETE FROM users WHERE id = ?", userID)
	}
}

func (f *settingsAddFixture) post(t *testing.T, path, token, value string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(models.AddIntegrationSettingsRequest{FormFieldValue: value, FormFieldLabel: "label"})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, path, bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func (f *settingsAddFixture) orgSettingRows(value string) int64 {
	var n int64
	f.db.Postgresql.Model(&models.IntegrationSettings{}).Where("form_field_value = ?", value).Count(&n)
	return n
}

func (f *settingsAddFixture) channelSettingRows(value string) int64 {
	var n int64
	f.db.Postgresql.Model(&models.ChannelIntegrationSettings{}).Where("form_field_value = ?", value).Count(&n)
	return n
}

func TestAddAgentSetting_RequiresOrgMembership(t *testing.T) {
	f := setupSettingsAddFixture(t)

	t.Run("MemberCanAddSetting", func(t *testing.T) {
		value := "m-" + utility.RandomString(10)
		rr := f.post(t, fmt.Sprintf(settingsAddOrgPath, f.orgID, f.agentID), f.memberToken, value)

		assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Equal(t, int64(1), f.orgSettingRows(value))
	})

	t.Run("NonMemberIsForbidden", func(t *testing.T) {
		value := "o-" + utility.RandomString(10)
		rr := f.post(t, fmt.Sprintf(settingsAddOrgPath, f.orgID, f.agentID), f.outsiderToken, value)

		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		assert.Equal(t, int64(0), f.orgSettingRows(value))
	})

	t.Run("NonMemberIsForbiddenForUnknownOrganisation", func(t *testing.T) {
		value := "u-" + utility.RandomString(10)
		rr := f.post(t, fmt.Sprintf(settingsAddOrgPath, utility.GenerateUUID(), f.agentID), f.memberToken, value)

		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		assert.Equal(t, int64(0), f.orgSettingRows(value))
	})

	t.Run("AgentNotInOrganisationIsRejected", func(t *testing.T) {
		value := "a-" + utility.RandomString(10)
		otherOrgID := settingsAddCreateOrg(t, f.db, f.userIDs[1])
		f.orgIDs = append(f.orgIDs, otherOrgID)
		otherAgentID := settingsAddCreateAgent(t, f.db, otherOrgID, f.userIDs[1])

		rr := f.post(t, fmt.Sprintf(settingsAddOrgPath, f.orgID, otherAgentID), f.memberToken, value)

		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		assert.Equal(t, int64(0), f.orgSettingRows(value))
	})
}

func TestAddChannelAgentSetting_RequiresOrgMembership(t *testing.T) {
	f := setupSettingsAddFixture(t)

	t.Run("MemberCanAddSetting", func(t *testing.T) {
		value := "m-" + utility.RandomString(10)
		rr := f.post(t, fmt.Sprintf(settingsAddChannelPath, f.orgID, f.agentID, f.channelID), f.memberToken, value)

		assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Equal(t, int64(1), f.channelSettingRows(value))
	})

	t.Run("NonMemberIsForbidden", func(t *testing.T) {
		value := "o-" + utility.RandomString(10)
		rr := f.post(t, fmt.Sprintf(settingsAddChannelPath, f.orgID, f.agentID, f.channelID), f.outsiderToken, value)

		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		assert.Equal(t, int64(0), f.channelSettingRows(value))
	})

	t.Run("ChannelFromAnotherOrganisationIsRejected", func(t *testing.T) {
		value := "c-" + utility.RandomString(10)
		otherOrgID := settingsAddCreateOrg(t, f.db, f.userIDs[1])
		f.orgIDs = append(f.orgIDs, otherOrgID)
		otherChannelID := settingsAddCreateChannel(t, f.db, otherOrgID, f.userIDs[1])

		rr := f.post(t, fmt.Sprintf(settingsAddChannelPath, f.orgID, f.agentID, otherChannelID), f.memberToken, value)

		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		assert.Equal(t, int64(0), f.channelSettingRows(value))
	})
}
