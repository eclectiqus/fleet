package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/WatchBeam/clock"
	eeservice "github.com/fleetdm/fleet/v4/ee/server/service"
	"github.com/fleetdm/fleet/v4/server/config"
	"github.com/fleetdm/fleet/v4/server/contexts/license"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/logging"
	"github.com/fleetdm/fleet/v4/server/mail"
	apple_mdm "github.com/fleetdm/fleet/v4/server/mdm/apple"
	nanodep_mock "github.com/fleetdm/fleet/v4/server/mock/nanodep"
	"github.com/fleetdm/fleet/v4/server/ptr"
	"github.com/fleetdm/fleet/v4/server/service/async"
	"github.com/fleetdm/fleet/v4/server/service/mock"
	"github.com/fleetdm/fleet/v4/server/sso"
	"github.com/fleetdm/fleet/v4/server/test"
	kitlog "github.com/go-kit/kit/log"
	"github.com/google/uuid"
	nanodep_storage "github.com/micromdm/nanodep/storage"
	"github.com/micromdm/nanomdm/mdm"
	"github.com/micromdm/nanomdm/push"
	nanomdm_push "github.com/micromdm/nanomdm/push"
	nanomdm_storage "github.com/micromdm/nanomdm/storage"
	scep_depot "github.com/micromdm/scep/v2/depot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/throttled/throttled/v2"
	"github.com/throttled/throttled/v2/store/memstore"
)

func newTestService(t *testing.T, ds fleet.Datastore, rs fleet.QueryResultStore, lq fleet.LiveQueryStore, opts ...*TestServerOpts) (fleet.Service, context.Context) {
	return newTestServiceWithConfig(t, ds, config.TestConfig(), rs, lq, opts...)
}

func newTestServiceWithConfig(t *testing.T, ds fleet.Datastore, fleetConfig config.FleetConfig, rs fleet.QueryResultStore, lq fleet.LiveQueryStore, opts ...*TestServerOpts) (fleet.Service, context.Context) {
	lic := &fleet.LicenseInfo{Tier: fleet.TierFree}
	writer, err := logging.NewFilesystemLogWriter(fleetConfig.Filesystem.StatusLogFile, kitlog.NewNopLogger(), fleetConfig.Filesystem.EnableLogRotation, fleetConfig.Filesystem.EnableLogCompression, 500, 28, 3)
	require.NoError(t, err)

	osqlogger := &OsqueryLogger{Status: writer, Result: writer}
	logger := kitlog.NewNopLogger()

	var ssoStore sso.SessionStore

	var (
		failingPolicySet  fleet.FailingPolicySet  = NewMemFailingPolicySet()
		enrollHostLimiter fleet.EnrollHostLimiter = nopEnrollHostLimiter{}
		is                fleet.InstallerStore
		mdmStorage        nanomdm_storage.AllStorage
		depStorage        nanodep_storage.AllStorage = &nanodep_mock.Storage{}
		mdmPusher         nanomdm_push.Pusher
		mailer            fleet.MailService = &mockMailService{SendEmailFn: func(e fleet.Email) error { return nil }}
	)
	var c clock.Clock = clock.C
	if len(opts) > 0 {
		if opts[0].Clock != nil {
			c = opts[0].Clock
		}
	}

	task := async.NewTask(ds, nil, c, config.OsqueryConfig{})
	if len(opts) > 0 {
		if opts[0].Task != nil {
			task = opts[0].Task
		} else {
			opts[0].Task = task
		}
	}

	if len(opts) > 0 {
		if opts[0].Logger != nil {
			logger = opts[0].Logger
		}
		if opts[0].License != nil {
			lic = opts[0].License
		}
		if opts[0].Pool != nil {
			ssoStore = sso.NewSessionStore(opts[0].Pool)
		}
		if opts[0].FailingPolicySet != nil {
			failingPolicySet = opts[0].FailingPolicySet
		}
		if opts[0].EnrollHostLimiter != nil {
			enrollHostLimiter = opts[0].EnrollHostLimiter
		}
		if opts[0].UseMailService {
			mailer, err = mail.NewService(config.TestConfig())
			require.NoError(t, err)
		}

		// allow to explicitly set installer store to nil
		is = opts[0].Is
		// allow to explicitly set MDM storage to nil
		mdmStorage = opts[0].MDMStorage
		if opts[0].DEPStorage != nil {
			depStorage = opts[0].DEPStorage
		}
		// allow to explicitly set mdm pusher to nil
		mdmPusher = opts[0].MDMPusher
	}

	ctx := license.NewContext(context.Background(), lic)

	cronSchedulesService := fleet.NewCronSchedules()

	if len(opts) > 0 && opts[0].StartCronSchedules != nil {
		for _, fn := range opts[0].StartCronSchedules {
			err = cronSchedulesService.StartCronSchedule(fn(ctx, ds))
			require.NoError(t, err)
		}
	}

	svc, err := NewService(
		ctx,
		ds,
		task,
		rs,
		logger,
		osqlogger,
		fleetConfig,
		mailer,
		c,
		ssoStore,
		lq,
		ds,
		is,
		failingPolicySet,
		&fleet.NoOpGeoIP{},
		enrollHostLimiter,
		depStorage,
		mdmStorage,
		mdmPusher,
		"",
		cronSchedulesService,
	)
	if err != nil {
		panic(err)
	}
	if lic.IsPremium() {
		svc, err = eeservice.NewService(
			svc,
			ds,
			kitlog.NewNopLogger(),
			fleetConfig,
			mailer,
			c,
			depStorage,
			apple_mdm.NewMDMAppleCommander(mdmStorage, mdmPusher),
			"",
			ssoStore,
		)
		if err != nil {
			panic(err)
		}
	}
	return svc, ctx
}

