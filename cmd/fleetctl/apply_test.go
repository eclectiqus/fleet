package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/fleetdm/fleet/v4/pkg/optjson"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/mock"
	nanomdm_mock "github.com/fleetdm/fleet/v4/server/mock/nanomdm"
	"github.com/fleetdm/fleet/v4/server/ptr"
	"github.com/fleetdm/fleet/v4/server/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var userRoleSpecList = []*fleet.User{
	{
		UpdateCreateTimestamps: fleet.UpdateCreateTimestamps{
			CreateTimestamp: fleet.CreateTimestamp{CreatedAt: time.Now()},
			UpdateTimestamp: fleet.UpdateTimestamp{UpdatedAt: time.Now()},
		},
		ID:         42,
		Name:       "Test Name admin1@example.com",
		Email:      "admin1@example.com",
		GlobalRole: ptr.String(fleet.RoleAdmin),
	},
	{
		UpdateCreateTimestamps: fleet.UpdateCreateTimestamps{
			CreateTimestamp: fleet.CreateTimestamp{CreatedAt: time.Now()},
			UpdateTimestamp: fleet.UpdateTimestamp{UpdatedAt: time.Now()},
		},
		ID:         23,
		Name:       "Test Name2 admin2@example.com",
		Email:      "admin2@example.com",
		GlobalRole: nil,
		Teams:      []fleet.UserTeam{},
	},
}

func TestApplyUserRoles(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}

	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}

	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		return &fleet.Team{
			ID:        1,
			CreatedAt: time.Now(),
			Name:      "team1",
		}, nil
	}

	ds.SaveUsersFunc = func(ctx context.Context, users []*fleet.User) error {
		for _, u := range users {
			switch u.Email {
			case "admin1@example.com":
				userRoleList[0] = u
			case "admin2@example.com":
				userRoleList[1] = u
			}
		}
		return nil
	}

	tmpFile, err := os.CreateTemp(os.TempDir(), "*.yml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(`
---
apiVersion: v1
kind: user_roles
spec:
  roles:
    admin1@example.com:
      global_role: admin
      teams: null
    admin2@example.com:
      global_role: null
      teams:
      - role: maintainer
        team: team1
`)
	require.NoError(t, err)
	assert.Equal(t, "[+] applied user roles\n", runAppForTest(t, []string{"apply", "-f", tmpFile.Name()}))
	require.Len(t, userRoleSpecList[1].Teams, 1)
	assert.Equal(t, fleet.RoleMaintainer, userRoleSpecList[1].Teams[0].Role)
}

func TestApplyTeamSpecs(t *testing.T) {
	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	_, ds := runServerWithMockedDS(t, &service.TestServerOpts{License: license})

	teamsByName := map[string]*fleet.Team{
		"team1": {
			ID:          42,
			Name:        "team1",
			Description: "team1 description",
		},
	}

	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		team, ok := teamsByName[name]
		if !ok {
			return nil, sql.ErrNoRows
		}
		return team, nil
	}

	i := 1
	ds.NewTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
		team.ID = uint(i)
		i++
		teamsByName[team.Name] = team
		return team, nil
	}

	agentOpts := json.RawMessage(`{"config":{"foo":"bar"},"overrides":{"platforms":{"darwin":{"foo":"override"}}}}`)
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{AgentOptions: &agentOpts}, nil
	}

	ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
		teamsByName[team.Name] = team
		return team, nil
	}

	enrolledSecretsCalled := make(map[uint][]*fleet.EnrollSecret)
	ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
		enrolledSecretsCalled[*teamID] = secrets
		return nil
	}

	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	filename := writeTmpYml(t, `
---
apiVersion: v1
kind: team
spec:
  team:
    name: team2
---
apiVersion: v1
kind: team
spec:
  team:
    agent_options:
      config:
        views:
          foo: bar
    name: team1
    secrets:
      - secret: AAA
    mdm:
      macos_updates:
        minimum_version: 12.3.1
        deadline: 2011-03-01
`)

	newAgentOpts := json.RawMessage(`{"config":{"views":{"foo":"bar"}}}`)
	newMDMSettings := fleet.TeamMDM{
		MacOSUpdates: fleet.MacOSUpdates{
			MinimumVersion: "12.3.1",
			Deadline:       "2011-03-01",
		},
	}
	require.Equal(t, "[+] applied 2 teams\n", runAppForTest(t, []string{"apply", "-f", filename}))
	assert.JSONEq(t, string(agentOpts), string(*teamsByName["team2"].Config.AgentOptions))
	assert.JSONEq(t, string(newAgentOpts), string(*teamsByName["team1"].Config.AgentOptions))
	assert.Equal(t, []*fleet.EnrollSecret{{Secret: "AAA"}}, enrolledSecretsCalled[uint(42)])
	assert.Equal(t, fleet.TeamMDM{}, teamsByName["team2"].Config.MDM)
	assert.Equal(t, newMDMSettings, teamsByName["team1"].Config.MDM)
	assert.True(t, ds.ApplyEnrollSecretsFuncInvoked)
	ds.ApplyEnrollSecretsFuncInvoked = false

	filename = writeTmpYml(t, `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
`)

	require.Equal(t, "[+] applied 1 teams\n", runAppForTest(t, []string{"apply", "-f", filename}))
	assert.Equal(t, []*fleet.EnrollSecret{{Secret: "AAA"}}, enrolledSecretsCalled[uint(42)])
	assert.False(t, ds.ApplyEnrollSecretsFuncInvoked)
	// agent options not provided, so left unchanged
	assert.JSONEq(t, string(newAgentOpts), string(*teamsByName["team1"].Config.AgentOptions))
	assert.Equal(t, fleet.TeamMDM{}, teamsByName["team1"].Config.MDM)

	filename = writeTmpYml(t, `
apiVersion: v1
kind: team
spec:
  team:
    agent_options:
      config:
        views:
          foo: qux
    name: team1
    mdm:
      macos_updates:
        minimum_version: 10.10.10
        deadline: 1992-03-01
    secrets:
      - secret: BBB
`)

	newMDMSettings = fleet.TeamMDM{
		MacOSUpdates: fleet.MacOSUpdates{
			MinimumVersion: "10.10.10",
			Deadline:       "1992-03-01",
		},
	}
	newAgentOpts = json.RawMessage(`{"config":{"views":{"foo":"qux"}}}`)
	require.Equal(t, "[+] applied 1 teams\n", runAppForTest(t, []string{"apply", "-f", filename}))
	assert.JSONEq(t, string(newAgentOpts), string(*teamsByName["team1"].Config.AgentOptions))
	assert.Equal(t, newMDMSettings, teamsByName["team1"].Config.MDM)
	assert.Equal(t, []*fleet.EnrollSecret{{Secret: "BBB"}}, enrolledSecretsCalled[uint(42)])
	assert.True(t, ds.ApplyEnrollSecretsFuncInvoked)

	filename = writeTmpYml(t, `
apiVersion: v1
kind: team
spec:
  team:
    agent_options:
    name: team1
`)

	require.Equal(t, "[+] applied 1 teams\n", runAppForTest(t, []string{"apply", "-f", filename}))
	// agent options provided but empty, clears the value
	assert.Nil(t, teamsByName["team1"].Config.AgentOptions)
}

func writeTmpYml(t *testing.T, contents string) string {
	tmpFile, err := os.CreateTemp(t.TempDir(), "*.yml")
	require.NoError(t, err)
	_, err = tmpFile.WriteString(contents)
	require.NoError(t, err)
	return tmpFile.Name()
}

func writeTmpJSON(t *testing.T, v any) string {
	tmpFile, err := os.CreateTemp(t.TempDir(), "*.json")
	require.NoError(t, err)
	err = json.NewEncoder(tmpFile).Encode(v)
	require.NoError(t, err)
	return tmpFile.Name()
}

func TestApplyAppConfig(t *testing.T) {
	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	_, ds := runServerWithMockedDS(t, &service.TestServerOpts{License: license})

	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}

	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}
	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		return &fleet.Team{ID: 123}, nil
	}

	defaultAgentOpts := json.RawMessage(`{"config":{"foo":"bar"}}`)
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{
			OrgInfo:        fleet.OrgInfo{OrgName: "Fleet"},
			ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"},
			AgentOptions:   &defaultAgentOpts,
		}, nil
	}

	var savedAppConfig *fleet.AppConfig
	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		savedAppConfig = config
		return nil
	}

	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  features:
    enable_host_users: false
    enable_software_inventory: false
  mdm:
    apple_bm_default_team: "team1"
    macos_updates:
      minimum_version: 12.1.1
      deadline: 2011-02-01
`)

	newMDMSettings := fleet.MDM{
		AppleBMDefaultTeam:  "team1",
		AppleBMTermsExpired: false,
		MacOSUpdates: fleet.MacOSUpdates{
			MinimumVersion: "12.1.1",
			Deadline:       "2011-02-01",
		},
	}
	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.NotNil(t, savedAppConfig)
	assert.False(t, savedAppConfig.Features.EnableHostUsers)
	assert.False(t, savedAppConfig.Features.EnableSoftwareInventory)
	assert.Equal(t, newMDMSettings, savedAppConfig.MDM)
	// agent options were not modified, since they were not provided
	assert.Equal(t, string(defaultAgentOpts), string(*savedAppConfig.AgentOptions))

	name = writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  features:
    enable_host_users: true
    enable_software_inventory: true
  agent_options:
  mdm:
    macos_updates:
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.NotNil(t, savedAppConfig)
	assert.True(t, savedAppConfig.Features.EnableHostUsers)
	assert.True(t, savedAppConfig.Features.EnableSoftwareInventory)
	// agent options were cleared, provided but empty
	assert.Nil(t, savedAppConfig.AgentOptions)
	assert.Equal(t, newMDMSettings, savedAppConfig.MDM)
}

