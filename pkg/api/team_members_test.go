package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/licensing"
	"github.com/grafana/grafana/pkg/services/sqlstore"
	"github.com/grafana/grafana/pkg/services/teamguardian/database"
	"github.com/grafana/grafana/pkg/services/teamguardian/manager"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setUpGetTeamMembersHandler(t *testing.T, sqlStore *sqlstore.SQLStore) {
	const testOrgID int64 = 1
	var userCmd models.CreateUserCommand
	team, err := sqlStore.CreateTeam("group1 name", "test1@test.com", testOrgID)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		userCmd = models.CreateUserCommand{
			Email: fmt.Sprint("user", i, "@test.com"),
			Name:  fmt.Sprint("user", i),
			Login: fmt.Sprint("loginuser", i),
		}
		// user
		user, err := sqlStore.CreateUser(context.Background(), userCmd)
		require.NoError(t, err)
		err = sqlStore.AddTeamMember(user.Id, testOrgID, team.Id, false, 1)
		require.NoError(t, err)
	}
}

func TestTeamMembersAPIEndpoint_userLoggedIn(t *testing.T) {
	settings := setting.NewCfg()
	sqlStore := sqlstore.InitTestDB(t)
	hs := &HTTPServer{
		Cfg:      settings,
		License:  &licensing.OSSLicensingService{},
		SQLStore: sqlStore,
	}

	loggedInUserScenarioWithRole(t, "When calling GET on", "GET", "api/teams/1/members",
		"api/teams/:teamId/members", models.ROLE_ADMIN, func(sc *scenarioContext) {
			setUpGetTeamMembersHandler(t, sqlStore)

			sc.handlerFunc = hs.GetTeamMembers
			sc.fakeReqWithParams("GET", sc.url, map[string]string{}).exec()

			require.Equal(t, http.StatusOK, sc.resp.Code)

			var resp []models.TeamMemberDTO
			err := json.Unmarshal(sc.resp.Body.Bytes(), &resp)
			require.NoError(t, err)
			assert.Len(t, resp, 3)
		})

	t.Run("Given there is two hidden users", func(t *testing.T) {
		settings.HiddenUsers = map[string]struct{}{
			"user1":       {},
			testUserLogin: {},
		}
		t.Cleanup(func() { settings.HiddenUsers = make(map[string]struct{}) })

		loggedInUserScenarioWithRole(t, "When calling GET on", "GET", "api/teams/1/members",
			"api/teams/:teamId/members", models.ROLE_ADMIN, func(sc *scenarioContext) {
				setUpGetTeamMembersHandler(t, sqlStore)

				sc.handlerFunc = hs.GetTeamMembers
				sc.fakeReqWithParams("GET", sc.url, map[string]string{}).exec()

				require.Equal(t, http.StatusOK, sc.resp.Code)

				var resp []models.TeamMemberDTO
				err := json.Unmarshal(sc.resp.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Len(t, resp, 3)
				assert.Equal(t, "loginuser0", resp[0].Login)
				assert.Equal(t, "loginuser1", resp[1].Login)
				assert.Equal(t, "loginuser2", resp[2].Login)
			})
	})
}

func createUser(db *sqlstore.SQLStore, orgId int64, t *testing.T) int64 {
	user, err := db.CreateUser(context.Background(), models.CreateUserCommand{
		Login:    fmt.Sprintf("TestUser%d", rand.Int()),
		OrgId:    orgId,
		Password: "password",
	})
	require.NoError(t, err)

	return user.Id
}

func setupTeamTestScenario(userCount int, db *sqlstore.SQLStore, t *testing.T) int64 {
	user, err := db.CreateUser(context.Background(), models.CreateUserCommand{SkipOrgSetup: true, Login: testUserLogin})
	require.NoError(t, err)
	testOrg, err := db.CreateOrgWithMember("TestOrg", user.Id)
	require.NoError(t, err)

	team, err := db.CreateTeam("test", "test@test.com", testOrg.Id)
	require.NoError(t, err)

	for i := 0; i < userCount; i++ {
		userId := createUser(db, testOrg.Id, t)
		require.NoError(t, err)

		err = db.AddTeamMember(userId, testOrg.Id, team.Id, false, 0)
		require.NoError(t, err)
	}

	return testOrg.Id
}

var (
	teamMemberAddRoute    = "/api/teams/%s/members"
	createTeamMemberCmd   = `{"userId": %d}`
	teamMemberUpdateRoute = "/api/teams/%s/members/%s"
	updateTeamMemberCmd   = `{"permission": %d}`
	teamMemberDeleteRoute = "/api/teams/%s/members/%s"
)

func TestAddTeamMembersAPIEndpoint_LegacyAccessControl(t *testing.T) {
	cfg := setting.NewCfg()
	cfg.EditorsCanAdmin = true
	sc := setupHTTPServerWithCfg(t, true, false, cfg)
	guardian := manager.ProvideService(database.ProvideTeamGuardianStore(sc.db))
	sc.hs.teamGuardian = guardian

	teamMemberCount := 3
	testOrgId := setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	newUserId := createUser(sc.db, testOrgId, t)
	input := strings.NewReader(fmt.Sprintf(createTeamMemberCmd, newUserId))
	t.Run("Organisation admins can add a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInEditor(sc.initCtx)
	sc.initCtx.IsGrafanaAdmin = true
	newUserId = createUser(sc.db, testOrgId, t)
	input = strings.NewReader(fmt.Sprintf(createTeamMemberCmd, newUserId))
	t.Run("Editors cannot add team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err := sc.db.AddTeamMember(sc.initCtx.UserId, 1, 1, false, 0)
	require.NoError(t, err)
	input = strings.NewReader(fmt.Sprintf(createTeamMemberCmd, newUserId))
	t.Run("Team members cannot add team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err = sc.db.UpdateTeamMember(context.Background(), &models.UpdateTeamMemberCommand{
		UserId:     sc.initCtx.UserId,
		OrgId:      1,
		TeamId:     1,
		Permission: models.PERMISSION_ADMIN,
	})
	require.NoError(t, err)
	input = strings.NewReader(fmt.Sprintf(createTeamMemberCmd, newUserId))
	t.Run("Team admins can add a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})
}

func TestAddTeamMembersAPIEndpoint_FGAC(t *testing.T) {
	sc := setupHTTPServer(t, true, true)
	sc.hs.License = &licensing.OSSLicensingService{}

	teamMemberCount := 3
	testOrgId := setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInViewer(sc.initCtx)
	newUserId := createUser(sc.db, testOrgId, t)
	input := strings.NewReader(fmt.Sprintf(createTeamMemberCmd, newUserId))
	t.Run("Access control allows adding a team member with the right permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	newUserId = createUser(sc.db, testOrgId, t)
	input = strings.NewReader(fmt.Sprintf(createTeamCmd, newUserId))
	t.Run("Access control prevents from adding a team member with the wrong permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsRead, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	setInitCtxSignedInViewer(sc.initCtx)
	t.Run("Access control prevents adding a team member with incorrect scope", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:2"}}, 1)
		response := callAPI(sc.server, http.MethodPost, fmt.Sprintf(teamMemberAddRoute, "1"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})
}

func TestUpdateTeamMembersAPIEndpoint_LegacyAccessControl(t *testing.T) {
	cfg := setting.NewCfg()
	cfg.EditorsCanAdmin = true
	sc := setupHTTPServerWithCfg(t, true, false, cfg)
	guardian := manager.ProvideService(database.ProvideTeamGuardianStore(sc.db))
	sc.hs.teamGuardian = guardian

	teamMemberCount := 3
	setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	input := strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, models.PERMISSION_ADMIN))
	t.Run("Organisation admins can update a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInEditor(sc.initCtx)
	sc.initCtx.IsGrafanaAdmin = true
	input = strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, 0))
	t.Run("Editors cannot update team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err := sc.db.AddTeamMember(sc.initCtx.UserId, 1, 1, false, 0)
	require.NoError(t, err)
	input = strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, 0))
	t.Run("Team members cannot update team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err = sc.db.UpdateTeamMember(context.Background(), &models.UpdateTeamMemberCommand{
		UserId:     sc.initCtx.UserId,
		OrgId:      1,
		TeamId:     1,
		Permission: models.PERMISSION_ADMIN,
	})
	require.NoError(t, err)
	input = strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, 0))
	t.Run("Team admins can update a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})
}