func newTestServiceWithClock(t *testing.T, ds fleet.Datastore, rs fleet.QueryResultStore, lq fleet.LiveQueryStore, c clock.Clock) (fleet.Service, context.Context) {
	testConfig := config.TestConfig()
	return newTestServiceWithConfig(t, ds, testConfig, rs, lq, &TestServerOpts{
		Clock: c,
	})
}

func createTestUsers(t *testing.T, ds fleet.Datastore) map[string]fleet.User {
	users := make(map[string]fleet.User)
	userID := uint(1)
	for _, u := range testUsers {
		role := fleet.RoleObserver
		if strings.Contains(u.Email, "admin") {
			role = fleet.RoleAdmin
		}
		user := &fleet.User{
			ID:         userID,
			Name:       "Test Name " + u.Email,
			Email:      u.Email,
			GlobalRole: &role,
		}
		err := user.SetPassword(u.PlaintextPassword, 10, 10)
		require.Nil(t, err)
		user, err = ds.NewUser(context.Background(), user)
		require.Nil(t, err)
		users[user.Email] = *user
		userID++
	}
	return users
}

var testUsers = map[string]struct {
	Email             string
	PlaintextPassword string
	GlobalRole        *string
}{
	"admin1": {
		PlaintextPassword: test.GoodPassword,
		Email:             "admin1@example.com",
		GlobalRole:        ptr.String(fleet.RoleAdmin),
	},
	"user1": {
		PlaintextPassword: test.GoodPassword,
		Email:             "user1@example.com",
		GlobalRole:        ptr.String(fleet.RoleMaintainer),
	},
	"user2": {
		PlaintextPassword: test.GoodPassword,
		Email:             "user2@example.com",
		GlobalRole:        ptr.String(fleet.RoleObserver),
	},
}

func createEnrollSecrets(t *testing.T, count int) []*fleet.EnrollSecret {
	secrets := make([]*fleet.EnrollSecret, count)
	for i := 0; i < count; i++ {
		secrets[i] = &fleet.EnrollSecret{Secret: fmt.Sprintf("testSecret%d", i)}
	}
	return secrets
}

type mockMailService struct {
	SendEmailFn func(e fleet.Email) error
	Invoked     bool
}

func (svc *mockMailService) SendEmail(e fleet.Email) error {
	svc.Invoked = true
	return svc.SendEmailFn(e)
}

type TestNewScheduleFunc func(ctx context.Context, ds fleet.Datastore) fleet.NewCronScheduleFunc

