package test_agents

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/agents"
	"github.com/hngprojects/telex_be/pkg/controller/auth"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

const settingsReadPath = "/api/v1/organisations/%s/settings/agents/%s"

var settingsReadOrgChildTables = []struct{ table, column string }{
	{"integration_settings", "org_id"},
	{"organisation_integrations", "org_id"},
	{"invitations", "organisation_id"},
	{"credit_usages", "organisation_id"},
	{"credit_transactions", "organisation_id"},
	{"channel_participants", "org_id"},
	{"channels", "organisation_id"},
	{"dm_channels", "org_id"},
	{"dm_favourites", "org_id"},
	{"org_user_managements", "organisation_id"},
	{"organisation_plans", "organisation_id"},
	{"org_roles", "organisation_id"},
	{"user_organisations", "organisation_id"},
}

type settingsReadUser struct {
	id    string
	token string
}

type settingsReadFixture struct {
	router      *gin.Engine
	orgID       string
	agentID     string
	fieldLabel  string
	fieldSecret string
	member      settingsReadUser
	outsider    settingsReadUser
}

func settingsReadNewRouter(db *storage.Database) (*gin.Engine, auth.Controller) {
	logger := utility.NewLogger()
	extReq := request.ExternalRequest{Logger: logger, Test: true}

	authCtrl := auth.Controller{Db: db, Validator: validator.New(), Logger: logger, ExtReq: extReq}
	agentsCtrl := agents.Controller{Db: db, Validator: validator.New(), Logger: logger, ExtReq: extReq}

	r := gin.New()
	r.GET("/api/v1/organisations/:org_id/settings/agents/:agent_id",
		middleware.Authorize(db.Postgresql), agentsCtrl.GetAgentSettings)

	return r, authCtrl
}

func settingsReadNewUser(t *testing.T, r *gin.Engine, authCtrl auth.Controller, db *storage.Database, label string) settingsReadUser {
	t.Helper()

	suffix := utility.GenerateUUID()
	signup := models.CreateUserRequestModel{
		Email:       fmt.Sprintf("settingsread-%s-%s@qa.team", label, suffix),
		PhoneNumber: fmt.Sprintf("+234%v", utility.GetRandomNumbersInRange(7000000000, 9099999999)),
		FirstName:   "SettingsRead",
		LastName:    label,
		Password:    "password",
		UserName:    fmt.Sprintf("settingsread_%s_%s", label, suffix),
	}
	login := models.LoginRequestModel{Email: signup.Email, Password: signup.Password}

	tst.SignupUser(t, r, authCtrl, signup, false)
	token := tst.GetLoginToken(t, r, authCtrl, login)
	if token == "" {
		t.Fatalf("failed to obtain a login token for the %s user", label)
	}

	user := settingsReadUser{id: tst.GetUserIDFromToken(t, token, db), token: token}

	t.Cleanup(func() {
		var orgIDs []string
		db.Postgresql.Table("user_organisations").Where("user_id = ?", user.id).Pluck("organisation_id", &orgIDs)
		for _, orgID := range orgIDs {
			settingsReadDeleteOrg(db.Postgresql, orgID)
		}
		db.Postgresql.Exec("DELETE FROM user_channels WHERE user_id = ?", user.id)
		db.Postgresql.Exec("DELETE FROM access_tokens WHERE owner_id = ?", user.id)
		db.Postgresql.Exec("DELETE FROM profiles WHERE userid = ?", user.id)
		db.Postgresql.Exec("DELETE FROM users WHERE id = ?", user.id)
	})

	return user
}

func settingsReadDeleteOrg(db *gorm.DB, orgID string) {
	for _, child := range settingsReadOrgChildTables {
		db.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s = ?", child.table, child.column), orgID)
	}
	db.Exec("DELETE FROM organisations WHERE id = ?", orgID)
}

