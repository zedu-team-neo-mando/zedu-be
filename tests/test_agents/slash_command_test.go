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

type slashCommandFixture struct {
	router      *gin.Engine
	db          *storage.Database
	memberToken string
	outsiderTok string
	orgID       string
	agentID     string
	userIDs     []string
	orgIDs      []string
}

func setupSlashCommandFixture(t *testing.T) *slashCommandFixture {
	t.Helper()
	logger := tst.Setup()
	gin.SetMode(gin.TestMode)

	db := storage.Connection()
	validate := validator.New()
	extReq := request.ExternalRequest{Logger: logger, Test: true}

	authCtrl := auth.Controller{Db: db, Validator: validate, Logger: logger, ExtReq: extReq}
	agentsCtrl := agents.Controller{Db: db, Validator: validate, Logger: logger, ExtReq: extReq}

	r := gin.New()
	r.POST("/api/v1/organisations/:org_id/agents/:agent_id/slash-commands",
		middleware.Authorize(db.Postgresql), agentsCtrl.AddAgentSlashCommand)

	f := &slashCommandFixture{router: r, db: db}

	memberToken, memberID := createUser(t, r, authCtrl, db, "member")
	outsiderToken, outsiderID := createUser(t, r, authCtrl, db, "outsider")
	f.memberToken, f.outsiderTok = memberToken, outsiderToken
	f.userIDs = []string{memberID, outsiderID}

	f.orgID = createOrg(t, db, memberID)
	f.orgIDs = append(f.orgIDs, f.orgID)
	f.agentID = createOrgAgent(t, db, f.orgID, memberID)

	t.Cleanup(func() { f.cleanup() })
	return f
}

func createUser(t *testing.T, r *gin.Engine, authCtrl auth.Controller, db *storage.Database, label string) (string, string) {
	t.Helper()
	id := utility.GenerateUUID()
	user := models.CreateUserRequestModel{
		Email:       fmt.Sprintf("slash%s%s@qa.team", label, id),
		PhoneNumber: fmt.Sprintf("+234%v", utility.GetRandomNumbersInRange(7000000000, 9099999999)),
		FirstName:   "Slash",
		LastName:    label,
		Password:    "password",
		UserName:    fmt.Sprintf("slash%s%s", label, id),
	}
	tst.SignupUser(t, r, authCtrl, user, false)
	token := tst.GetLoginToken(t, r, authCtrl, models.LoginRequestModel{Email: user.Email, Password: user.Password})
	return token, tst.GetUserIDFromToken(t, token, db)
}

func createOrg(t *testing.T, db *storage.Database, ownerID string) string {
	t.Helper()
	orgID := utility.GenerateUUID()
	org := models.Organisation{
		ID:      orgID,
		Name:    fmt.Sprintf("Slash Org %s", utility.RandomString(5)),
		Email:   fmt.Sprintf("slashorg%s@qa.team", utility.RandomString(5)),
		OwnerID: ownerID,
		Type:    "test",
		Country: "NG",
	}
	require.NoError(t, db.Postgresql.Create(&org).Error)
	require.NoError(t, db.Postgresql.Exec(
		"INSERT INTO user_organisations (user_id, organisation_id) VALUES (?, ?)", ownerID, orgID).Error)
	return orgID
}

func createOrgAgent(t *testing.T, db *storage.Database, orgID, ownerID string) string {
	t.Helper()
	agent := models.OrganisationIntegrations{
		ID:            utility.GenerateUUID(),
		OrgID:         orgID,
		IntegrationID: utility.GenerateUUID(),
		OwnerID:       ownerID,
		AppName:       "slash-test-agent",
		IsActive:      true,
	}
	require.NoError(t, db.Postgresql.Create(&agent).Error)
	return agent.IntegrationID
}

func (f *slashCommandFixture) cleanup() {
	for _, orgID := range f.orgIDs {
		f.db.Postgresql.Exec("DELETE FROM slash_commands WHERE org_id = ?", orgID)
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

func (f *slashCommandFixture) addSlashCommand(t *testing.T, token, orgID, agentID, command string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(models.AddSlashCommandRequest{
		Command:       command,
		ProcessingURL: "https://example.com/hook",
		Description:   "test command",
	})
	url := fmt.Sprintf("/api/v1/organisations/%s/agents/%s/slash-commands", orgID, agentID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func (f *slashCommandFixture) countCommands(command string) int64 {
	var n int64
	f.db.Postgresql.Model(&models.SlashCommand{}).Where("command = ?", command).Count(&n)
	return n
}

func TestAddAgentSlashCommand_MemberSucceeds(t *testing.T) {
	f := setupSlashCommandFixture(t)
	command := "/m" + utility.RandomString(8)

	rr := f.addSlashCommand(t, f.memberToken, f.orgID, f.agentID, command)

	assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, int64(1), f.countCommands(command))
}

func TestAddAgentSlashCommand_NonMemberForbidden(t *testing.T) {
	f := setupSlashCommandFixture(t)
	command := "/o" + utility.RandomString(8)

	rr := f.addSlashCommand(t, f.outsiderTok, f.orgID, f.agentID, command)

	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Equal(t, int64(0), f.countCommands(command))
}

func TestAddAgentSlashCommand_AgentNotInOrgRejected(t *testing.T) {
	f := setupSlashCommandFixture(t)
	command := "/a" + utility.RandomString(8)

	// An agent that belongs to a different organisation than the one in the URL.
	otherOrgID := createOrg(t, f.db, f.userIDs[1])
	f.orgIDs = append(f.orgIDs, otherOrgID)
	otherAgentID := createOrgAgent(t, f.db, otherOrgID, f.userIDs[1])

	rr := f.addSlashCommand(t, f.memberToken, f.orgID, otherAgentID, command)

	assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	assert.Equal(t, int64(0), f.countCommands(command))
}