type TestServerOpts struct {
	Logger              kitlog.Logger
	License             *fleet.LicenseInfo
	SkipCreateTestUsers bool
	Rs                  fleet.QueryResultStore
	Lq                  fleet.LiveQueryStore
	Pool                fleet.RedisPool
	FailingPolicySet    fleet.FailingPolicySet
	Clock               clock.Clock
	Task                *async.Task
	EnrollHostLimiter   fleet.EnrollHostLimiter
	Is                  fleet.InstallerStore
	FleetConfig         *config.FleetConfig
	MDMStorage          nanomdm_storage.AllStorage
	DEPStorage          nanodep_storage.AllStorage
	SCEPStorage         scep_depot.Depot
	MDMPusher           nanomdm_push.Pusher
	HTTPServerConfig    *http.Server
	StartCronSchedules  []TestNewScheduleFunc
	UseMailService      bool
}

func RunServerForTestsWithDS(t *testing.T, ds fleet.Datastore, opts ...*TestServerOpts) (map[string]fleet.User, *httptest.Server) {
	var rs fleet.QueryResultStore
	if len(opts) > 0 && opts[0].Rs != nil {
		rs = opts[0].Rs
	}
	var lq fleet.LiveQueryStore
	if len(opts) > 0 && opts[0].Lq != nil {
		lq = opts[0].Lq
	}
	cfg := config.TestConfig()
	if len(opts) > 0 && opts[0].FleetConfig != nil {
		cfg = *opts[0].FleetConfig
	}
	svc, ctx := newTestServiceWithConfig(t, ds, cfg, rs, lq, opts...)
	users := map[string]fleet.User{}
	if len(opts) == 0 || (len(opts) > 0 && !opts[0].SkipCreateTestUsers) {
		users = createTestUsers(t, ds)
	}
	logger := kitlog.NewLogfmtLogger(os.Stdout)
	if len(opts) > 0 && opts[0].Logger != nil {
		logger = opts[0].Logger
	}
	var mdmPusher nanomdm_push.Pusher
	if len(opts) > 0 && opts[0].MDMPusher != nil {
		mdmPusher = opts[0].MDMPusher
	}
	limitStore, _ := memstore.New(0)
	rootMux := http.NewServeMux()

	if len(opts) > 0 {
		mdmStorage := opts[0].MDMStorage
		scepStorage := opts[0].SCEPStorage
		if mdmStorage != nil && scepStorage != nil {
			err := RegisterAppleMDMProtocolServices(
				rootMux,
				cfg.MDM,
				mdmStorage,
				scepStorage,
				logger,
				&MDMAppleCheckinAndCommandService{
					ds:        ds,
					commander: apple_mdm.NewMDMAppleCommander(mdmStorage, mdmPusher),
					logger:    kitlog.NewNopLogger(),
				},
			)
			require.NoError(t, err)
		}
	}

	apiHandler := MakeHandler(svc, cfg, logger, limitStore, WithLoginRateLimit(throttled.PerMin(100)))
	rootMux.Handle("/api/", apiHandler)

	server := httptest.NewUnstartedServer(rootMux)
	server.Config = cfg.Server.DefaultHTTPServer(ctx, rootMux)
	if len(opts) > 0 && opts[0].HTTPServerConfig != nil {
		server.Config = opts[0].HTTPServerConfig
		// make sure we use the application handler we just created
		server.Config.Handler = rootMux
	}
	server.Start()
	t.Cleanup(func() {
		server.Close()
	})
	return users, server
}

func testSESPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Email = config.EmailConfig{EmailBackend: "ses"}
	c.SES = config.SESConfig{
		Region:           "us-east-1",
		AccessKeyID:      "foo",
		SecretAccessKey:  "bar",
		StsAssumeRoleArn: "baz",
		SourceArn:        "qux",
	}
	return c
}

func testKinesisPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery.ResultLogPlugin = "kinesis"
	c.Osquery.StatusLogPlugin = "kinesis"
	c.Activity.AuditLogPlugin = "kinesis"
	c.Kinesis = config.KinesisConfig{
		Region:           "us-east-1",
		AccessKeyID:      "foo",
		SecretAccessKey:  "bar",
		StsAssumeRoleArn: "baz",
		StatusStream:     "test-status-stream",
		ResultStream:     "test-result-stream",
		AuditStream:      "test-audit-stream",
	}
	return c
}

func testFirehosePluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery.ResultLogPlugin = "firehose"
	c.Osquery.StatusLogPlugin = "firehose"
	c.Activity.AuditLogPlugin = "firehose"
	c.Firehose = config.FirehoseConfig{
		Region:           "us-east-1",
		AccessKeyID:      "foo",
		SecretAccessKey:  "bar",
		StsAssumeRoleArn: "baz",
		StatusStream:     "test-status-firehose",
		ResultStream:     "test-result-firehose",
		AuditStream:      "test-audit-firehose",
	}
	return c
}

func testLambdaPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery.ResultLogPlugin = "lambda"
	c.Osquery.StatusLogPlugin = "lambda"
	c.Activity.AuditLogPlugin = "lambda"
	c.Lambda = config.LambdaConfig{
		Region:           "us-east-1",
		AccessKeyID:      "foo",
		SecretAccessKey:  "bar",
		StsAssumeRoleArn: "baz",
		ResultFunction:   "result-func",
		StatusFunction:   "status-func",
		AuditFunction:    "audit-func",
	}
	return c
}

func testPubSubPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery.ResultLogPlugin = "pubsub"
	c.Osquery.StatusLogPlugin = "pubsub"
	c.Activity.AuditLogPlugin = "pubsub"
	c.PubSub = config.PubSubConfig{
		Project:       "test",
		StatusTopic:   "status-topic",
		ResultTopic:   "result-topic",
		AuditTopic:    "audit-topic",
		AddAttributes: false,
	}
	return c
}

func testStdoutPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery.ResultLogPlugin = "stdout"
	c.Osquery.StatusLogPlugin = "stdout"
	c.Activity.AuditLogPlugin = "stdout"
	return c
}

func testUnrecognizedPluginConfig() config.FleetConfig {
	c := config.TestConfig()
	c.Osquery = config.OsqueryConfig{
		ResultLogPlugin: "bar",
		StatusLogPlugin: "bar",
	}
	c.Activity.AuditLogPlugin = "bar"
	return c
}

func assertBodyContains(t *testing.T, resp *http.Response, expected string) {
	bodyBytes, err := io.ReadAll(resp.Body)
	require.Nil(t, err)
	bodyString := string(bodyBytes)
	assert.Contains(t, bodyString, expected)
}

func getJSON(r *http.Response, target interface{}) error {
	return json.NewDecoder(r.Body).Decode(target)
}

func assertErrorCodeAndMessage(t *testing.T, resp *http.Response, code int, message string) {
	err := &fleet.Error{}
	require.Nil(t, getJSON(resp, err))
	assert.Equal(t, code, err.Code)
	assert.Equal(t, message, err.Message)
}

type memFailingPolicySet struct {
	mMu sync.RWMutex
	m   map[uint][]fleet.PolicySetHost
}

var _ fleet.FailingPolicySet = (*memFailingPolicySet)(nil)

func NewMemFailingPolicySet() *memFailingPolicySet {
	return &memFailingPolicySet{
		m: make(map[uint][]fleet.PolicySetHost),
	}
}

// AddFailingPoliciesForHost adds the given host to the policy sets.
func (m *memFailingPolicySet) AddHost(policyID uint, host fleet.PolicySetHost) error {
	m.mMu.Lock()
	defer m.mMu.Unlock()

	m.m[policyID] = append(m.m[policyID], host)
	return nil
}

// ListHosts returns the list of hosts present in the policy set.
func (m *memFailingPolicySet) ListHosts(policyID uint) ([]fleet.PolicySetHost, error) {
	m.mMu.RLock()
	defer m.mMu.RUnlock()

	hosts := make([]fleet.PolicySetHost, len(m.m[policyID]))
	for i := range m.m[policyID] {
		hosts[i] = m.m[policyID][i]
	}
	return hosts, nil
}

// RemoveHosts removes the hosts from the policy set.
func (m *memFailingPolicySet) RemoveHosts(policyID uint, hosts []fleet.PolicySetHost) error {
	m.mMu.Lock()
	defer m.mMu.Unlock()

	if _, ok := m.m[policyID]; !ok {
		return nil
	}
	hostsSet := make(map[uint]struct{})
	for _, host := range hosts {
		hostsSet[host.ID] = struct{}{}
	}
	n := 0
	for _, host := range m.m[policyID] {
		if _, ok := hostsSet[host.ID]; !ok {
			m.m[policyID][n] = host
			n++
		}
	}
	m.m[policyID] = m.m[policyID][:n]
	return nil
}