// settingsReadNewFixture creates an organisation that owns an agent with one
// stored setting. The member user belongs to the organisation; the outsider
// is a logged-in user who only belongs to their own personal organisation.
func settingsReadNewFixture(t *testing.T) settingsReadFixture {
	t.Helper()

	_ = tst.Setup()
	gin.SetMode(gin.TestMode)

	db := storage.Connection()
	r, authCtrl := settingsReadNewRouter(db)

	f := settingsReadFixture{
		router:      r,
		orgID:       utility.GenerateUUID(),
		agentID:     utility.GenerateUUID(),
		fieldLabel:  "api_key",
		fieldSecret: "settings-read-secret-" + utility.GenerateUUID(),
		member:      settingsReadNewUser(t, r, authCtrl, db, "member"),
		outsider:    settingsReadNewUser(t, r, authCtrl, db, "outsider"),
	}

	org := models.Organisation{
		ID:      f.orgID,
		Name:    "Settings Read Org " + utility.RandomString(5),
		Email:   fmt.Sprintf("settingsread-org-%s@qa.team", utility.RandomString(8)),
		OwnerID: f.member.id,
		Type:    "test",
		Country: "NG",
	}
	if err := db.Postgresql.Create(&org).Error; err != nil {
		t.Fatalf("failed to create organisation: %v", err)
	}

	if err := db.Postgresql.Exec(
		"INSERT INTO user_organisations (user_id, organisation_id) VALUES (?, ?)", f.member.id, f.orgID,
	).Error; err != nil {
		t.Fatalf("failed to add the member to the organisation: %v", err)
	}

	integration := models.Integrations{
		ID:           f.agentID,
		Name:         "settings-read-agent-" + utility.RandomString(8),
		OwnerID:      f.member.id,
		PreSharedKey: utility.GenerateUUID(),
	}
	if err := db.Postgresql.Create(&integration).Error; err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	orgIntegration := models.OrganisationIntegrations{
		ID:            utility.GenerateUUID(),
		OrgID:         f.orgID,
		IntegrationID: f.agentID,
		OwnerID:       f.member.id,
		AppName:       integration.Name,
		IsActive:      true,
	}
	if err := db.Postgresql.Create(&orgIntegration).Error; err != nil {
		t.Fatalf("failed to attach the agent to the organisation: %v", err)
	}

	setting := models.IntegrationSettings{
		ID:             utility.GenerateUUID(),
		OrgID:          &f.orgID,
		IntegrationID:  f.agentID,
		FormFieldLabel: f.fieldLabel,
		FormFieldValue: f.fieldSecret,
	}
	if err := db.Postgresql.Create(&setting).Error; err != nil {
		t.Fatalf("failed to create agent setting: %v", err)
	}

	t.Cleanup(func() {
		db.Postgresql.Exec("DELETE FROM integrations WHERE id = ?", f.agentID)
		settingsReadDeleteOrg(db.Postgresql, f.orgID)
	})

	return f
}

func settingsReadGet(f settingsReadFixture, token, orgID string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf(settingsReadPath, orgID, f.agentID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func TestGetAgentSettings_MemberReadsSettings(t *testing.T) {
	f := settingsReadNewFixture(t)

	rr := settingsReadGet(f, f.member.token, f.orgID)

	tst.AssertStatusCode(t, rr.Code, http.StatusOK)

	body := rr.Body.String()
	if !strings.Contains(body, f.fieldSecret) {
		t.Fatalf("expected the member to receive the agent setting, got body: %s", body)
	}
	if !strings.Contains(body, f.fieldLabel) {
		t.Fatalf("expected the setting label %q in the body, got: %s", f.fieldLabel, body)
	}
}

func TestGetAgentSettings_NonMemberIsForbidden(t *testing.T) {
	f := settingsReadNewFixture(t)

	rr := settingsReadGet(f, f.outsider.token, f.orgID)

	tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)

	body := rr.Body.String()
	if strings.Contains(body, f.fieldSecret) {
		t.Fatalf("the settings of another organisation were leaked to a non-member: %s", body)
	}
}

func TestGetAgentSettings_NonMemberUnknownOrgIsForbidden(t *testing.T) {
	f := settingsReadNewFixture(t)

	rr := settingsReadGet(f, f.outsider.token, utility.GenerateUUID())

	tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
}