func TestApplyAppConfigDryRunIssue(t *testing.T) {
	// reproduces the bug fixed by https://github.com/fleetdm/fleet/pull/8194
	_, ds := runServerWithMockedDS(t)

	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}

	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}

	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	currentAppConfig := &fleet.AppConfig{
		OrgInfo: fleet.OrgInfo{OrgName: "Fleet"}, ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"},
	}
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return currentAppConfig, nil
	}

	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		currentAppConfig = config
		return nil
	}

	// first, set the default app config's agent options as set after fleetctl setup
	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  agent_options:
    config:
      decorators:
        load:
        - SELECT uuid AS host_uuid FROM system_info;
        - SELECT hostname AS hostname FROM system_info;
      options:
        disable_distributed: false
        distributed_interval: 10
        distributed_plugin: tls
        distributed_tls_max_attempts: 3
        logger_tls_endpoint: /api/osquery/log
        logger_tls_period: 10
        pack_delimiter: /
    overrides: {}
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))

	// then, dry-run a valid app config's agent options, which made the original
	// app config's agent options invalid JSON (when it shouldn't have modified
	// it at all - the issue was in the cached_mysql datastore, it did not clone
	// the app config properly).
	name = writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  agent_options:
    overrides:
      platforms:
        darwin:
          auto_table_construction:
            tcc_system_entries:
              query: "SELECT service, client, allowed, prompt_count, last_modified FROM access"
              path: "/Library/Application Support/com.apple.TCC/TCC.db"
              columns:
                - "service"
                - "client"
                - "allowed"
                - "prompt_count"
                - "last_modified"
`)

	assert.Equal(t, "[+] would've applied fleet config\n", runAppForTest(t, []string{"apply", "--dry-run", "-f", name}))

	// the saved app config was left unchanged, still equal to the original agent
	// options
	got := runAppForTest(t, []string{"get", "config"})
	assert.Contains(t, got, `agent_options:
    config:
      decorators:
        load:
        - SELECT uuid AS host_uuid FROM system_info;
        - SELECT hostname AS hostname FROM system_info;
      options:
        disable_distributed: false
        distributed_interval: 10
        distributed_plugin: tls
        distributed_tls_max_attempts: 3
        logger_tls_endpoint: /api/osquery/log
        logger_tls_period: 10
        pack_delimiter: /
    overrides: {}`)
}

func TestApplyAppConfigUnknownFields(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}

	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{}, nil
	}

	var savedAppConfig *fleet.AppConfig
	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		savedAppConfig = config
		return nil
	}

	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  features:
    enabled_software_inventory: false # typo, correct config is enable_software_inventory
`)

	runAppCheckErr(t, []string{"apply", "-f", name},
		"applying fleet config: PATCH /api/latest/fleet/config received status 400 Bad Request: unsupported key provided: \"enabled_software_inventory\"",
	)
	require.Nil(t, savedAppConfig)
}

func TestApplyAppConfigDeprecatedFields(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}

	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{OrgInfo: fleet.OrgInfo{OrgName: "Fleet"}, ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"}}, nil
	}

	var savedAppConfig *fleet.AppConfig
	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		savedAppConfig = config
		return nil
	}

	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  host_settings:
    enable_host_users: false
    enable_software_inventory: false
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.NotNil(t, savedAppConfig)
	assert.False(t, savedAppConfig.Features.EnableHostUsers)
	assert.False(t, savedAppConfig.Features.EnableSoftwareInventory)

	name = writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  host_settings:
    enable_host_users: true
    enable_software_inventory: true
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.NotNil(t, savedAppConfig)
	assert.True(t, savedAppConfig.Features.EnableHostUsers)
	assert.True(t, savedAppConfig.Features.EnableSoftwareInventory)
}

const (
	policySpec = `---
apiVersion: v1
kind: policy
spec:
  name: Is Gatekeeper enabled on macOS devices?
  query: SELECT 1 FROM gatekeeper WHERE assessments_enabled = 1;
  description: Checks to make sure that the Gatekeeper feature is enabled on macOS devices. Gatekeeper tries to ensure only trusted software is run on a mac machine.
  resolution: "Run the following command in the Terminal app: /usr/sbin/spctl --master-enable"
  platform: darwin
  team: Team1
---
apiVersion: v1
kind: policy
spec:
  name: Is disk encryption enabled on Windows devices?
  query: SELECT 1 FROM bitlocker_info where protection_status = 1;
  description: Checks to make sure that device encryption is enabled on Windows devices.
  resolution: "Option 1: Select the Start button. Select Settings  > Update & Security  > Device encryption. If Device encryption doesn't appear, skip to Option 2. If device encryption is turned off, select Turn on. Option 2: Select the Start button. Under Windows System, select Control Panel. Select System and Security. Under BitLocker Drive Encryption, select Manage BitLocker. Select Turn on BitLocker and then follow the instructions."
  platform: windows
---
apiVersion: v1
kind: policy
spec:
  name: Is Filevault enabled on macOS devices?
  query: SELECT 1 FROM disk_encryption WHERE user_uuid IS NOT “” AND filevault_status = ‘on’ LIMIT 1;
  description: Checks to make sure that the Filevault feature is enabled on macOS devices.
  resolution: "Choose Apple menu > System Preferences, then click Security & Privacy. Click the FileVault tab. Click the Lock icon, then enter an administrator name and password. Click Turn On FileVault."
  platform: darwin
`
	enrollSecretsSpec = `---
apiVersion: v1
kind: enroll_secret
spec:
  secrets:
    - secret: RzTlxPvugG4o4O5IKS/HqEDJUmI1hwBoffff
    - secret: reallyworks
    - secret: thissecretwontwork!
`
	labelsSpec = `---
apiVersion: v1
kind: label
spec:
  name: pending_updates
  query: select 1;
  platforms:
    - darwin
`
	packsSpec = `---
apiVersion: v1
kind: pack
spec:
  name: osquery_monitoring
  queries:
    - query: osquery_version
      name: osquery_version_snapshot
      interval: 7200
      snapshot: true
    - query: osquery_version
      name: osquery_version_differential
      interval: 7200
`
	queriesSpec = `---
apiVersion: v1
kind: query
spec:
  description: Retrieves the list of application scheme/protocol-based IPC handlers.
  name: app_schemes
  query: select * from app_schemes;
`
)

func TestApplyPolicies(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	var appliedPolicySpecs []*fleet.PolicySpec
	ds.ApplyPolicySpecsFunc = func(ctx context.Context, authorID uint, specs []*fleet.PolicySpec) error {
		appliedPolicySpecs = specs
		return nil
	}
	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		if name == "Team1" {
			return &fleet.Team{ID: 123}, nil
		}
		return nil, errors.New("unexpected team name!")
	}
	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	name := writeTmpYml(t, policySpec)

	assert.Equal(t, "[+] applied 3 policies\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyPolicySpecsFuncInvoked)
	assert.Len(t, appliedPolicySpecs, 3)
	for _, p := range appliedPolicySpecs {
		assert.NotEmpty(t, p.Platform)
	}
	assert.True(t, ds.TeamByNameFuncInvoked)
}

