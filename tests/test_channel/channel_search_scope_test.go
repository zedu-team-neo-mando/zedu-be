package test_channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/gofrs/uuid"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/auth"
	"github.com/hngprojects/telex_be/pkg/controller/channel"
	"github.com/hngprojects/telex_be/pkg/controller/organisation"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

func TestSearchChannelsByNameIsScopedToCallerOrganisation(t *testing.T) {
	logger := tst.Setup()
	db := storage.Connection()
	validatorRef := validator.New()

	currUUID := uuid.Must(uuid.NewV4()).String()

	authCtrl := auth.Controller{
		Db: db, Validator: validatorRef, Logger: logger,
		ExtReq: request.ExternalRequest{Logger: logger, Test: true},
	}
	channelCtrl := channel.Controller{Db: db, Validator: validatorRef, Logger: logger}
	orgCtrl := organisation.Controller{Db: db, Validator: validatorRef, Logger: logger}

	r := gin.Default()
	r.GET("/api/v1/channels/search/:channelName", middleware.Authorize(db.Postgresql), channelCtrl.SearchChannelsByNames)
	r.POST("/api/v1/organisations", middleware.Authorize(db.Postgresql), orgCtrl.CreateOrganisation)

	signUp := models.CreateUserRequestModel{
		Email:       fmt.Sprintf("chansearch_%v@qa.team", currUUID),
		PhoneNumber: fmt.Sprintf("+234%v", utility.GetRandomNumbersInRange(7000000000, 9099999999)),
		FirstName:   "Chan",
		LastName:    "Searcher",
		Password:    "password",
		UserName:    fmt.Sprintf("chansearch_%v", currUUID),
	}
	tst.SignupUser(t, r, authCtrl, signUp, false)

	var caller models.User
	if err := db.Postgresql.Where("email = ?", signUp.Email).First(&caller).Error; err != nil {
		t.Fatalf("failed to load caller: %v", err)
	}

	setupToken := tst.GetLoginToken(t, r, authCtrl, models.LoginRequestModel{
		Email:    signUp.Email,
		Password: signUp.Password,
	})
	newOrg := func(name string) string {
		var body bytes.Buffer
		json.NewEncoder(&body).Encode(models.CreateOrgRequestModel{
			Name:        fmt.Sprintf("%s%s", name, currUUID),
			Description: "search scope test",
			Email:       signUp.Email,
			Type:        "test",
			Location:    "test",
			Country:     "test",
		})
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/organisations", &body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+setupToken)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("failed to create organisation %s: %d %s", name, rr.Code, rr.Body.String())
		}
		data, _ := tst.ParseResponse(rr)["data"].(map[string]any)
		orgID, _ := data["id"].(string)
		if orgID == "" {
			t.Fatalf("organisation %s has no id: %s", name, rr.Body.String())
		}
		return orgID
	}
	callerOrg := newOrg("SearchScopeCaller")
	otherOrg := newOrg("SearchScopeOther")

	if err := db.Postgresql.Model(&models.User{}).Where("id = ?", caller.ID).Update("current_org", callerOrg).Error; err != nil {
		t.Fatalf("failed to set current org: %v", err)
	}

	prefix := fmt.Sprintf("scope%s", currUUID[:8])
	newChannel := func(suffix, orgID string, private bool) models.Channels {
		ch := models.Channels{
			ID:             utility.GenerateUUID(),
			Name:           prefix + suffix,
			Description:    "search scope test",
			OrganisationID: orgID,
			OwnerId:        caller.ID,
			IsPrivate:      private,
		}
		if err := db.Postgresql.Create(&ch).Error; err != nil {
			t.Fatalf("failed to create channel %s: %v", ch.Name, err)
		}
		return ch
	}

	public := newChannel("-public", callerOrg, false)
	privateMember := newChannel("-private-member", callerOrg, true)
	privateOther := newChannel("-private-other", callerOrg, true)
	otherOrgChannel := newChannel("-other-org", otherOrg, false)

	if err := db.Postgresql.Create(&models.UserChannels{
		ChannelsID: privateMember.ID,
		UserID:     caller.ID,
		Username:   signUp.UserName,
	}).Error; err != nil {
		t.Fatalf("failed to add caller to private channel: %v", err)
	}

	token := tst.GetLoginToken(t, r, authCtrl, models.LoginRequestModel{
		Email:    signUp.Email,
		Password: signUp.Password,
	})

	search := func(t *testing.T, term string) []map[string]any {
		t.Helper()
		path := "/api/v1/channels/search/" + url.PathEscape(term)
		req, _ := http.NewRequest(http.MethodGet, path+"?page=1&limit=50", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		tst.AssertStatusCode(t, rr.Code, http.StatusOK)

		resp := tst.ParseResponse(rr)
		raw, _ := resp["data"].([]any)
		results := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			results = append(results, item.(map[string]any))
		}
		return results
	}

	t.Run("returns only the caller's organisation and visible channels", func(t *testing.T) {
		got := map[string]bool{}
		for _, ch := range search(t, prefix) {
			got[ch["channels_id"].(string)] = true
			if _, ok := ch["users"]; ok {
				t.Errorf("channel %v exposes its member list", ch["name"])
			}
		}

		if !got[public.ID] {
			t.Errorf("expected public channel in caller's organisation to be returned")
		}
		if !got[privateMember.ID] {
			t.Errorf("expected private channel the caller belongs to be returned")
		}
		if got[privateOther.ID] {
			t.Errorf("private channel the caller does not belong to must not be returned")
		}
		if got[otherOrgChannel.ID] {
			t.Errorf("channel from another organisation must not be returned")
		}
	})

	t.Run("a percent sign is matched literally, not as a wildcard", func(t *testing.T) {
		if results := search(t, "%"); len(results) != 0 {
			t.Errorf("expected no channels for a literal %%, got %d", len(results))
		}
	})
}