func TestUpdateTeamMembersAPIEndpoint_FGAC(t *testing.T) {
	sc := setupHTTPServer(t, true, true)
	sc.hs.License = &licensing.OSSLicensingService{}

	teamMemberCount := 3
	setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInViewer(sc.initCtx)
	input := strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, models.PERMISSION_ADMIN))
	t.Run("Access control allows updating a team member with the right permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	input = strings.NewReader(fmt.Sprintf(updateTeamMemberCmd, models.PERMISSION_ADMIN))
	t.Run("Access control prevents updating a team member with the wrong permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsRead, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	setInitCtxSignedInViewer(sc.initCtx)
	t.Run("Access control prevents updating a team member with incorrect scope", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:2"}}, 1)
		response := callAPI(sc.server, http.MethodPut, fmt.Sprintf(teamMemberUpdateRoute, "1", "2"), input, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})
}

func TestDeleteTeamMembersAPIEndpoint_LegacyAccessControl(t *testing.T) {
	cfg := setting.NewCfg()
	cfg.EditorsCanAdmin = true
	sc := setupHTTPServerWithCfg(t, true, false, cfg)
	guardian := manager.ProvideService(database.ProvideTeamGuardianStore(sc.db))
	sc.hs.teamGuardian = guardian

	teamMemberCount := 3
	setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	t.Run("Organisation admins can remove a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "2"), nil, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInEditor(sc.initCtx)
	sc.initCtx.IsGrafanaAdmin = true
	t.Run("Editors cannot remove team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "3"), nil, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err := sc.db.AddTeamMember(sc.initCtx.UserId, 1, 1, false, 0)
	require.NoError(t, err)
	t.Run("Team members cannot remove team members", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "3"), nil, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	err = sc.db.UpdateTeamMember(context.Background(), &models.UpdateTeamMemberCommand{
		UserId:     sc.initCtx.UserId,
		OrgId:      1,
		TeamId:     1,
		Permission: models.PERMISSION_ADMIN,
	})
	require.NoError(t, err)
	t.Run("Team admins can remove a team member", func(t *testing.T) {
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "3"), nil, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})
}

func TestDeleteTeamMembersAPIEndpoint_FGAC(t *testing.T) {
	sc := setupHTTPServer(t, true, true)
	sc.hs.License = &licensing.OSSLicensingService{}

	teamMemberCount := 3
	setupTeamTestScenario(teamMemberCount, sc.db, t)

	setInitCtxSignedInViewer(sc.initCtx)
	t.Run("Access control allows removing a team member with the right permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "2"), nil, t)
		assert.Equal(t, http.StatusOK, response.Code)
	})

	setInitCtxSignedInOrgAdmin(sc.initCtx)
	t.Run("Access control prevents removing a team member with the wrong permissions", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsRead, Scope: "teams:id:1"}}, 1)
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "3"), nil, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})

	setInitCtxSignedInViewer(sc.initCtx)
	t.Run("Access control prevents removing a team member with incorrect scope", func(t *testing.T) {
		setAccessControlPermissions(sc.acmock, []*accesscontrol.Permission{{Action: accesscontrol.ActionTeamsPermissionsWrite, Scope: "teams:id:2"}}, 1)
		response := callAPI(sc.server, http.MethodDelete, fmt.Sprintf(teamMemberDeleteRoute, "1", "3"), nil, t)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})
}