func mobileconfigForTest(name, identifier string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array/>
	<key>PayloadDisplayName</key>
	<string>%s</string>
	<key>PayloadIdentifier</key>
	<string>%s</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>%s</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>
`, name, identifier, uuid.New().String()))
}

func TestApplyAsGitOps(t *testing.T) {
	enqueuer := new(nanomdm_mock.Storage)
	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	_, ds := runServerWithMockedDS(t, &service.TestServerOpts{
		License:    license,
		MDMStorage: enqueuer,
		MDMPusher:  mockPusher{},
	})

	gitOps := &fleet.User{
		Name:       "GitOps",
		Password:   []byte("p4ssw0rd.123"),
		Email:      "gitops1@example.com",
		GlobalRole: ptr.String(fleet.RoleGitOps),
	}
	gitOps, err := ds.NewUser(context.Background(), gitOps)
	require.NoError(t, err)
	ds.SessionByKeyFunc = func(ctx context.Context, key string) (*fleet.Session, error) {
		return &fleet.Session{
			CreateTimestamp: fleet.CreateTimestamp{CreatedAt: time.Now()},
			ID:              1,
			AccessedAt:      time.Now(),
			UserID:          gitOps.ID,
			Key:             key,
		}, nil
	}
	ds.UserByIDFunc = func(ctx context.Context, id uint) (*fleet.User, error) {
		return gitOps, nil
	}
	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	// Apply global config.
	currentAppConfig := &fleet.AppConfig{
		OrgInfo: fleet.OrgInfo{
			OrgName: "Fleet",
		},
		ServerSettings: fleet.ServerSettings{
			ServerURL: "https://example.org",
		},
		MDM: fleet.MDM{
			EnabledAndConfigured: true,
		},
	}
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return currentAppConfig, nil
	}

	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		currentAppConfig = config
		return nil
	}
	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  features:
    enable_host_users: true
    enable_software_inventory: true
  agent_options:
    config:
      decorators:
        load:
        - SELECT uuid AS host_uuid FROM system_info;
        - SELECT hostname AS hostname FROM system_info;
      options:
        disable_distributed: false
        distributed_interval: 10
        distributed_plugin: tls
        distributed_tls_max_attempts: 3
        logger_tls_endpoint: /api/osquery/log
        logger_tls_period: 10
        pack_delimiter: /
    overrides: {}
`)
	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, currentAppConfig.Features.EnableHostUsers)

	// Apply team config.
	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		if name == "Team1" {
			return &fleet.Team{ID: 123}, nil
		}
		return nil, errors.New("unexpected team name!")
	}
	var savedTeam *fleet.Team
	ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
		savedTeam = team
		return team, nil
	}
	var teamEnrollSecrets []*fleet.EnrollSecret
	ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
		if teamID == nil || *teamID != 123 {
			return fmt.Errorf("unexpected data: %+v", teamID)
		}
		teamEnrollSecrets = secrets
		return nil
	}
	ds.BatchSetMDMAppleProfilesFunc = func(ctx context.Context, teamID *uint, profiles []*fleet.MDMAppleConfigProfile) error {
		return nil
	}
	ds.BulkSetPendingMDMAppleHostProfilesFunc = func(ctx context.Context, hostIDs, teamIDs, profileIDs []uint, hostUUIDs []string) error {
		return nil
	}

	mobileConfig := mobileconfigForTest("foo", "bar")
	mobileConfigPath := filepath.Join(t.TempDir(), "foo.mobileconfig")
	err = os.WriteFile(mobileConfigPath, mobileConfig, 0o644)
	require.NoError(t, err)

	name = writeTmpYml(t, fmt.Sprintf(`
apiVersion: v1
kind: team
spec:
  team:
    agent_options:
      config:
        views:
          foo: qux
    name: Team1
    mdm:
      macos_updates:
        minimum_version: 10.10.10
        deadline: 1992-03-01
      macos_settings:
        custom_settings:
          - %s
        enable_disk_encryption: false
    secrets:
      - secret: BBB
`, mobileConfigPath))

	require.Equal(t, "[+] applied 1 teams\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.JSONEq(t, string(json.RawMessage(`{"config":{"views":{"foo":"qux"}}}`)), string(*savedTeam.Config.AgentOptions))
	assert.Equal(t, fleet.TeamMDM{
		MacOSSettings: fleet.MacOSSettings{
			CustomSettings:       []string{mobileConfigPath},
			EnableDiskEncryption: false,
		},
		MacOSUpdates: fleet.MacOSUpdates{
			MinimumVersion: "10.10.10",
			Deadline:       "1992-03-01",
		},
	}, savedTeam.Config.MDM)
	assert.Equal(t, []*fleet.EnrollSecret{{Secret: "BBB"}}, teamEnrollSecrets)
	assert.True(t, ds.ApplyEnrollSecretsFuncInvoked)
	assert.True(t, ds.BatchSetMDMAppleProfilesFuncInvoked)

	// Apply policies.
	var appliedPolicySpecs []*fleet.PolicySpec
	ds.ApplyPolicySpecsFunc = func(ctx context.Context, authorID uint, specs []*fleet.PolicySpec) error {
		appliedPolicySpecs = specs
		return nil
	}
	name = writeTmpYml(t, policySpec)
	assert.Equal(t, "[+] applied 3 policies\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyPolicySpecsFuncInvoked)
	assert.Len(t, appliedPolicySpecs, 3)
	for _, p := range appliedPolicySpecs {
		assert.NotEmpty(t, p.Platform)
	}
	assert.True(t, ds.TeamByNameFuncInvoked)

	// Apply enroll secrets.
	var appliedSecrets []*fleet.EnrollSecret
	ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
		appliedSecrets = secrets
		return nil
	}
	name = writeTmpYml(t, enrollSecretsSpec)
	assert.Equal(t, "[+] applied enroll secrets\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyEnrollSecretsFuncInvoked)
	assert.Len(t, appliedSecrets, 3)
	for _, s := range appliedSecrets {
		assert.NotEmpty(t, s.Secret)
	}

	// Apply labels.
	var appliedLabels []*fleet.LabelSpec
	ds.ApplyLabelSpecsFunc = func(ctx context.Context, specs []*fleet.LabelSpec) error {
		appliedLabels = specs
		return nil
	}
	name = writeTmpYml(t, labelsSpec)
	assert.Equal(t, "[+] applied 1 labels\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyLabelSpecsFuncInvoked)
	require.Len(t, appliedLabels, 1)
	assert.Equal(t, "pending_updates", appliedLabels[0].Name)
	assert.Equal(t, "select 1;", appliedLabels[0].Query)

	// Apply packs.
	var appliedPacks []*fleet.PackSpec
	ds.ApplyPackSpecsFunc = func(ctx context.Context, specs []*fleet.PackSpec) error {
		appliedPacks = specs
		return nil
	}
	ds.ListPacksFunc = func(ctx context.Context, opt fleet.PackListOptions) ([]*fleet.Pack, error) {
		return nil, nil
	}
	name = writeTmpYml(t, packsSpec)
	assert.Equal(t, "[+] applied 1 packs\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyPackSpecsFuncInvoked)
	require.Len(t, appliedPacks, 1)
	assert.Equal(t, "osquery_monitoring", appliedPacks[0].Name)
	require.Len(t, appliedPacks[0].Queries, 2)

	// Apply queries.
	var appliedQueries []*fleet.Query
	ds.QueryByNameFunc = func(ctx context.Context, name string, opts ...fleet.OptionalArg) (*fleet.Query, error) {
		return nil, sql.ErrNoRows
	}
	ds.ApplyQueriesFunc = func(ctx context.Context, authorID uint, queries []*fleet.Query) error {
		appliedQueries = queries
		return nil
	}
	name = writeTmpYml(t, queriesSpec)
	assert.Equal(t, "[+] applied 1 queries\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyQueriesFuncInvoked)
	require.Len(t, appliedQueries, 1)
	assert.Equal(t, "app_schemes", appliedQueries[0].Name)
	assert.Equal(t, "select * from app_schemes;", appliedQueries[0].Query)
}

func TestApplyEnrollSecrets(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	var appliedSecrets []*fleet.EnrollSecret
	ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
		appliedSecrets = secrets
		return nil
	}

	name := writeTmpYml(t, enrollSecretsSpec)

	assert.Equal(t, "[+] applied enroll secrets\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyEnrollSecretsFuncInvoked)
	assert.Len(t, appliedSecrets, 3)
	for _, s := range appliedSecrets {
		assert.NotEmpty(t, s.Secret)
	}
}

func TestApplyLabels(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	var appliedLabels []*fleet.LabelSpec
	ds.ApplyLabelSpecsFunc = func(ctx context.Context, specs []*fleet.LabelSpec) error {
		appliedLabels = specs
		return nil
	}

	name := writeTmpYml(t, labelsSpec)

	assert.Equal(t, "[+] applied 1 labels\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyLabelSpecsFuncInvoked)
	require.Len(t, appliedLabels, 1)
	assert.Equal(t, "pending_updates", appliedLabels[0].Name)
	assert.Equal(t, "select 1;", appliedLabels[0].Query)
}

func TestApplyPacks(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	ds.ListPacksFunc = func(ctx context.Context, opt fleet.PackListOptions) ([]*fleet.Pack, error) {
		return nil, nil
	}
	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	var appliedPacks []*fleet.PackSpec
	ds.ApplyPackSpecsFunc = func(ctx context.Context, specs []*fleet.PackSpec) error {
		appliedPacks = specs
		return nil
	}

	name := writeTmpYml(t, packsSpec)

	assert.Equal(t, "[+] applied 1 packs\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyPackSpecsFuncInvoked)
	require.Len(t, appliedPacks, 1)
	assert.Equal(t, "osquery_monitoring", appliedPacks[0].Name)
	require.Len(t, appliedPacks[0].Queries, 2)

	interval := writeTmpYml(t, `---
apiVersion: v1
kind: pack
spec:
  name: test_bad_interval
  queries:
    - query: good_interval
      name: good_interval
      interval: 7200
    - query: bad_interval
      name: bad_interval
      interval: 604801
`)

	expectedErrMsg := "applying packs: POST /api/latest/fleet/spec/packs received status 400 Bad request: pack payload verification: pack scheduled query interval must be an integer greater than 1 and less than 604800"

	_, err := runAppNoChecks([]string{"apply", "-f", interval})
	assert.Error(t, err)
	require.Equal(t, expectedErrMsg, err.Error())
}

func TestApplyQueries(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	var appliedQueries []*fleet.Query
	ds.QueryByNameFunc = func(ctx context.Context, name string, opts ...fleet.OptionalArg) (*fleet.Query, error) {
		return nil, sql.ErrNoRows
	}
	ds.ApplyQueriesFunc = func(ctx context.Context, authorID uint, queries []*fleet.Query) error {
		appliedQueries = queries
		return nil
	}
	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
		return nil
	}

	name := writeTmpYml(t, queriesSpec)

	assert.Equal(t, "[+] applied 1 queries\n", runAppForTest(t, []string{"apply", "-f", name}))
	assert.True(t, ds.ApplyQueriesFuncInvoked)
	require.Len(t, appliedQueries, 1)
	assert.Equal(t, "app_schemes", appliedQueries[0].Name)
	assert.Equal(t, "select * from app_schemes;", appliedQueries[0].Query)
}

func TestCanApplyIntervalsInNanoseconds(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	// Stubs
	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}
	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{OrgInfo: fleet.OrgInfo{OrgName: "Fleet"}, ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"}}, nil
	}

	var savedAppConfig *fleet.AppConfig
	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		savedAppConfig = config
		return nil
	}

	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  webhook_settings:
    interval: 30000000000
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.Equal(t, savedAppConfig.WebhookSettings.Interval.Duration, 30*time.Second)
}

func TestCanApplyIntervalsUsingDurations(t *testing.T) {
	_, ds := runServerWithMockedDS(t)

	// Stubs
	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return userRoleSpecList, nil
	}
	ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
		if email == "admin1@example.com" {
			return userRoleSpecList[0], nil
		}
		return userRoleSpecList[1], nil
	}
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{OrgInfo: fleet.OrgInfo{OrgName: "Fleet"}, ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"}}, nil
	}

	var savedAppConfig *fleet.AppConfig
	ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
		savedAppConfig = config
		return nil
	}

	name := writeTmpYml(t, `---
apiVersion: v1
kind: config
spec:
  webhook_settings:
    interval: 30s
`)

	assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
	require.Equal(t, savedAppConfig.WebhookSettings.Interval.Duration, 30*time.Second)
}

