package agents

import (
	"errors"
	"net/http"

	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage/postgresql"
)

// verifyOrgAgentAccess checks that the caller (ids["user_id"]) belongs to the organisation
// (ids["org_id"]) and that the organisation has the agent (ids["agent_id"]).
// On failure it returns the HTTP status the caller should respond with.
func verifyOrgAgentAccess(db *gorm.DB, ids map[string]string) (int, error) {
	var (
		org            models.Organisation
		orgIntegration models.OrganisationIntegrations
	)

	if isMember, err := org.CheckUserIsMemberOfOrg(ids["user_id"], ids["org_id"], db); err != nil || !isMember {
		return http.StatusForbidden, errors.New("user not a member of organisation")
	}

	if !postgresql.CheckExists(db, &orgIntegration, "org_id = ? AND integration_id = ?", ids["org_id"], ids["agent_id"]) {
		return http.StatusNotFound, errors.New("organisation does not have that agent")
	}

	return http.StatusOK, nil
}