// RemoveSet removes a policy set.
func (m *memFailingPolicySet) RemoveSet(policyID uint) error {
	m.mMu.Lock()
	defer m.mMu.Unlock()

	delete(m.m, policyID)
	return nil
}

// ListSets lists all the policy sets.
func (m *memFailingPolicySet) ListSets() ([]uint, error) {
	m.mMu.RLock()
	defer m.mMu.RUnlock()

	var policyIDs []uint
	for policyID := range m.m {
		policyIDs = append(policyIDs, policyID)
	}
	return policyIDs, nil
}

type nopEnrollHostLimiter struct{}

func (nopEnrollHostLimiter) CanEnrollNewHost(ctx context.Context) (bool, error) {
	return true, nil
}

func (nopEnrollHostLimiter) SyncEnrolledHostIDs(ctx context.Context) error {
	return nil
}

func newMockAPNSPushProviderFactory() (*mock.APNSPushProviderFactory, *mock.APNSPushProvider) {
	provider := &mock.APNSPushProvider{}
	provider.PushFunc = mockSuccessfulPush
	factory := &mock.APNSPushProviderFactory{}
	factory.NewPushProviderFunc = func(*tls.Certificate) (push.PushProvider, error) {
		return provider, nil
	}

	return factory, provider
}

func mockSuccessfulPush(pushes []*mdm.Push) (map[string]*push.Response, error) {
	res := make(map[string]*push.Response, len(pushes))
	for _, p := range pushes {
		res[p.Token.String()] = &push.Response{
			Id:  uuid.New().String(),
			Err: nil,
		}
	}
	return res, nil
}

func mdmAppleConfigurationRequiredEndpoints() [][2]string {
	return [][2]string{
		{"POST", "/api/latest/fleet/mdm/apple/enrollmentprofiles"},
		{"GET", "/api/latest/fleet/mdm/apple/enrollmentprofiles"},
		{"POST", "/api/latest/fleet/mdm/apple/enqueue"},
		{"GET", "/api/latest/fleet/mdm/apple/commandresults"},
		{"GET", "/api/latest/fleet/mdm/apple/installers/1"},
		{"DELETE", "/api/latest/fleet/mdm/apple/installers/1"},
		{"GET", "/api/latest/fleet/mdm/apple/installers"},
		{"GET", "/api/latest/fleet/mdm/apple/devices"},
		{"GET", "/api/latest/fleet/mdm/apple/dep/devices"},
		{"GET", "/api/latest/fleet/mdm/apple/profiles"},
		{"GET", "/api/latest/fleet/mdm/apple/profiles/1"},
		{"DELETE", "/api/latest/fleet/mdm/apple/profiles/1"},
		{"GET", "/api/latest/fleet/mdm/apple/profiles/summary"},
		{"PATCH", "/api/latest/fleet/mdm/hosts/1/unenroll"},
		{"GET", "/api/latest/fleet/mdm/hosts/1/encryption_key"},
		{"POST", "/api/latest/fleet/mdm/hosts/1/lock"},
		{"POST", "/api/latest/fleet/mdm/hosts/1/wipe"},
		{"PATCH", "/api/latest/fleet/mdm/apple/settings"},
		{"GET", "/api/latest/fleet/mdm/apple"},
		{"GET", apple_mdm.EnrollPath + "?token=test"},
		{"GET", apple_mdm.InstallerPath + "?token=test"},
		{"GET", "/api/latest/fleet/mdm/apple/setup/eula/token"},
		{"DELETE", "/api/latest/fleet/mdm/apple/setup/eula/token"},
		{"GET", "/api/latest/fleet/mdm/apple/setup/eula/metadata"},
		// TODO: this endpoint accepts multipart/form data that gets
		// parsed before the MDM check, we need to refactor this
		// function to return more information to the caller, or find a
		// better way to test these endpoints.
		// {"POST", "/api/latest/fleet/mdm/apple/setup/eula"},
		{"GET", "/api/latest/fleet/mdm/apple/enrollment_profile"},
		{"POST", "/api/latest/fleet/mdm/apple/enrollment_profile"},
		{"DELETE", "/api/latest/fleet/mdm/apple/enrollment_profile"},
	}
}