func TestApplyMacosSetup(t *testing.T) {
	mockStore := struct {
		sync.Mutex
		appConfig *fleet.AppConfig
		metaHash  []byte
	}{}

	setupServer := func(t *testing.T, premium bool) *mock.Store {
		tier := fleet.TierFree
		if premium {
			tier = fleet.TierPremium
		}
		license := &fleet.LicenseInfo{Tier: tier, Expiration: time.Now().Add(24 * time.Hour)}
		_, ds := runServerWithMockedDS(t, &service.TestServerOpts{License: license})

		tm1 := &fleet.Team{ID: 1, Name: "tm1"}
		teamsByName := map[string]*fleet.Team{
			"tm1": tm1,
		}
		teamsByID := map[uint]*fleet.Team{
			tm1.ID: tm1,
		}
		ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
			return nil
		}

		ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
			team, ok := teamsByName[name]
			if !ok {
				// TeamByName in the real Datastore does not return notFoundError, it
				// returns ErrNoRows directly, we're a bit inconsistent with that at
				// the moment. This is important as ApplyTeamSpecs checks if TeamByName
				// returns an error that wraps ErrNoRows (and not an IsNotFound).
				return nil, sql.ErrNoRows
			}
			clone := *team
			return &clone, nil
		}

		tmID := 1 // new teams will start at 2
		ds.NewTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
			tmID++
			team.ID = uint(tmID)
			clone := *team
			teamsByName[team.Name] = &clone
			teamsByID[team.ID] = &clone
			return team, nil
		}

		ds.TeamFunc = func(ctx context.Context, id uint) (*fleet.Team, error) {
			tm, ok := teamsByID[id]
			if !ok {
				return nil, &notFoundError{}
			}
			clone := *tm
			return &clone, nil
		}

		ds.ListTeamsFunc = func(ctx context.Context, filter fleet.TeamFilter, opt fleet.ListOptions) ([]*fleet.Team, error) {
			tms := make([]*fleet.Team, 0, len(teamsByName))
			for _, tm := range teamsByName {
				clone := *tm
				tms = append(tms, &clone)
			}
			sort.Slice(tms, func(i, j int) bool {
				l, r := tms[i], tms[j]
				return l.Name < r.Name
			})
			return tms, nil
		}

		// initialize mockConfig
		mockStore.Lock()
		mockStore.appConfig = &fleet.AppConfig{
			OrgInfo:        fleet.OrgInfo{OrgName: "Fleet"},
			ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"},
			MDM:            fleet.MDM{EnabledAndConfigured: true},
		}
		mockStore.Unlock()
		ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
			mockStore.Lock()
			defer mockStore.Unlock()
			clone, err := mockStore.appConfig.Clone()
			return clone.(*fleet.AppConfig), err
		}

		ds.SaveAppConfigFunc = func(ctx context.Context, info *fleet.AppConfig) error {
			mockStore.Lock()
			defer mockStore.Unlock()
			clone, err := info.Clone()
			if err != nil {
				return err
			}
			mockStore.appConfig = clone.(*fleet.AppConfig)
			return nil
		}

		ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
			teamsByName[team.Name] = team
			teamsByID[team.ID] = team
			return team, nil
		}

		asstsByTeam := make(map[uint]*fleet.MDMAppleSetupAssistant)
		asstID := 0
		ds.SetOrUpdateMDMAppleSetupAssistantFunc = func(ctx context.Context, asst *fleet.MDMAppleSetupAssistant) (*fleet.MDMAppleSetupAssistant, error) {
			asstID++
			asst.ID = uint(asstID)
			asst.UploadedAt = time.Now()

			var tmID uint
			if asst.TeamID != nil {
				tmID = *asst.TeamID
			}
			asstsByTeam[tmID] = asst

			return asst, nil
		}

		ds.DeleteMDMAppleSetupAssistantFunc = func(ctx context.Context, teamID *uint) error {
			var tmID uint
			if teamID != nil {
				tmID = *teamID
			}
			delete(asstsByTeam, tmID)
			return nil
		}

		ds.GetMDMAppleSetupAssistantFunc = func(ctx context.Context, teamID *uint) (*fleet.MDMAppleSetupAssistant, error) {
			var tmID uint
			if teamID != nil {
				tmID = *teamID
			}
			if asst, ok := asstsByTeam[tmID]; ok {
				return asst, nil
			}
			return nil, &notFoundError{}
		}
		ds.InsertMDMAppleBootstrapPackageFunc = func(ctx context.Context, bp *fleet.MDMAppleBootstrapPackage) error {
			return nil
		}
		ds.DeleteMDMAppleBootstrapPackageFunc = func(ctx context.Context, teamID uint) error {
			return nil
		}
		ds.GetMDMAppleBootstrapPackageMetaFunc = func(ctx context.Context, teamID uint) (*fleet.MDMAppleBootstrapPackage, error) {
			return nil, nil
		}
		return ds
	}

	emptyMacosSetup := writeTmpJSON(t, map[string]any{})
	invalidWebURLMacosSetup := writeTmpJSON(t, map[string]any{
		"configuration_web_url": "https://example.com",
	})
	invalidAwaitDeviceMacosSetup := writeTmpJSON(t, map[string]any{
		"await_device_configured": true,
	})
	invalidURLMacosSetup := writeTmpJSON(t, map[string]any{
		"url": "https://example.com",
	})

	const (
		appConfigSpec = `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_setup:
      bootstrap_package: %s
      macos_setup_assistant: %s
`
		appConfigNoKeySpec = `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_setup:
`
		team1Spec = `
apiVersion: v1
kind: team
spec:
  team:
    name: tm1
    mdm:
      macos_setup:
        bootstrap_package: %s
        macos_setup_assistant: %s
`
		team1NoKeySpec = `
apiVersion: v1
kind: team
spec:
  team:
    name: tm1
    mdm:
      macos_setup:
`
		team1And2Spec = `
apiVersion: v1
kind: team
spec:
  team:
    name: tm1
    mdm:
      macos_setup:
        bootstrap_package: %s
        macos_setup_assistant: %s
---
apiVersion: v1
kind: team
spec:
  team:
    name: tm2
    mdm:
      macos_setup:
        bootstrap_package: %s
        macos_setup_assistant: %s
`
	)

	t.Run("free license", func(t *testing.T) {
		ds := setupServer(t, false)

		// appconfig macos setup assistant
		name := writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", emptyMacosSetup))
		runAppCheckErr(t, []string{"apply", "-f", name}, `applying fleet config: missing or invalid license`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "https://example.com", ""))
		runAppCheckErr(t, []string{"apply", "-f", name}, `applying fleet config: missing or invalid license`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// team macos setup assistant
		name = writeTmpYml(t, fmt.Sprintf(team1Spec, "", emptyMacosSetup))
		runAppCheckErr(t, []string{"apply", "-f", name}, `applying teams: missing or invalid license`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)

		name = writeTmpYml(t, fmt.Sprintf(team1Spec, "https://example.com", ""))
		runAppCheckErr(t, []string{"apply", "-f", name}, `applying teams: missing or invalid license`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)
	})

	t.Run("setup assistant invalid file, not json, invalid json", func(t *testing.T) {
		ds := setupServer(t, true)

		// create invalid json file
		tmpFile, err := os.CreateTemp(t.TempDir(), "*.json")
		require.NoError(t, err)
		_, err = tmpFile.WriteString(`not json`)
		require.NoError(t, err)
		invalidJSON := tmpFile.Name()

		// appconfig invalid file
		name := writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", "no_such_file.json"))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `no such file`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// appconfig not .json
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", "no_such_file.txt"))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `Couldn’t edit macos_setup_assistant. The file should be a .json file.`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// appconfig invalid json
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", invalidJSON))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `The file should include valid JSON`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// team invalid file
		name = writeTmpYml(t, fmt.Sprintf(team1Spec, "", "no_such_file.json"))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `no such file`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)

		// team not .json
		name = writeTmpYml(t, fmt.Sprintf(team1Spec, "", "no_such_file.txt"))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `Couldn’t edit macos_setup_assistant. The file should be a .json file.`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)

		// team invalid json
		name = writeTmpYml(t, fmt.Sprintf(team1Spec, "", invalidJSON))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, `The file should include valid JSON`)
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)
	})

	t.Run("setup assistant get and apply roundtrip", func(t *testing.T) {
		ds := setupServer(t, true)

		b, err := os.ReadFile(filepath.Join("testdata", "macosSetupExpectedAppConfigEmpty.yml"))
		require.NoError(t, err)
		expectedEmptyAppCfg := string(b)

		b, err = os.ReadFile(filepath.Join("testdata", "macosSetupExpectedAppConfigSet.yml"))
		require.NoError(t, err)
		expectedAppCfgSet := fmt.Sprintf(string(b), "", emptyMacosSetup)

		b, err = os.ReadFile(filepath.Join("testdata", "macosSetupExpectedTeam1Empty.yml"))
		require.NoError(t, err)
		expectedEmptyTm1 := string(b)

		b, err = os.ReadFile(filepath.Join("testdata", "macosSetupExpectedTeam1And2Empty.yml"))
		require.NoError(t, err)
		expectedEmptyTm1And2 := string(b)

		b, err = os.ReadFile(filepath.Join("testdata", "macosSetupExpectedTeam1And2Set.yml"))
		require.NoError(t, err)
		expectedTm1And2Set := fmt.Sprintf(string(b), "", emptyMacosSetup)

		// get without setup assistant set
		assert.YAMLEq(t, expectedEmptyAppCfg, runAppForTest(t, []string{"get", "config", "--yaml"}))
		assert.YAMLEq(t, expectedEmptyTm1, runAppForTest(t, []string{"get", "teams", "--yaml"}))

		// apply with dry-run, appconfig
		name := writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", emptyMacosSetup))
		assert.Equal(t, "[+] would've applied fleet config\n", runAppForTest(t, []string{"apply", "--dry-run", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// apply with dry-run, teams
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", emptyMacosSetup, "", emptyMacosSetup))
		assert.Equal(t, "[+] would've applied 2 teams\n", runAppForTest(t, []string{"apply", "--dry-run", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)

		// get, setup assistant still not set
		assert.YAMLEq(t, expectedEmptyAppCfg, runAppForTest(t, []string{"get", "config", "--yaml"}))
		assert.YAMLEq(t, expectedEmptyTm1, runAppForTest(t, []string{"get", "teams", "--yaml"}))

		// apply appconfig for real
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", emptyMacosSetup))
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.True(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)

		// apply teams for real
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", emptyMacosSetup, "", emptyMacosSetup))
		ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked = false
		assert.Equal(t, "[+] applied 2 teams\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.True(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveTeamFuncInvoked)

		// get, setup assistant is now set
		assert.YAMLEq(t, expectedAppCfgSet, runAppForTest(t, []string{"get", "config", "--yaml"}))
		assert.YAMLEq(t, expectedTm1And2Set, runAppForTest(t, []string{"get", "teams", "--yaml"}))

		// clear with dry-run, appconfig
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", ""))
		ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked = false
		ds.SaveAppConfigFuncInvoked = false
		assert.Equal(t, "[+] would've applied fleet config\n", runAppForTest(t, []string{"apply", "--dry-run", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveAppConfigFuncInvoked)

		// clear with dry-run, teams
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", "", "", ""))
		ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked = false
		ds.SaveTeamFuncInvoked = false
		assert.Equal(t, "[+] would've applied 2 teams\n", runAppForTest(t, []string{"apply", "--dry-run", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.SaveTeamFuncInvoked)

		// apply appconfig without the setup assistant key
		name = writeTmpYml(t, appConfigNoKeySpec)
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)

		// apply team 1 without the setup assistant key
		name = writeTmpYml(t, team1NoKeySpec)
		assert.Equal(t, "[+] applied 1 teams\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveTeamFuncInvoked)

		// get, results unchanged
		assert.YAMLEq(t, expectedAppCfgSet, runAppForTest(t, []string{"get", "config", "--yaml"}))
		assert.YAMLEq(t, expectedTm1And2Set, runAppForTest(t, []string{"get", "teams", "--yaml"}))

		// clear appconfig for real
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", ""))
		ds.SaveAppConfigFuncInvoked = false
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)

		// clear teams for real
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", "", "", ""))
		ds.SaveTeamFuncInvoked = false
		assert.Equal(t, "[+] applied 2 teams\n", runAppForTest(t, []string{"apply", "-f", name}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.DeleteMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.SaveTeamFuncInvoked)

		// get, results now empty
		assert.YAMLEq(t, expectedEmptyAppCfg, runAppForTest(t, []string{"get", "config", "--yaml"}))
		assert.YAMLEq(t, expectedEmptyTm1And2, runAppForTest(t, []string{"get", "teams", "--yaml"}))

		// apply appconfig with invalid key #1
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", invalidWebURLMacosSetup))
		ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked = false
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include configuration_web_url.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)

		// apply appconfig with invalid key #2
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", invalidAwaitDeviceMacosSetup))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include await_device_configured.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)

		// apply appconfig with invalid key #3
		name = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", invalidURLMacosSetup))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include url.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)

		// apply teams with invalid key #1
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", invalidWebURLMacosSetup, "", invalidWebURLMacosSetup))
		ds.SaveTeamFuncInvoked = false
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include configuration_web_url.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)

		// apply teams with invalid key #2
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", invalidAwaitDeviceMacosSetup, "", invalidAwaitDeviceMacosSetup))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include await_device_configured.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)

		// apply teams with invalid key #3
		name = writeTmpYml(t, fmt.Sprintf(team1And2Spec, "", invalidURLMacosSetup, "", invalidURLMacosSetup))
		_, err = runAppNoChecks([]string{"apply", "-f", name})
		require.ErrorContains(t, err, "The automatic enrollment profile can’t include url.")
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
	})

	t.Run("new bootstrap package", func(t *testing.T) {
		cases := []struct {
			pkgName     string
			expectedErr error
		}{
			{"signed.pkg", nil},
			{"unsigned.pkg", errors.New("applying fleet config: Couldn’t edit bootstrap_package. The bootstrap_package must be signed. Learn how to sign the package in the Fleet documentation: https://fleetdm.com/docs/using-fleet/mdm-macos-setup#step-2-sign-the-package")},
			{"invalid.tar.gz", errors.New("applying fleet config: Couldn’t edit bootstrap_package. The file must be a package (.pkg).")},
			{"wrong-toc.pkg", errors.New("applying fleet config: checking package signature: decompressing TOC: unexpected EOF")},
		}

		for _, c := range cases {
			t.Run(c.pkgName, func(t *testing.T) {
				pkgBytes, err := os.ReadFile(filepath.Join("../../server/service/testdata/bootstrap-packages", c.pkgName))
				require.NoError(t, err)

				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", strconv.Itoa(len(pkgBytes)))
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment;filename="%s"`, c.pkgName))
					if n, err := w.Write(pkgBytes); err != nil {
						require.NoError(t, err)
						require.Equal(t, len(pkgBytes), n)
					}
				}))
				defer srv.Close()

				ds := setupServer(t, true)
				ds.InsertMDMAppleBootstrapPackageFunc = func(ctx context.Context, bp *fleet.MDMAppleBootstrapPackage) error {
					require.Equal(t, len(bp.Bytes), len(pkgBytes))
					return nil
				}
				ds.GetMDMAppleBootstrapPackageMetaFunc = func(ctx context.Context, teamID uint) (*fleet.MDMAppleBootstrapPackage, error) {
					return nil, &notFoundError{}
				}

				mockStore.Lock()
				assert.Equal(t, "", mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
				mockStore.Unlock()

				// create the app config yaml with server url for bootstrap package
				tmpFilename := writeTmpYml(t, fmt.Sprintf(appConfigSpec, srv.URL, ""))

				if c.expectedErr != nil {
					runAppCheckErr(t, []string{"apply", "-f", tmpFilename}, c.expectedErr.Error())
					assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
					assert.False(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
					assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
					assert.False(t, ds.SaveAppConfigFuncInvoked)
					mockStore.Lock()
					assert.Equal(t, "", mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
					mockStore.Unlock()
				} else {
					assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", tmpFilename}))
					assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
					assert.True(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
					assert.True(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
					assert.True(t, ds.SaveAppConfigFuncInvoked)
					mockStore.Lock()
					assert.Equal(t, srv.URL, mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
					mockStore.Unlock()
				}
			})
		}
	})

	t.Run("replace bootstrap package", func(t *testing.T) {
		pkgName := "signed.pkg"
		pkgBytes, err := os.ReadFile(filepath.Join("../../server/service/testdata/bootstrap-packages", pkgName))
		require.NoError(t, err)
		pkgHash := sha256.New()
		n, err := io.Copy(pkgHash, bytes.NewReader(pkgBytes))
		require.NoError(t, err)
		require.Equal(t, int64(len(pkgBytes)), n)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(pkgBytes)))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment;filename="%s"`, pkgName))
			if n, err := w.Write(pkgBytes); err != nil {
				require.NoError(t, err)
				require.Equal(t, len(pkgBytes), n)
			}
		}))
		defer srv.Close()

		ds := setupServer(t, true)
		ds.InsertMDMAppleBootstrapPackageFunc = func(ctx context.Context, bp *fleet.MDMAppleBootstrapPackage) error {
			mockStore.Lock()
			defer mockStore.Unlock()
			require.Equal(t, pkgName, bp.Name)
			require.Equal(t, len(bp.Bytes), len(pkgBytes))
			require.Equal(t, pkgHash.Sum(nil), bp.Sha256)
			mockStore.metaHash = bp.Sha256
			return nil
		}
		ds.DeleteMDMAppleBootstrapPackageFunc = func(ctx context.Context, teamID uint) error {
			require.Equal(t, uint(0), teamID)
			return nil
		}
		ds.GetMDMAppleBootstrapPackageMetaFunc = func(ctx context.Context, teamID uint) (*fleet.MDMAppleBootstrapPackage, error) {
			mockStore.Lock()
			defer mockStore.Unlock()
			return &fleet.MDMAppleBootstrapPackage{
				TeamID:    0,
				Name:      pkgName,
				Sha256:    mockStore.metaHash,
				Token:     "token",
				CreatedAt: time.Now().Add(-1 * time.Hour),
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			}, nil
		}

		mockStore.Lock()
		mockStore.metaHash = []byte("foobar")                                                          // initial hash is a throwaway
		mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage = optjson.SetString("https://example.com") // initial value is a throwaway
		mockStore.Unlock()

		// upload a new package
		tmpFilename := writeTmpYml(t, fmt.Sprintf(appConfigSpec, srv.URL, ""))
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", tmpFilename}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.True(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.True(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)
		mockStore.Lock()
		assert.Equal(t, srv.URL, mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
		mockStore.Unlock()

		ds.GetMDMAppleBootstrapPackageMetaFuncInvoked = false
		ds.InsertMDMAppleBootstrapPackageFuncInvoked = false
		ds.DeleteMDMAppleBootstrapPackageFuncInvoked = false
		ds.SaveAppConfigFuncInvoked = false

		// running again should not re-upload
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", tmpFilename}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.False(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)
		mockStore.Lock()
		assert.Equal(t, srv.URL, mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
		mockStore.Unlock()

		ds.GetMDMAppleBootstrapPackageMetaFuncInvoked = false
		ds.InsertMDMAppleBootstrapPackageFuncInvoked = false
		ds.DeleteMDMAppleBootstrapPackageFuncInvoked = false
		ds.SaveAppConfigFuncInvoked = false

		// empty server url should delete the package
		tmpFilename = writeTmpYml(t, fmt.Sprintf(appConfigSpec, "", ""))
		assert.Equal(t, "[+] applied fleet config\n", runAppForTest(t, []string{"apply", "-f", tmpFilename}))
		assert.False(t, ds.SetOrUpdateMDMAppleSetupAssistantFuncInvoked)
		assert.True(t, ds.GetMDMAppleBootstrapPackageMetaFuncInvoked)
		assert.False(t, ds.InsertMDMAppleBootstrapPackageFuncInvoked)
		assert.True(t, ds.DeleteMDMAppleBootstrapPackageFuncInvoked)
		assert.True(t, ds.SaveAppConfigFuncInvoked)
		mockStore.Lock()
		assert.Equal(t, "", mockStore.appConfig.MDM.MacOSSetup.BootstrapPackage.Value)
		mockStore.Unlock()
	})
}

func TestApplySpecs(t *testing.T) {
	// create a macos setup json file (content not important)
	macSetupFile := writeTmpJSON(t, map[string]any{})

	setupDS := func(ds *mock.Store) {
		// labels
		ds.ApplyLabelSpecsFunc = func(ctx context.Context, specs []*fleet.LabelSpec) error {
			return nil
		}

		// teams - team ID 1 already exists
		teamsByName := map[string]*fleet.Team{
			"team1": {
				ID:          1,
				Name:        "team1",
				Description: "team1 description",
			},
		}

		ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
			team, ok := teamsByName[name]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return team, nil
		}

		i := 1 // new teams will start at 2
		ds.NewTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
			i++
			team.ID = uint(i)
			teamsByName[team.Name] = team
			return team, nil
		}

		agentOpts := json.RawMessage(`{"config":{}}`)
		ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
			return &fleet.AppConfig{AgentOptions: &agentOpts}, nil
		}

		ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
			teamsByName[team.Name] = team
			return team, nil
		}

		ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
			return nil
		}

		// activities
		ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activity fleet.ActivityDetails) error {
			return nil
		}

		// app config
		ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
			return userRoleSpecList, nil
		}

		ds.UserByEmailFunc = func(ctx context.Context, email string) (*fleet.User, error) {
			if email == "admin1@example.com" {
				return userRoleSpecList[0], nil
			}
			return userRoleSpecList[1], nil
		}

		ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
			return &fleet.AppConfig{OrgInfo: fleet.OrgInfo{OrgName: "Fleet"}, ServerSettings: fleet.ServerSettings{ServerURL: "https://example.org"}}, nil
		}

		ds.SaveAppConfigFunc = func(ctx context.Context, config *fleet.AppConfig) error {
			return nil
		}
	}

	cases := []struct {
		desc       string
		flags      []string
		spec       string
		wantOutput string
		wantErr    string
	}{
		{
			desc: "empty team spec",
			spec: `
apiVersion: v1
kind: team
spec:
`,
			wantOutput: "[+] applied 1 teams",
		},
		{
			desc: "empty team name",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: ""
`,
			wantErr: `422 Validation Failed: name may not be empty`,
		},
		{
			desc: "invalid agent options for existing team",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    agent_options:
      config:
        blah: nope
`,
			wantErr: `400 Bad Request: unsupported key provided: "blah"`,
		},
		{
			desc: "invalid top-level key for team",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    blah: nope
`,
			wantErr: `400 Bad Request: unsupported key provided: "blah"`,
		},
		{
			desc: "invalid known key's value type for team cannot be forced",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: 123
`,
			flags:   []string{"--force"},
			wantErr: `400 Bad Request: invalid value type at 'specs.name': expected string but got number`,
		},
		{
			desc: "unknown key for team can be forced",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    blah: true
`,
			flags:      []string{"--force"},
			wantOutput: `[+] applied 1 teams`,
		},
		{
			desc: "invalid agent options for new team",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      config:
        blah: nope
`,
			wantErr: `400 Bad Request: unsupported key provided: "blah"`,
		},
		{
			desc: "invalid agent options dry-run",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      config:
        blah: nope
`,
			flags:   []string{"--dry-run"},
			wantErr: `400 Bad Request: unsupported key provided: "blah"`,
		},
		{
			desc: "invalid agent options force",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      config:
        blah: nope
`,
			flags:      []string{"--force"},
			wantOutput: `[+] applied 1 teams`,
		},
		{
			desc: "invalid agent options field type",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      config:
        options:
          aws_debug: 123
`,
			flags:   []string{"--dry-run"},
			wantErr: `400 Bad Request: invalid value type at 'options.aws_debug': expected bool but got number`,
		},
		{
			desc: "invalid team agent options command-line flag",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      command_line_flags:
        no_such_flag: 123
`,
			wantErr: `400 Bad Request: unsupported key provided: "no_such_flag"`,
		},
		{
			desc: "valid team agent options command-line flag",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      command_line_flags:
        enable_tables: "abc"
`,
			wantOutput: `[+] applied 1 teams`,
		},
		{
			desc: "invalid agent options field type in overrides",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
    agent_options:
      config:
        options:
          aws_debug: true
      overrides:
        platforms:
          darwin:
            options:
              aws_debug: 123
`,
			wantErr: `400 Bad Request: invalid value type at 'options.aws_debug': expected bool but got number`,
		},
		{
			desc: "empty config",
			spec: `
apiVersion: v1
kind: config
spec:
`,
			wantOutput: ``, // no output for empty config
		},
		{
			desc: "config with blank required org name",
			spec: `
apiVersion: v1
kind: config
spec:
  org_info:
    org_name: ""
`,
			wantErr: `422 Validation Failed: organization name must be present`,
		},
		{
			desc: "config with blank required server url",
			spec: `
apiVersion: v1
kind: config
spec:
  server_settings:
    server_url: ""
`,
			wantErr: `422 Validation Failed: Fleet server URL must be present`,
		},
		{
			desc: "config with unknown key",
			spec: `
apiVersion: v1
kind: config
spec:
  server_settings:
    foo: bar
`,
			wantErr: `400 Bad Request: unsupported key provided: "foo"`,
		},
		{
			desc: "config with invalid key type",
			spec: `
apiVersion: v1
kind: config
spec:
  server_settings:
    server_url: 123
`,
			wantErr: `400 Bad request: failed to decode app config`,
		},
		{
			desc: "config with invalid agent options in dry-run",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    foo: bar
`,
			flags:   []string{"--dry-run"},
			wantErr: `400 Bad Request: unsupported key provided: "foo"`,
		},
		{
			desc: "config with invalid agent options data type in dry-run",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    config:
      options:
        aws_debug: 123
`,
			flags:   []string{"--dry-run"},
			wantErr: `400 Bad Request: invalid value type at 'options.aws_debug': expected bool but got number`,
		},
		{
			desc: "config with invalid agent options data type with force",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    config:
      options:
        aws_debug: 123
`,
			flags:      []string{"--force"},
			wantOutput: `[+] applied fleet config`,
		},
		{
			desc: "config with invalid agent options command-line flags",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    command_line_flags:
      enable_tables: "foo"
      no_such_flag: false
`,
			wantErr: `400 Bad Request: unsupported key provided: "no_such_flag"`,
		},
		{
			desc: "config with invalid value for agent options command-line flags",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    command_line_flags:
      enable_tables: 123
`,
			wantErr: `400 Bad Request: invalid value type at 'enable_tables': expected string but got number`,
		},
		{
			desc: "config with valid agent options command-line flags",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    command_line_flags:
      enable_tables: "abc"
`,
			wantOutput: `[+] applied fleet config`,
		},
		{
			desc: "dry-run set with unsupported spec",
			spec: `
apiVersion: v1
kind: label
spec:
  name: label1
  query: SELECT 1
`,
			flags:      []string{"--dry-run"},
			wantOutput: `[!] ignoring labels, dry run mode only supported for 'config' and 'team' specs`,
		},
		{
			desc: "dry-run set with various specs, appconfig warning for legacy",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
---
apiVersion: v1
kind: label
spec:
  name: label1
  query: SELECT 1
---
apiVersion: v1
kind: config
spec:
  host_settings:
    enable_software_inventory: true
`,
			flags:      []string{"--dry-run"},
			wantErr:    `400 Bad request: warning: deprecated settings were used in the configuration: [host_settings]`,
			wantOutput: `[!] ignoring labels, dry run mode only supported for 'config' and 'team' spec`,
		},
		{
			desc: "dry-run set with various specs, no errors",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: teamNEW
---
apiVersion: v1
kind: label
spec:
  name: label1
  query: SELECT 1
---
apiVersion: v1
kind: config
spec:
  features:
    enable_software_inventory: true
`,
			flags: []string{"--dry-run"},
			wantOutput: `[!] ignoring labels, dry run mode only supported for 'config' and 'team' specs
[+] would've applied fleet config
[+] would've applied 1 teams`,
		},
		{
			desc: "macos_updates deadline set but minimum_version empty",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        deadline: 2022-01-04
`,
			wantErr: `422 Validation Failed: minimum_version is required when deadline is provided`,
		},
		{
			desc: "macos_updates minimum_version set but deadline empty",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        minimum_version: "12.2"
`,
			wantErr: `422 Validation Failed: deadline is required when minimum_version is provided`,
		},
		{
			desc: "macos_updates.minimum_version with build version",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        minimum_version: "12.2 (ABCD)"
        deadline: 1892-01-01
`,
			wantErr: `422 Validation Failed: minimum_version accepts version numbers only. (E.g., "13.0.1.") NOT "Ventura 13" or "13.0.1 (22A400)"`,
		},
		{
			desc: "macos_updates.deadline with timestamp",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        minimum_version: "12.2"
        deadline: "1892-01-01T00:00:00Z"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "macos_updates.deadline with invalid date",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        minimum_version: "12.2"
        deadline: "18-01-01"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "macos_updates.deadline with incomplete date",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_updates:
        minimum_version: "12.2"
        deadline: "2022-01"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "missing required sso entity_id",
			spec: `
apiVersion: v1
kind: config
spec:
  sso_settings:
    enable_sso: true
    entity_id: ""
    issuer_uri: "http://localhost:8080/simplesaml/saml2/idp/SSOService.php"
    idp_name: "SimpleSAML"
    metadata_url: "http://localhost:9080/simplesaml/saml2/idp/metadata.php"
`,
			wantErr: `422 Validation Failed: required`,
		},
		{
			desc: "missing required sso idp_name",
			spec: `
apiVersion: v1
kind: config
spec:
  sso_settings:
    enable_sso: true
    entity_id: "https://localhost:8080"
    issuer_uri: "http://localhost:8080/simplesaml/saml2/idp/SSOService.php"
    idp_name: ""
    metadata_url: "http://localhost:9080/simplesaml/saml2/idp/metadata.php"
`,
			wantErr: `422 Validation Failed: required`,
		},
		{
			desc: "missing required failing policies destination_url",
			spec: `
apiVersion: v1
kind: config
spec:
  webhook_settings:
    failing_policies_webhook:
      enable_failing_policies_webhook: true
      destination_url: ""
      policy_ids:
        - 1
      host_batch_size: 1000
    interval: 1h
`,
			wantErr: `422 Validation Failed: destination_url is required to enable the failing policies webhook`,
		},
		{
			desc: "missing required vulnerabilities destination_url",
			spec: `
apiVersion: v1
kind: config
spec:
  webhook_settings:
    vulnerabilities_webhook:
      enable_vulnerabilities_webhook: true
      destination_url: ""
      host_batch_size: 1000
    interval: 1h
`,
			wantErr: `422 Validation Failed: destination_url is required to enable the vulnerabilities webhook`,
		},
		{
			desc: "missing required host status destination_url",
			spec: `
apiVersion: v1
kind: config
spec:
  webhook_settings:
    host_status_webhook:
      enable_host_status_webhook: true
      destination_url: ""
      days_count: 10
      host_percentage: 10
    interval: 1h
`,
			wantErr: `422 Validation Failed: destination_url is required to enable the host status webhook`,
		},
		{
			desc: "missing required host status days_count",
			spec: `
apiVersion: v1
kind: config
spec:
  webhook_settings:
    host_status_webhook:
      enable_host_status_webhook: true
      destination_url: "http://some/url"
      days_count: 0
      host_percentage: 10
    interval: 1h
`,
			wantErr: `422 Validation Failed: days_count must be > 0 to enable the host status webhook`,
		},
		{
			desc: "missing required host status host_percentage",
			spec: `
apiVersion: v1
kind: config
spec:
  webhook_settings:
    host_status_webhook:
      enable_host_status_webhook: true
      destination_url: "http://some/url"
      days_count: 10
      host_percentage: -1
    interval: 1h
`,
			wantErr: `422 Validation Failed: host_percentage must be > 0 to enable the host status webhook`,
		},
		{
			desc: "config with FIM values for agent options (#8699)",
			spec: `
apiVersion: v1
kind: config
spec:
  agent_options:
    config:
      file_paths:
        ssh:
          - /home/%/.ssh/authorized_keys
      exclude_paths:
        ssh:
          - /home/ubuntu/.ssh/authorized_keys
`,
			wantOutput: `[+] applied fleet config`,
		},
		{
			desc: "app config macos_updates deadline set but minimum_version empty",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      deadline: 2022-01-04
`,
			wantErr: `422 Validation Failed: minimum_version is required when deadline is provided`,
		},
		{
			desc: "app config macos_updates minimum_version set but deadline empty",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      minimum_version: "12.2"
`,
			wantErr: `422 Validation Failed: deadline is required when minimum_version is provided`,
		},
		{
			desc: "app config macos_updates.minimum_version with build version",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      minimum_version: "12.2 (ABCD)"
      deadline: 1892-01-01
`,
			wantErr: `422 Validation Failed: minimum_version accepts version numbers only. (E.g., "13.0.1.") NOT "Ventura 13" or "13.0.1 (22A400)"`,
		},
		{
			desc: "app config macos_updates.deadline with timestamp",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      minimum_version: "12.2"
      deadline: "1892-01-01T00:00:00Z"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "app config macos_updates.deadline with invalid date",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      minimum_version: "12.2"
      deadline: "18-01-01"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "app config macos_updates.deadline with incomplete date",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_updates:
      minimum_version: "12.2"
      deadline: "2022-01"
`,
			wantErr: `422 Validation Failed: deadline accepts YYYY-MM-DD format only (E.g., "2023-06-01.")`,
		},
		{
			desc: "app config macos_settings.enable_disk_encryption without a value",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_settings:
      enable_disk_encryption:
`,
			wantOutput: `[+] applied fleet config`,
		},
		{
			desc: "app config macos_settings.enable_disk_encryption with invalid value type",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_settings:
      enable_disk_encryption: 123
`,
			wantErr: `400 Bad request: failed to decode app config`,
		},
		{
			desc: "app config macos_settings.enable_disk_encryption true",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_settings:
      enable_disk_encryption: true
`,
			wantErr: `Couldn't update macos_settings because MDM features aren't turned on in Fleet.`,
		},
		{
			desc: "app config macos_settings.enable_disk_encryption false",
			spec: `
apiVersion: v1
kind: config
spec:
  mdm:
    macos_settings:
      enable_disk_encryption: false
`,
			wantOutput: `[+] applied fleet config`,
		},
		{
			desc: "team config macos_settings.enable_disk_encryption without a value",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_settings:
        enable_disk_encryption:
`,
			wantErr: `400 Bad Request: invalid value type at 'macos_settings.enable_disk_encryption': expected bool but got <nil>`,
		},
		{
			desc: "team config macos_settings.enable_disk_encryption with invalid value type",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_settings:
        enable_disk_encryption: 123
`,
			wantErr: `400 Bad Request: invalid value type at 'macos_settings.enable_disk_encryption': expected bool but got float64`,
		},
		{
			desc: "team config macos_settings.enable_disk_encryption true",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_settings:
        enable_disk_encryption: true
`,
			wantErr: `Couldn't update macos_settings because MDM features aren't turned on in Fleet.`,
		},
		{
			desc: "team config macos_settings.enable_disk_encryption false",
			spec: `
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_settings:
        enable_disk_encryption: false
`,
			wantOutput: `[+] applied 1 teams`,
		},
		{
			desc: "team config mac setup assistant",
			spec: fmt.Sprintf(`
apiVersion: v1
kind: team
spec:
  team:
    name: team1
    mdm:
      macos_setup:
        macos_setup_assistant: %s
`, macSetupFile),
			wantErr: `MDM features aren't turned on.`,
		},
		{
			desc: "app config macos setup assistant",
			spec: fmt.Sprintf(`
apiVersion: v1
kind: config
spec:
  mdm:
    macos_setup:
      macos_setup_assistant: %s
`, macSetupFile),
			wantErr: `MDM features aren't turned on.`,
		},
	}
	// NOTE: Integrations required fields are not tested (Jira/Zendesk) because
	// they require a complex setup to mock the client that would communicate
	// with the external API. However, we make a test API call when enabling an
	// integration, ensuring that any missing configuration field results in an
	// error. Same for smtp_settings (a test email is sent when enabling).

	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			_, ds := runServerWithMockedDS(t, &service.TestServerOpts{License: license})
			setupDS(ds)
			filename := writeTmpYml(t, c.spec)

			var got string
			if c.wantErr == "" {
				got = runAppForTest(t, append([]string{"apply", "-f", filename}, c.flags...))
			} else {
				buf, err := runAppNoChecks(append([]string{"apply", "-f", filename}, c.flags...))
				require.ErrorContains(t, err, c.wantErr)
				got = buf.String()
			}
			if c.wantOutput == "" {
				require.Empty(t, got)
			} else {
				require.Contains(t, got, c.wantOutput)
			}
		})
	}
}
