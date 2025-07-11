package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fleetdm/fleet/v4/server/contexts/ctxerr"
	"github.com/fleetdm/fleet/v4/server/fleet"
	apple_mdm "github.com/fleetdm/fleet/v4/server/mdm/apple"
	"github.com/fleetdm/fleet/v4/server/mdm/apple/mobileconfig"
	"github.com/fleetdm/fleet/v4/server/ptr"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/jmoiron/sqlx"
	"github.com/micromdm/nanodep/godep"
	"github.com/micromdm/nanomdm/mdm"
)

func (ds *Datastore) NewMDMAppleConfigProfile(ctx context.Context, cp fleet.MDMAppleConfigProfile) (*fleet.MDMAppleConfigProfile, error) {
	stmt := `
INSERT INTO
    mdm_apple_configuration_profiles (team_id, identifier, name, mobileconfig, checksum)
VALUES (?, ?, ?, ?, UNHEX(MD5(mobileconfig)))`

	var teamID uint
	if cp.TeamID != nil {
		teamID = *cp.TeamID
	}

	res, err := ds.writer.ExecContext(ctx, stmt, teamID, cp.Identifier, cp.Name, cp.Mobileconfig)
	if err != nil {
		switch {
		case isDuplicate(err):
			return nil, ctxerr.Wrap(ctx, formatErrorDuplicateConfigProfile(err, &cp))
		default:
			return nil, ctxerr.Wrap(ctx, err, "creating new mdm config profile")
		}
	}

	id, _ := res.LastInsertId()

	return &fleet.MDMAppleConfigProfile{
		ProfileID:    uint(id),
		Identifier:   cp.Identifier,
		Name:         cp.Name,
		Mobileconfig: cp.Mobileconfig,
		TeamID:       cp.TeamID,
	}, nil
}

func formatErrorDuplicateConfigProfile(err error, cp *fleet.MDMAppleConfigProfile) error {
	switch {
	case strings.Contains(err.Error(), "idx_mdm_apple_config_prof_team_identifier"):
		return &existsError{
			ResourceType: "MDMAppleConfigProfile.PayloadIdentifier",
			Identifier:   cp.Identifier,
			TeamID:       cp.TeamID,
		}
	case strings.Contains(err.Error(), "idx_mdm_apple_config_prof_team_name"):
		return &existsError{
			ResourceType: "MDMAppleConfigProfile.PayloadDisplayName",
			Identifier:   cp.Name,
			TeamID:       cp.TeamID,
		}
	default:
		return err
	}
}

func (ds *Datastore) ListMDMAppleConfigProfiles(ctx context.Context, teamID *uint) ([]*fleet.MDMAppleConfigProfile, error) {
	stmt := `
SELECT
	profile_id,
	team_id,
	name,
	identifier,
	mobileconfig,
	created_at,
	updated_at
FROM
	mdm_apple_configuration_profiles
WHERE
	team_id=? AND identifier NOT IN (?)
ORDER BY name`

	if teamID == nil {
		teamID = ptr.Uint(0)
	}

	fleetIdentifiers := []string{}
	for idf := range mobileconfig.FleetPayloadIdentifiers() {
		fleetIdentifiers = append(fleetIdentifiers, idf)
	}
	stmt, args, err := sqlx.In(stmt, teamID, fleetIdentifiers)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "sqlx.In ListMDMAppleConfigProfiles")
	}

	var res []*fleet.MDMAppleConfigProfile
	if err = sqlx.SelectContext(ctx, ds.reader, &res, stmt, args...); err != nil {
		return nil, err
	}
	return res, nil
}

func (ds *Datastore) GetMDMAppleConfigProfile(ctx context.Context, profileID uint) (*fleet.MDMAppleConfigProfile, error) {
	stmt := `
SELECT
	profile_id,
	team_id,
	name,
	identifier,
	mobileconfig,
	created_at,
	updated_at
FROM
	mdm_apple_configuration_profiles
WHERE
	profile_id=?`

	var res fleet.MDMAppleConfigProfile
	err := sqlx.GetContext(ctx, ds.reader, &res, stmt, profileID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("MDMAppleConfigProfile").WithID(profileID))
		}
		return nil, ctxerr.Wrap(ctx, err, "get mdm apple config profile")
	}

	return &res, nil
}

func (ds *Datastore) DeleteMDMAppleConfigProfile(ctx context.Context, profileID uint) error {
	res, err := ds.writer.ExecContext(ctx, `DELETE FROM mdm_apple_configuration_profiles WHERE profile_id=?`, profileID)
	if err != nil {
		return ctxerr.Wrap(ctx, err)
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return ctxerr.Wrap(ctx, err, "fetching delete mdm config profile query rows affected")
	}
	if deleted != 1 {
		return ctxerr.Wrap(ctx, notFound("MDMAppleConfigProfile").WithID(profileID))
	}

	return nil
}

func (ds *Datastore) DeleteMDMAppleConfigProfileByTeamAndIdentifier(ctx context.Context, teamID *uint, profileIdentifier string) error {
	if teamID == nil {
		teamID = ptr.Uint(0)
	}

	res, err := ds.writer.ExecContext(ctx, `DELETE FROM mdm_apple_configuration_profiles WHERE team_id = ? AND identifier = ?`, teamID, profileIdentifier)
	if err != nil {
		return ctxerr.Wrap(ctx, err)
	}

	if deleted, _ := res.RowsAffected(); deleted == 0 {
		message := fmt.Sprintf("identifier: %s, team_id: %d", profileIdentifier, teamID)
		return ctxerr.Wrap(ctx, notFound("MDMAppleConfigProfile").WithMessage(message))
	}

	return nil
}

func (ds *Datastore) GetHostMDMProfiles(ctx context.Context, hostUUID string) ([]fleet.HostMDMAppleProfile, error) {
	stmt := fmt.Sprintf(`
SELECT
	profile_id,
	profile_name AS name,
	profile_identifier AS identifier,
	-- internally, a NULL status implies that the cron needs to pick up
	-- this profile, for the user that difference doesn't exist, the
	-- profile is effectively pending. This is consistent with all our
	-- aggregation functions.
	COALESCE(status, '%s') AS status,
	COALESCE(operation_type, '') AS operation_type,
	COALESCE(detail, '') AS detail
FROM
	host_mdm_apple_profiles
WHERE
	host_uuid = ? AND NOT (operation_type = '%s' AND COALESCE(status, '%s') = '%s')`,
		fleet.MDMAppleDeliveryPending,
		fleet.MDMAppleOperationTypeRemove,
		fleet.MDMAppleDeliveryPending,
		fleet.MDMAppleDeliveryVerifying,
	)

	var profiles []fleet.HostMDMAppleProfile
	if err := sqlx.SelectContext(ctx, ds.reader, &profiles, stmt, hostUUID); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (ds *Datastore) NewMDMAppleEnrollmentProfile(
	ctx context.Context,
	payload fleet.MDMAppleEnrollmentProfilePayload,
) (*fleet.MDMAppleEnrollmentProfile, error) {
	res, err := ds.writer.ExecContext(ctx,
		`
INSERT INTO
    mdm_apple_enrollment_profiles (token, type, dep_profile)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE
    token = VALUES(token),
    type = VALUES(type),
    dep_profile = VALUES(dep_profile)
`,
		payload.Token, payload.Type, payload.DEPProfile,
	)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err)
	}
	id, _ := res.LastInsertId()
	return &fleet.MDMAppleEnrollmentProfile{
		ID:         uint(id),
		Token:      payload.Token,
		Type:       payload.Type,
		DEPProfile: payload.DEPProfile,
	}, nil
}

func (ds *Datastore) ListMDMAppleEnrollmentProfiles(ctx context.Context) ([]*fleet.MDMAppleEnrollmentProfile, error) {
	var enrollmentProfiles []*fleet.MDMAppleEnrollmentProfile
	if err := sqlx.SelectContext(
		ctx,
		ds.writer,
		&enrollmentProfiles,
		`
SELECT
    id,
    token,
    type,
    dep_profile,
    created_at,
    updated_at
FROM
    mdm_apple_enrollment_profiles
ORDER BY created_at DESC
`,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "list enrollment profiles")
	}
	return enrollmentProfiles, nil
}

func (ds *Datastore) GetMDMAppleEnrollmentProfileByToken(ctx context.Context, token string) (*fleet.MDMAppleEnrollmentProfile, error) {
	var enrollment fleet.MDMAppleEnrollmentProfile
	if err := sqlx.GetContext(ctx, ds.writer,
		&enrollment,
		`
SELECT
    id,
    token,
    type,
    dep_profile,
    created_at,
    updated_at
FROM
    mdm_apple_enrollment_profiles
WHERE
    token = ?
`,
		token,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("MDMAppleEnrollmentProfile"))
		}
		return nil, ctxerr.Wrap(ctx, err, "get enrollment profile by token")
	}
	return &enrollment, nil
}

func (ds *Datastore) GetMDMAppleCommandRequestType(ctx context.Context, commandUUID string) (string, error) {
	var rt string
	err := sqlx.GetContext(ctx, ds.reader, &rt, `SELECT request_type FROM nano_commands WHERE command_uuid = ?`, commandUUID)
	if err == sql.ErrNoRows {
		return "", ctxerr.Wrap(ctx, notFound("MDMAppleCommand").WithName(commandUUID))
	}
	return rt, err
}

func (ds *Datastore) GetMDMAppleCommandResults(ctx context.Context, commandUUID string) ([]*fleet.MDMAppleCommandResult, error) {
	query := `
SELECT
    ncr.id as device_id,
    ncr.command_uuid,
    ncr.status,
    ncr.result,
		ncr.updated_at,
		nc.request_type
FROM
    nano_command_results ncr
INNER JOIN
    nano_commands nc
ON
    ncr.command_uuid = nc.command_uuid
WHERE
    ncr.command_uuid = ?
`

	var results []*fleet.MDMAppleCommandResult
	err := sqlx.SelectContext(
		ctx,
		ds.reader,
		&results,
		query,
		commandUUID,
	)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "get command results")
	}
	return results, nil
}

func (ds *Datastore) ListMDMAppleCommands(
	ctx context.Context,
	tmFilter fleet.TeamFilter,
	listOpts *fleet.MDMAppleCommandListOptions,
) ([]*fleet.MDMAppleCommand, error) {
	stmt := fmt.Sprintf(`
SELECT
    nvq.id as device_id,
    nvq.command_uuid,
    COALESCE(NULLIF(nvq.status, ''), 'Pending') as status,
    COALESCE(nvq.result_updated_at, nvq.created_at) as updated_at,
    nvq.request_type,
    h.hostname,
    h.team_id
FROM
    nano_view_queue nvq
INNER JOIN
    hosts h
ON
    nvq.id = h.uuid
WHERE
    %s
`, ds.whereFilterHostsByTeams(tmFilter, "h"))
	stmt, params := appendListOptionsWithCursorToSQL(stmt, nil, &listOpts.ListOptions)

	var results []*fleet.MDMAppleCommand
	if err := sqlx.SelectContext(ctx, ds.reader, &results, stmt, params...); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "list commands")
	}
	return results, nil
}

func (ds *Datastore) NewMDMAppleInstaller(ctx context.Context, name string, size int64, manifest string, installer []byte, urlToken string) (*fleet.MDMAppleInstaller, error) {
	res, err := ds.writer.ExecContext(
		ctx,
		`INSERT INTO mdm_apple_installers (name, size, manifest, installer, url_token) VALUES (?, ?, ?, ?, ?)`,
		name, size, manifest, installer, urlToken,
	)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err)
	}
	id, _ := res.LastInsertId()
	return &fleet.MDMAppleInstaller{
		ID:        uint(id),
		Size:      size,
		Name:      name,
		Manifest:  manifest,
		Installer: installer,
		URLToken:  urlToken,
	}, nil
}

func (ds *Datastore) MDMAppleInstaller(ctx context.Context, token string) (*fleet.MDMAppleInstaller, error) {
	var installer fleet.MDMAppleInstaller
	if err := sqlx.GetContext(
		ctx,
		ds.writer,
		&installer,
		`SELECT id, name, size, manifest, installer, url_token FROM mdm_apple_installers WHERE url_token = ?`,
		token,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("AppleInstaller").WithName(token))
		}
		return nil, ctxerr.Wrap(ctx, err, "get installer by token")
	}
	return &installer, nil
}

func (ds *Datastore) MDMAppleInstallerDetailsByID(ctx context.Context, id uint) (*fleet.MDMAppleInstaller, error) {
	var installer fleet.MDMAppleInstaller
	if err := sqlx.GetContext(
		ctx,
		ds.writer,
		&installer,
		`SELECT id, name, size, manifest, url_token FROM mdm_apple_installers WHERE id = ?`,
		id,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("AppleInstaller").WithID(id))
		}
		return nil, ctxerr.Wrap(ctx, err, "get installer details by id")
	}
	return &installer, nil
}

func (ds *Datastore) DeleteMDMAppleInstaller(ctx context.Context, id uint) error {
	if _, err := ds.writer.ExecContext(ctx, `DELETE FROM mdm_apple_installers WHERE id = ?`, id); err != nil {
		return ctxerr.Wrap(ctx, err)
	}
	return nil
}

func (ds *Datastore) MDMAppleInstallerDetailsByToken(ctx context.Context, token string) (*fleet.MDMAppleInstaller, error) {
	var installer fleet.MDMAppleInstaller
	if err := sqlx.GetContext(
		ctx,
		ds.writer,
		&installer,
		`SELECT id, name, size, manifest, url_token FROM mdm_apple_installers WHERE url_token = ?`,
		token,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("AppleInstaller").WithName(token))
		}
		return nil, ctxerr.Wrap(ctx, err, "get installer details by id")
	}
	return &installer, nil
}

func (ds *Datastore) ListMDMAppleInstallers(ctx context.Context) ([]fleet.MDMAppleInstaller, error) {
	var installers []fleet.MDMAppleInstaller
	if err := sqlx.SelectContext(ctx, ds.writer,
		&installers,
		`SELECT id, name, size, manifest, url_token FROM mdm_apple_installers`,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "list installers")
	}
	return installers, nil
}

func (ds *Datastore) MDMAppleListDevices(ctx context.Context) ([]fleet.MDMAppleDevice, error) {
	var devices []fleet.MDMAppleDevice
	if err := sqlx.SelectContext(
		ctx,
		ds.writer,
		&devices,
		`
SELECT
    d.id,
    d.serial_number,
    e.enabled
FROM
    nano_devices d
    JOIN nano_enrollments e ON d.id = e.device_id
WHERE
    type = "Device"
`,
	); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "list devices")
	}
	return devices, nil
}

func (ds *Datastore) IngestMDMAppleDeviceFromCheckin(ctx context.Context, mdmHost fleet.MDMAppleHostDetails) error {
	appCfg, err := ds.AppConfig(ctx)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host get app config")
	}
	return ds.withRetryTxx(ctx, func(tx sqlx.ExtContext) error {
		return ingestMDMAppleDeviceFromCheckinDB(ctx, tx, mdmHost, ds.logger, appCfg)
	})
}

func ingestMDMAppleDeviceFromCheckinDB(
	ctx context.Context,
	tx sqlx.ExtContext,
	mdmHost fleet.MDMAppleHostDetails,
	logger log.Logger,
	appCfg *fleet.AppConfig,
) error {
	if mdmHost.SerialNumber == "" {
		return ctxerr.New(ctx, "ingest mdm apple host from checkin expected device serial number but got empty string")
	}
	if mdmHost.UDID == "" {
		return ctxerr.New(ctx, "ingest mdm apple host from checkin expected unique device id but got empty string")
	}

	// MDM is necessarily enabled if this gets called, always pass true for that
	// parameter.
	matchID, _, err := matchHostDuringEnrollment(ctx, tx, true, "", mdmHost.UDID, mdmHost.SerialNumber)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return insertMDMAppleHostDB(ctx, tx, mdmHost, logger, appCfg)

	case err != nil:
		return ctxerr.Wrap(ctx, err, "get mdm apple host by serial number or udid")

	default:
		return updateMDMAppleHostDB(ctx, tx, matchID, mdmHost, appCfg)
	}
}

func updateMDMAppleHostDB(
	ctx context.Context,
	tx sqlx.ExtContext,
	hostID uint,
	mdmHost fleet.MDMAppleHostDetails,
	appCfg *fleet.AppConfig,
) error {
	updateStmt := `
		UPDATE hosts SET
			hardware_serial = ?,
			uuid = ?,
			hardware_model = ?,
			platform =  ?,
			refetch_requested = ?,
			osquery_host_id = COALESCE(NULLIF(osquery_host_id, ''), ?)
		WHERE id = ?`

	if _, err := tx.ExecContext(
		ctx,
		updateStmt,
		mdmHost.SerialNumber,
		mdmHost.UDID,
		mdmHost.Model,
		"darwin",
		1,
		// Set osquery_host_id to the device UUID only if it is not already set.
		mdmHost.UDID,
		hostID,
	); err != nil {
		return ctxerr.Wrap(ctx, err, "update mdm apple host")
	}

	if err := upsertMDMAppleHostMDMInfoDB(ctx, tx, appCfg.ServerSettings, false, hostID); err != nil {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert MDM info")
	}

	return nil
}

func insertMDMAppleHostDB(
	ctx context.Context,
	tx sqlx.ExtContext,
	mdmHost fleet.MDMAppleHostDetails,
	logger log.Logger,
	appCfg *fleet.AppConfig,
) error {
	insertStmt := `
		INSERT INTO hosts (
			hardware_serial,
			uuid,
			hardware_model,
			platform,
			last_enrolled_at,
			detail_updated_at,
			osquery_host_id,
			refetch_requested
		) VALUES (?,?,?,?,?,?,?,?)`

	res, err := tx.ExecContext(
		ctx,
		insertStmt,
		mdmHost.SerialNumber,
		mdmHost.UDID,
		mdmHost.Model,
		"darwin",
		"2000-01-01 00:00:00",
		"2000-01-01 00:00:00",
		mdmHost.UDID,
		1,
	)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "insert mdm apple host")
	}

	id, err := res.LastInsertId()
	if err != nil {
		return ctxerr.Wrap(ctx, err, "last insert id mdm apple host")
	}
	if id < 1 {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host unexpected last insert id")
	}
	host := fleet.Host{ID: uint(id), HardwareModel: mdmHost.Model, HardwareSerial: mdmHost.SerialNumber}

	if err := upsertMDMAppleHostDisplayNamesDB(ctx, tx, host); err != nil {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert display names")
	}

	if err := upsertMDMAppleHostLabelMembershipDB(ctx, tx, logger, host); err != nil {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert label membership")
	}

	if err := upsertMDMAppleHostMDMInfoDB(ctx, tx, appCfg.ServerSettings, false, host.ID); err != nil {
		return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert MDM info")
	}
	return nil
}

func (ds *Datastore) IngestMDMAppleDevicesFromDEPSync(ctx context.Context, devices []godep.Device) (int64, error) {
	if len(devices) < 1 {
		level.Debug(ds.logger).Log("msg", "ingesting devices from DEP received < 1 device, skipping", "len(devices)", len(devices))
		return 0, nil
	}
	filteredDevices := filterMDMAppleDevices(devices, ds.logger)
	if len(filteredDevices) < 1 {
		level.Debug(ds.logger).Log("msg", "ingesting devices from DEP filtered all devices, skipping", "len(devices)", len(devices))
		return 0, nil
	}

	appCfg, err := ds.AppConfig(ctx)
	if err != nil {
		return 0, ctxerr.Wrap(ctx, err, "ingest mdm apple host get app config")
	}

	args := []interface{}{nil}
	if name := appCfg.MDM.AppleBMDefaultTeam; name != "" {
		team, err := ds.TeamByName(ctx, name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			level.Debug(ds.logger).Log(
				"msg",
				"ingesting devices from DEP: unable to find default team assigned in config, the devices won't be assigned to a team",
				"team_name",
				name,
			)
			// If the team doesn't exist, we still ingest the device, but it won't
			// belong to any team.
		case err != nil:
			return 0, ctxerr.Wrap(ctx, err, "ingest mdm apple host get team by name")
		default:
			args[0] = team.ID
		}
	}

	var resCount int64
	err = ds.withRetryTxx(ctx, func(tx sqlx.ExtContext) error {
		us, unionArgs := unionSelectDevices(filteredDevices)
		args = append(args, unionArgs...)

		stmt := fmt.Sprintf(`
		INSERT INTO hosts (
			hardware_serial,
			hardware_model,
			platform,
			last_enrolled_at,
			detail_updated_at,
			osquery_host_id,
			refetch_requested,
			team_id
		) (
			SELECT
				us.hardware_serial,
				COALESCE(GROUP_CONCAT(DISTINCT us.hardware_model), ''),
				'darwin' AS platform,
				'2000-01-01 00:00:00' AS last_enrolled_at,
				'2000-01-01 00:00:00' AS detail_updated_at,
				NULL AS osquery_host_id,
				1 AS refetch_requested,
				? AS team_id
			FROM (%s) us
			LEFT JOIN hosts h ON us.hardware_serial = h.hardware_serial
		WHERE
			h.id IS NULL
		GROUP BY
			us.hardware_serial)`,
			us,
		)

		res, err := tx.ExecContext(ctx, stmt, args...)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple hosts from dep sync insert")
		}

		n, err := res.RowsAffected()
		if err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple hosts from dep sync rows affected")
		}
		resCount = n

		// get new host ids
		args = []interface{}{}
		parts := []string{}
		for _, d := range filteredDevices {
			args = append(args, d.SerialNumber)
			parts = append(parts, "?")
		}
		var hosts []fleet.Host
		err = sqlx.SelectContext(ctx, tx, &hosts, fmt.Sprintf(`
			SELECT id, hardware_model, hardware_serial FROM hosts WHERE hardware_serial IN(%s)`,
			strings.Join(parts, ",")),
			args...)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple host get host ids")
		}

		if err := upsertMDMAppleHostDisplayNamesDB(ctx, tx, hosts...); err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert display names")
		}

		if err := upsertMDMAppleHostLabelMembershipDB(ctx, tx, ds.logger, hosts...); err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert label membership")
		}

		var ids []uint
		for _, h := range hosts {
			ids = append(ids, h.ID)
		}
		if err := upsertMDMAppleHostMDMInfoDB(ctx, tx, appCfg.ServerSettings, true, ids...); err != nil {
			return ctxerr.Wrap(ctx, err, "ingest mdm apple host upsert MDM info")
		}

		return nil
	})

	return resCount, err
}

func upsertMDMAppleHostDisplayNamesDB(ctx context.Context, tx sqlx.ExtContext, hosts ...fleet.Host) error {
	args := []interface{}{}
	parts := []string{}
	for _, h := range hosts {
		args = append(args, h.ID, h.DisplayName())
		parts = append(parts, "(?, ?)")
	}

	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO host_display_names (host_id, display_name) VALUES %s
			ON DUPLICATE KEY UPDATE display_name = VALUES(display_name)`, strings.Join(parts, ",")),
		args...)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "upsert host display names")
	}

	return nil
}

func upsertMDMAppleHostMDMInfoDB(ctx context.Context, tx sqlx.ExtContext, serverSettings fleet.ServerSettings, fromSync bool, hostIDs ...uint) error {
	serverURL, err := apple_mdm.ResolveAppleMDMURL(serverSettings.ServerURL)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "resolve Fleet MDM URL")
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO mobile_device_management_solutions (name, server_url) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE server_url = VALUES(server_url)`,
		fleet.WellKnownMDMFleet, serverURL)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "upsert mdm solution")
	}

	var mdmID int64
	if insertOnDuplicateDidInsert(result) {
		mdmID, _ = result.LastInsertId()
	} else {
		stmt := `SELECT id FROM mobile_device_management_solutions WHERE name = ? AND server_url = ?`
		if err := sqlx.GetContext(ctx, tx, &mdmID, stmt, fleet.WellKnownMDMFleet, serverURL); err != nil {
			return ctxerr.Wrap(ctx, err, "query mdm solution id")
		}
	}

	// if the device is coming from the DEP sync, we don't consider it
	// enrolled yet.
	enrolled := !fromSync

	args := []interface{}{}
	parts := []string{}
	for _, id := range hostIDs {
		args = append(args, enrolled, serverURL, fromSync, mdmID, false, id)
		parts = append(parts, "(?, ?, ?, ?, ?, ?)")
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO host_mdm (enrolled, server_url, installed_from_dep, mdm_id, is_server, host_id) VALUES %s
		ON DUPLICATE KEY UPDATE enrolled = VALUES(enrolled)`, strings.Join(parts, ",")), args...)

	return ctxerr.Wrap(ctx, err, "upsert host mdm info")
}

func upsertMDMAppleHostLabelMembershipDB(ctx context.Context, tx sqlx.ExtContext, logger log.Logger, hosts ...fleet.Host) error {
	// Builtin label memberships are usually inserted when the first distributed
	// query results are received; however, we want to insert pending MDM hosts
	// now because it may still be some time before osquery is running on these
	// devices. Because these are Apple devices, we're adding them to the "All
	// Hosts" and "macOS" labels.
	labelIDs := []uint{}
	err := sqlx.SelectContext(ctx, tx, &labelIDs, `SELECT id FROM labels WHERE label_type = 1 AND (name = 'All Hosts' OR name = 'macOS')`)
	switch {
	case err != nil:
		return ctxerr.Wrap(ctx, err, "get builtin labels")
	case len(labelIDs) != 2:
		// Builtin labels can get deleted so it is important that we check that
		// they still exist before we continue.
		level.Error(logger).Log("err", fmt.Sprintf("expected 2 builtin labels but got %d", len(labelIDs)))
		return nil
	default:
		// continue
	}

	parts := []string{}
	args := []interface{}{}
	for _, h := range hosts {
		parts = append(parts, "(?,?),(?,?)")
		args = append(args, h.ID, labelIDs[0], h.ID, labelIDs[1])
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO label_membership (host_id, label_id) VALUES %s
			ON DUPLICATE KEY UPDATE host_id = host_id`, strings.Join(parts, ",")), args...)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "upsert label membership")
	}

	return nil
}

func (ds *Datastore) deleteMDMAppleProfilesForHost(ctx context.Context, tx sqlx.ExtContext, uuid string) error {
	_, err := tx.ExecContext(ctx, `
                    DELETE FROM host_mdm_apple_profiles
                    WHERE host_uuid = ?`, uuid)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "removing all profiles from host")
	}
	return nil
}

func (ds *Datastore) UpdateHostTablesOnMDMUnenroll(ctx context.Context, uuid string) error {
	return ds.withTx(ctx, func(tx sqlx.ExtContext) error {
		var hostID uint
		row := tx.QueryRowxContext(ctx, `SELECT id FROM hosts WHERE uuid = ?`, uuid)
		err := row.Scan(&hostID)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "getting host id from UUID")
		}

		_, err = tx.ExecContext(ctx, `
			DELETE FROM host_mdm
			WHERE host_id = ?`, hostID)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "removing host_mdm rows for host")
		}

		// Since the host is unenrolled, delete all profiles assigned to the
		// host manually, the device won't Acknowledge any more requests (eg:
		// to delete profiles) and profiles are automatically removed on
		// unenrollment.
		if err := ds.deleteMDMAppleProfilesForHost(ctx, tx, uuid); err != nil {
			return ctxerr.Wrap(ctx, err, "deleting profiles for host")
		}

		_, err = tx.ExecContext(ctx, `
                    DELETE FROM host_disk_encryption_keys
                    WHERE host_id = ?`, hostID)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "removing all profiles from host")
		}

		return nil
	})
}

func filterMDMAppleDevices(devices []godep.Device, logger log.Logger) []godep.Device {
	var filtered []godep.Device
	for _, device := range devices {
		// We currently only listen for an op_type of "added", the
		// other op_types are ambiguous and it would be needless to
		// ingest the device every single time we get an update.
		if strings.ToLower(device.OpType) == "added" ||
			// The op_type field is only applicable with the SyncDevices
			// API call, Empty op_type come from the first call to
			// FetchDevices without a cursor.
			strings.ToLower(device.OpType) == "" {
			level.Debug(logger).Log("msg", "filterMDMAppleDevices: adding device", "serial", device.SerialNumber, "op_type", device.OpType, "os", device.OS)
			filtered = append(filtered, device)
			continue
		}
		level.Debug(logger).Log("msg", "filterMDMAppleDevices: skipping device", "serial", device.SerialNumber, "op_type", device.OpType, "os", device.OS)
	}
	return filtered
}

func unionSelectDevices(devices []godep.Device) (stmt string, args []interface{}) {
	for i, d := range devices {
		if i == 0 {
			stmt = "SELECT ? hardware_serial, ? hardware_model"
		} else {
			stmt += " UNION SELECT ?, ?"
		}
		args = append(args, d.SerialNumber, d.Model)
	}

	return stmt, args
}

func (ds *Datastore) GetNanoMDMEnrollment(ctx context.Context, id string) (*fleet.NanoEnrollment, error) {
	var nanoEnroll fleet.NanoEnrollment
	err := sqlx.GetContext(ctx, ds.reader, &nanoEnroll, `SELECT id, device_id, type, enabled, token_update_tally
		FROM nano_enrollments WHERE id = ?`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, ctxerr.Wrapf(ctx, err, "getting data from nano_enrollments for id %s", id)
	}

	return &nanoEnroll, nil
}

func (ds *Datastore) BatchSetMDMAppleProfiles(ctx context.Context, tmID *uint, profiles []*fleet.MDMAppleConfigProfile) error {
	const loadExistingProfiles = `
SELECT
  identifier,
  mobileconfig
FROM
  mdm_apple_configuration_profiles
WHERE
  team_id = ? AND
  identifier IN (?)
`

	const deleteProfilesNotInList = `
DELETE FROM
  mdm_apple_configuration_profiles
WHERE
  team_id = ? AND
  identifier NOT IN (?)
`

	const insertNewOrEditedProfile = `
INSERT INTO
  mdm_apple_configuration_profiles (
    team_id, identifier, name, mobileconfig, checksum
  )
VALUES
  ( ?, ?, ?, ?, UNHEX(MD5(mobileconfig)) )
ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  mobileconfig = VALUES(mobileconfig),
  checksum = UNHEX(MD5(VALUES(mobileconfig)))
`

	// use a profile team id of 0 if no-team
	var profTeamID uint
	if tmID != nil {
		profTeamID = *tmID
	}

	// build a list of identifiers for the incoming profiles, will keep the
	// existing ones if there's a match and no change
	incomingIdents := make([]string, len(profiles))
	// at the same time, index the incoming profiles keyed by identifier for ease
	// or processing
	incomingProfs := make(map[string]*fleet.MDMAppleConfigProfile, len(profiles))
	for i, p := range profiles {
		incomingIdents[i] = p.Identifier
		incomingProfs[p.Identifier] = p
	}

	return ds.withRetryTxx(ctx, func(tx sqlx.ExtContext) error {
		var existingProfiles []*fleet.MDMAppleConfigProfile

		if len(incomingIdents) > 0 {
			// load existing profiles that match the incoming profiles by identifiers
			stmt, args, err := sqlx.In(loadExistingProfiles, profTeamID, incomingIdents)
			if err != nil {
				return ctxerr.Wrap(ctx, err, "build query to load existing profiles")
			}
			if err := sqlx.SelectContext(ctx, tx, &existingProfiles, stmt, args...); err != nil {
				return ctxerr.Wrap(ctx, err, "load existing profiles")
			}
		}

		// figure out if we need to delete any profiles
		keepIdents := make([]string, 0, len(incomingIdents))
		for _, p := range existingProfiles {
			if newP := incomingProfs[p.Identifier]; newP != nil {
				keepIdents = append(keepIdents, p.Identifier)
			}
		}

		// profiles that are managed and delivered by Fleet
		fleetIdents := []string{}
		for ident := range mobileconfig.FleetPayloadIdentifiers() {
			fleetIdents = append(fleetIdents, ident)
		}

		var (
			stmt string
			args []interface{}
			err  error
		)
		// delete the obsolete profiles (all those that are not in keepIdents or delivered by Fleet)
		stmt, args, err = sqlx.In(deleteProfilesNotInList, profTeamID, append(keepIdents, fleetIdents...))
		if err != nil {
			return ctxerr.Wrap(ctx, err, "build statement to delete obsolete profiles")
		}
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return ctxerr.Wrap(ctx, err, "delete obsolete profiles")
		}

		// insert the new profiles and the ones that have changed
		for _, p := range incomingProfs {
			if _, err := tx.ExecContext(ctx, insertNewOrEditedProfile, profTeamID, p.Identifier, p.Name, p.Mobileconfig); err != nil {
				return ctxerr.Wrapf(ctx, err, "insert new/edited profile with identifier %q", p.Identifier)
			}
		}
		return nil
	})
}

// Note that team ID 0 is used for profiles that apply to hosts in no team
// (i.e. pass 0 in that case as part of the teamIDs slice). Only one of the
// slice arguments can have values.
func (ds *Datastore) BulkSetPendingMDMAppleHostProfiles(ctx context.Context, hostIDs, teamIDs, profileIDs []uint, hostUUIDs []string) error {
	return ds.withTx(ctx, func(tx sqlx.ExtContext) error {
		var countArgs int
		if len(hostIDs) > 0 {
			countArgs++
		}
		if len(teamIDs) > 0 {
			countArgs++
		}
		if len(profileIDs) > 0 {
			countArgs++
		}
		if len(hostUUIDs) > 0 {
			countArgs++
		}
		if countArgs > 1 {
			return errors.New("only one of hostIDs, teamIDs, profileIDs or hostUUIDs can be provided")
		}
		if countArgs == 0 {
			return nil
		}

		var (
			uuids    []string
			args     []any
			uuidStmt string
		)

		switch {
		case len(hostUUIDs) > 0:
			// no need to run a query to load host UUIDs, that's what we received
			// directly.
			uuids = hostUUIDs

		case len(hostIDs) > 0:
			uuidStmt = `SELECT uuid FROM hosts WHERE id IN (?)`
			args = append(args, hostIDs)

		case len(teamIDs) > 0:
			uuidStmt = `SELECT uuid FROM hosts WHERE `
			if len(teamIDs) == 1 && teamIDs[0] == 0 {
				uuidStmt += `team_id IS NULL`
			} else {
				uuidStmt += `team_id IN (?)`
				args = append(args, teamIDs)
				for _, tmID := range teamIDs {
					if tmID == 0 {
						uuidStmt += ` OR team_id IS NULL`
						break
					}
				}
			}

		case len(profileIDs) > 0:
			uuidStmt = `
SELECT DISTINCT h.uuid
FROM hosts h
JOIN mdm_apple_configuration_profiles macp
	ON h.team_id = macp.team_id OR (h.team_id IS NULL AND macp.team_id = 0)
WHERE
	macp.profile_id IN (?)`
			args = append(args, profileIDs)
		}

		if len(uuids) == 0 {
			uuidStmt, args, err := sqlx.In(uuidStmt, args...)
			if err != nil {
				return ctxerr.Wrap(ctx, err, "prepare query to load host UUIDs")
			}
			if err := sqlx.SelectContext(ctx, tx, &uuids, uuidStmt, args...); err != nil {
				return ctxerr.Wrap(ctx, err, "execute query to load host UUIDs")
			}
		}

		if len(uuids) == 0 {
			return nil
		}

		const profilesToInstallStmt = `
		SELECT
			ds.profile_id as profile_id,
			ds.host_uuid as host_uuid,
			ds.profile_identifier as profile_identifier,
			ds.profile_name as profile_name,
			ds.checksum as checksum
		FROM (
			SELECT
				macp.profile_id,
				h.uuid as host_uuid,
				macp.identifier as profile_identifier,
				macp.name as profile_name,
				macp.checksum as checksum
			FROM mdm_apple_configuration_profiles macp
				JOIN hosts h ON h.team_id = macp.team_id OR (h.team_id IS NULL AND macp.team_id = 0)
				JOIN nano_enrollments ne ON ne.device_id = h.uuid
			WHERE h.platform = 'darwin' AND ne.enabled = 1 AND ne.type = 'Device' AND h.uuid IN (?)
		) as ds
		LEFT JOIN host_mdm_apple_profiles hmap
			ON hmap.profile_id = ds.profile_id AND hmap.host_uuid = ds.host_uuid
		WHERE
		-- profile has been updated
		( hmap.checksum != ds.checksum ) OR
		-- profiles in A but not in B
		( hmap.profile_id IS NULL AND hmap.host_uuid IS NULL ) OR
		-- profiles in A and B but with operation type "remove"
		( hmap.host_uuid IS NOT NULL AND ( hmap.operation_type = ? OR hmap.operation_type IS NULL ) )`

		stmt, args, err := sqlx.In(profilesToInstallStmt,
			uuids, fleet.MDMAppleOperationTypeRemove,
		)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "building profiles to install statement")
		}

		var profilesToInstall []*fleet.MDMAppleProfilePayload
		err = sqlx.SelectContext(ctx, tx, &profilesToInstall, stmt, args...)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "bulk set pending profile status execute")
		}
		installIdentifiers := []string{}
		identifierToHosts := map[string][]string{}
		for _, p := range profilesToInstall {
			installIdentifiers = append(installIdentifiers, p.ProfileIdentifier)
			if _, ok := identifierToHosts[p.ProfileIdentifier]; !ok {
				identifierToHosts[p.ProfileIdentifier] = []string{}
			}
			identifierToHosts[p.ProfileIdentifier] = append(identifierToHosts[p.ProfileIdentifier], p.HostUUID)
		}

		profilesToRemoveStmt := `
		SELECT
			hmap.profile_id as profile_id,
			hmap.host_uuid as host_uuid,
			hmap.profile_identifier as profile_identifier,
			hmap.profile_name as profile_name,
			hmap.checksum as checksum
		FROM (
			SELECT
				h.uuid, macp.profile_id
			FROM mdm_apple_configuration_profiles macp
				JOIN hosts h ON h.team_id = macp.team_id OR (h.team_id IS NULL AND macp.team_id = 0)
				JOIN nano_enrollments ne ON ne.device_id = h.uuid
			WHERE h.platform = 'darwin' AND ne.enabled = 1 AND ne.type = 'Device' AND h.uuid IN (?)
		) as ds
		RIGHT JOIN host_mdm_apple_profiles hmap
			ON hmap.profile_id = ds.profile_id AND hmap.host_uuid = ds.uuid
		WHERE
		hmap.host_uuid IN (?)
		-- profiles that are in B but not in A
		AND ds.profile_id IS NULL AND ds.uuid IS NULL
		-- except "remove" operations in any state
		AND ( hmap.operation_type IS NULL OR hmap.operation_type != ? )
		-- profiles that are being installed
	`

		inArgs := []any{uuids, uuids, fleet.MDMAppleOperationTypeRemove}
		if len(installIdentifiers) > 0 {
			profilesToRemoveStmt += `AND hmap.profile_identifier NOT IN (?)`
			inArgs = append(inArgs, installIdentifiers)

		}

		stmt, args, err = sqlx.In(profilesToRemoveStmt, inArgs...)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "building profiles to remove statement")
		}
		var profilesToRemove []*fleet.MDMAppleProfilePayload
		err = sqlx.SelectContext(ctx, tx, &profilesToRemove, stmt, args...)
		if err != nil {
			return ctxerr.Wrap(ctx, err, "bulk set pending profile status execute")
		}

		if len(profilesToInstall) == 0 && len(profilesToRemove) == 0 {
			return nil
		}

		// before doing the inserts, remove profiles with identifiers that will be re-sent
		if len(profilesToInstall) > 0 {
			var dargs []any
			var dsb strings.Builder
			for identifier, hostUUIDs := range identifierToHosts {
				for _, hostUUID := range hostUUIDs {
					dargs = append(dargs, hostUUID, identifier)
					dsb.WriteString("(?,?),")
				}
			}
			stmt = fmt.Sprintf(`DELETE FROM host_mdm_apple_profiles WHERE (host_uuid, profile_identifier) IN(%s)`, strings.TrimSuffix(dsb.String(), ","))
			_, err = tx.ExecContext(ctx, stmt, dargs...)
			if err != nil {
				return ctxerr.Wrap(ctx, err, "bulk set pending profile status execute")
			}
		}

		var pargs []any
		var psb strings.Builder
		for _, p := range profilesToInstall {
			pargs = append(pargs, p.ProfileID, p.HostUUID, p.ProfileIdentifier, p.ProfileName, p.Checksum, fleet.MDMAppleOperationTypeInstall, nil, "")
			psb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?),")

		}
		for _, p := range profilesToRemove {
			pargs = append(pargs, p.ProfileID, p.HostUUID, p.ProfileIdentifier, p.ProfileName, p.Checksum, fleet.MDMAppleOperationTypeRemove, nil, "")
			psb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?),")

		}

		baseStmt := fmt.Sprintf(`
INSERT INTO host_mdm_apple_profiles (
	profile_id,
	host_uuid,
	profile_identifier,
	profile_name,
	checksum,
	operation_type,
	status,
	command_uuid
)
VALUES %s
ON DUPLICATE KEY UPDATE
	operation_type = VALUES(operation_type),
	status = VALUES(status),
	command_uuid = VALUES(command_uuid),
	checksum = VALUES(checksum),
	detail = ''
`, strings.TrimSuffix(psb.String(), ","))

		_, err = tx.ExecContext(ctx, baseStmt, pargs...)
		return ctxerr.Wrap(ctx, err, "bulk set pending profile status execute")
	})
}

func (ds *Datastore) ListMDMAppleProfilesToInstall(ctx context.Context) ([]*fleet.MDMAppleProfilePayload, error) {
	// The query below is a set difference between:
	//
	// - Set A (ds), the desired state, can be obtained from a JOIN between
	//   mdm_apple_configuration_profiles and hosts.
	// - Set B, the current state given by host_mdm_apple_profiles.
	//
	// A - B gives us the profiles that need to be installed:
	//
	//   - profiles that are in A but not in B
	//
	//   - profiles which contents have changed, but their identifier are
	//   the same (by matching checksums)
	//
	//   - profiles that are in A and in B, but with an operation type of
	//   "remove", regardless of the status. (technically, if status is NULL then
	//   the profile should be already installed - it has not been queued for
	//   remove yet -, and same if status is failed, but the proper thing to do
	//   with it would be to remove the row, not return it as "to install". For
	//   simplicity of implementation here (and to err on the safer side - the
	//   profile's content could've changed), we'll return it as "to install" for
	//   now, which will cause the row to be updated with the correct operation
	//   type and status).
	//
	//   - profiles that are in A and in B, with an operation type of "install"
	//   and a NULL status. Other statuses mean that the operation is already in
	//   flight (pending), the operation has been completed but is still subject
	//   to independent verification by Fleet (verifying), or has reached a terminal
	//   state (failed). If the profile's content is edited, all relevant hosts will
	//   be marked as status NULL so that it gets re-installed.
	query := `
          SELECT ds.profile_id, ds.host_uuid, ds.profile_identifier, ds.profile_name, ds.checksum
          FROM (
            SELECT
              macp.profile_id,
              h.uuid as host_uuid,
              macp.identifier as profile_identifier,
              macp.name as profile_name,
	      macp.checksum as checksum
            FROM mdm_apple_configuration_profiles macp
            JOIN hosts h ON h.team_id = macp.team_id OR (h.team_id IS NULL AND macp.team_id = 0)
            JOIN nano_enrollments ne ON ne.device_id = h.uuid
            WHERE h.platform = 'darwin' AND ne.enabled = 1 AND ne.type = 'Device'
          ) as ds
          LEFT JOIN host_mdm_apple_profiles hmap
            ON hmap.profile_id = ds.profile_id AND hmap.host_uuid = ds.host_uuid
          WHERE
	  -- profile has been updated
	  ( hmap.checksum != ds.checksum ) OR
          -- profiles in A but not in B
          ( hmap.profile_id IS NULL AND hmap.host_uuid IS NULL ) OR
          -- profiles in A and B but with operation type "remove"
          ( hmap.host_uuid IS NOT NULL AND ( hmap.operation_type = ? OR hmap.operation_type IS NULL ) ) OR
          -- profiles in A and B with operation type "install" and NULL status
          ( hmap.host_uuid IS NOT NULL AND hmap.operation_type = ? AND hmap.status IS NULL )
`

	var profiles []*fleet.MDMAppleProfilePayload
	err := sqlx.SelectContext(ctx, ds.reader, &profiles, query, fleet.MDMAppleOperationTypeRemove, fleet.MDMAppleOperationTypeInstall)
	return profiles, err
}

func (ds *Datastore) ListMDMAppleProfilesToRemove(ctx context.Context) ([]*fleet.MDMAppleProfilePayload, error) {
	// The query below is a set difference between:
	//
	// - Set A (ds), the desired state, can be obtained from a JOIN between
	// mdm_apple_configuration_profiles and hosts.
	// - Set B, the current state given by host_mdm_apple_profiles.
	//
	// B - A gives us the profiles that need to be removed:
	//
	//   - profiles that are in B but not in A, except those with operation type
	//   "remove" and a terminal state (failed) or a state indicating
	//   that the operation is in flight (pending) or the operation has been completed
	//   but is still subject to independent verification by Fleet (verifying).
	//
	// Any other case are profiles that are in both B and A, and as such are
	// processed by the ListMDMAppleProfilesToInstall method (since they are in
	// both, their desired state is necessarily to be installed).
	query := `
          SELECT hmap.profile_id, hmap.profile_identifier, hmap.profile_name, hmap.host_uuid, hmap.checksum
          FROM (
            SELECT h.uuid, macp.profile_id
            FROM mdm_apple_configuration_profiles macp
            JOIN hosts h ON h.team_id = macp.team_id OR (h.team_id IS NULL AND macp.team_id = 0)
            JOIN nano_enrollments ne ON ne.device_id = h.uuid
            WHERE h.platform = 'darwin' AND ne.enabled = 1 AND ne.type = 'Device'
          ) as ds
          RIGHT JOIN host_mdm_apple_profiles hmap
            ON hmap.profile_id = ds.profile_id AND hmap.host_uuid = ds.uuid
          -- profiles that are in B but not in A
          WHERE ds.profile_id IS NULL AND ds.uuid IS NULL
          -- except "remove" operations in a terminal state or already pending
          AND ( hmap.operation_type IS NULL OR hmap.operation_type != ? OR hmap.status IS NULL )
`

	var profiles []*fleet.MDMAppleProfilePayload
	err := sqlx.SelectContext(ctx, ds.reader, &profiles, query, fleet.MDMAppleOperationTypeRemove)
	return profiles, err
}

func (ds *Datastore) GetMDMAppleProfilesContents(ctx context.Context, ids []uint) (map[uint]mobileconfig.Mobileconfig, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	stmt := `
          SELECT profile_id, mobileconfig as mobileconfig
          FROM mdm_apple_configuration_profiles WHERE profile_id IN (?)
	`
	query, args, err := sqlx.In(stmt, ids)
	if err != nil {
		return nil, err
	}
	rows, err := ds.reader.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make(map[uint]mobileconfig.Mobileconfig)
	for rows.Next() {
		var id uint
		var mobileconfig mobileconfig.Mobileconfig
		if err := rows.Scan(&id, &mobileconfig); err != nil {
			return nil, err
		}
		results[id] = mobileconfig
	}
	return results, nil
}

func (ds *Datastore) BulkUpsertMDMAppleHostProfiles(ctx context.Context, payload []*fleet.MDMAppleBulkUpsertHostProfilePayload) error {
	if len(payload) == 0 {
		return nil
	}

	var args []any
	var sb strings.Builder

	for _, p := range payload {
		args = append(args, p.ProfileID, p.ProfileIdentifier, p.ProfileName, p.HostUUID, p.Status, p.OperationType, p.CommandUUID, p.Checksum)
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?),")
	}

	stmt := fmt.Sprintf(`
	    INSERT INTO host_mdm_apple_profiles (
              profile_id,
              profile_identifier,
              profile_name,
              host_uuid,
              status,
              operation_type,
              command_uuid,
	      checksum
            )
            VALUES %s
	    ON DUPLICATE KEY UPDATE
              status = VALUES(status),
              operation_type = VALUES(operation_type),
              command_uuid = VALUES(command_uuid)`,
		strings.TrimSuffix(sb.String(), ","),
	)

	_, err := ds.writer.ExecContext(ctx, stmt, args...)
	return err
}

func (ds *Datastore) UpdateOrDeleteHostMDMAppleProfile(ctx context.Context, profile *fleet.HostMDMAppleProfile) error {
	if profile.OperationType == fleet.MDMAppleOperationTypeRemove &&
		profile.Status != nil && (*profile.Status == fleet.MDMAppleDeliveryVerifying || profile.IgnoreMDMClientError()) {
		_, err := ds.writer.ExecContext(ctx, `
          DELETE FROM host_mdm_apple_profiles
          WHERE host_uuid = ? AND command_uuid = ?
        `, profile.HostUUID, profile.CommandUUID)
		return err
	}

	_, err := ds.writer.ExecContext(ctx, `
          UPDATE host_mdm_apple_profiles
          SET status = ?, operation_type = ?, detail = ?
          WHERE host_uuid = ? AND command_uuid = ?
        `, profile.Status, profile.OperationType, profile.Detail, profile.HostUUID, profile.CommandUUID)
	return err
}

func subqueryHostsMacOSSettingsStatusFailing() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND hmap.status = ?`
	args := []interface{}{fleet.MDMAppleDeliveryFailed}

	return sql, args
}

func subqueryHostsMacOSSettingsStatusPending() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND (hmap.status IS NULL
                    OR hmap.status = ?
                    OR(hmap.profile_identifier = ?
                        AND hmap.status = ?
                        AND hmap.operation_type = ?
                        AND NOT EXISTS (
                            SELECT
                                1 FROM host_disk_encryption_keys hdek
                            WHERE
                                h.id = hdek.host_id
                                AND hdek.decryptable = 1)))
                AND NOT EXISTS (
                    SELECT
                        1 FROM host_mdm_apple_profiles hmap2
                    WHERE
                        h.uuid = hmap2.host_uuid
                        AND hmap2.status = ?)`
	args := []interface{}{
		fleet.MDMAppleDeliveryPending,
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		fleet.MDMAppleOperationTypeInstall,
		fleet.MDMAppleDeliveryFailed,
	}
	return sql, args
}

func subqueryHostsMacOSSetttingsStatusVerifying() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND hmap.status = ?
                AND(hmap.profile_identifier != ?
                    OR EXISTS (
                        SELECT
                            1 FROM host_disk_encryption_keys hdek
                        WHERE
                            h.id = hdek.host_id
                            AND hdek.decryptable = 1))
                AND NOT EXISTS (
                    SELECT
                        1 FROM host_mdm_apple_profiles hmap2
                    WHERE
                        h.uuid = hmap2.host_uuid
                        AND (hmap2.status IS NULL
                            OR hmap2.status != ?
                            OR(hmap2.profile_identifier = ?
                                AND hmap2.status = ?
                                AND hmap2.operation_type = ?
                                AND NOT EXISTS (
                                    SELECT
                                        1 FROM host_disk_encryption_keys hdek
                                    WHERE
                                        h.id = hdek.host_id
                                        AND hdek.decryptable = 1))))`
	args := []interface{}{
		fleet.MDMAppleDeliveryVerifying,
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		fleet.MDMAppleOperationTypeInstall,
	}
	return sql, args
}

func (ds *Datastore) GetMDMAppleHostsProfilesSummary(ctx context.Context, teamID *uint) (*fleet.MDMAppleConfigProfilesSummary, error) {
	var args []interface{}
	subqueryFailed, subqueryFailedArgs := subqueryHostsMacOSSettingsStatusFailing()
	args = append(args, subqueryFailedArgs...)
	subqueryPending, subqueryPendingArgs := subqueryHostsMacOSSettingsStatusPending()
	args = append(args, subqueryPendingArgs...)
	subqueryVerifying, subqueryVeryingingArgs := subqueryHostsMacOSSetttingsStatusVerifying()
	args = append(args, subqueryVeryingingArgs...)

	sqlFmt := `
SELECT
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS failed,
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS pending,
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS verifying
FROM
    hosts h
WHERE
    %s`

	teamFilter := "h.team_id IS NULL"
	if teamID != nil && *teamID > 0 {
		teamFilter = "h.team_id = ?"
		args = append(args, *teamID)
	}

	stmt := fmt.Sprintf(sqlFmt, subqueryFailed, subqueryPending, subqueryVerifying, teamFilter)

	var res fleet.MDMAppleConfigProfilesSummary
	err := sqlx.GetContext(ctx, ds.reader, &res, stmt, args...)
	if err != nil {
		return nil, err
	}

	return &res, nil
}

func (ds *Datastore) InsertMDMIdPAccount(ctx context.Context, account *fleet.MDMIdPAccount) error {
	stmt := `
      INSERT INTO mdm_idp_accounts
        (uuid, username, salt, entropy, iterations)
      VALUES
        (?, ?, ?, ?, ?)
      ON DUPLICATE KEY UPDATE
        username   = VALUES(username),
        salt       = VALUES(salt),
        entropy    = VALUES(entropy),
        iterations = VALUES(iterations)`

	_, err := ds.writer.ExecContext(ctx, stmt, account.UUID, account.Username, account.Salt, account.Entropy, account.Iterations)
	return ctxerr.Wrap(ctx, err, "creating new MDM IdP account")
}

func subqueryDiskEncryptionVerifying() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND hdek.decryptable = 1
                AND hmap.profile_identifier = ?
                AND hmap.status = ?
                AND hmap.operation_type = ?`
	args := []interface{}{
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		fleet.MDMAppleOperationTypeInstall,
	}
	return sql, args
}

func subqueryDiskEncryptionActionRequired() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND(hdek.decryptable = 0
                    OR (hdek.host_id IS NULL AND hdek.decryptable IS NULL))
                AND hmap.profile_identifier = ?
                AND hmap.status = ?
                AND hmap.operation_type = ?`
	args := []interface{}{
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		fleet.MDMAppleOperationTypeInstall,
	}
	return sql, args
}

func subqueryDiskEncryptionEnforcing() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND hmap.profile_identifier = ?
                AND (hmap.status IS NULL OR hmap.status = ?)
                AND hmap.operation_type = ?
                UNION SELECT
                    1 FROM host_mdm_apple_profiles hmap
                WHERE
                    h.uuid = hmap.host_uuid
                    AND hmap.profile_identifier = ?
                    AND (hmap.status IS NOT NULL AND hmap.status = ?)
                    AND hmap.operation_type = ?
                    AND hdek.decryptable IS NULL
                    AND hdek.host_id IS NOT NULL`
	args := []interface{}{
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryPending,
		fleet.MDMAppleOperationTypeInstall,
		mobileconfig.FleetFileVaultPayloadIdentifier,
		fleet.MDMAppleDeliveryVerifying,
		fleet.MDMAppleOperationTypeInstall,
	}
	return sql, args
}

func subqueryDiskEncryptionFailed() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
			    h.uuid = hmap.host_uuid
                AND hmap.profile_identifier = ?
                AND hmap.status = ?`
	args := []interface{}{mobileconfig.FleetFileVaultPayloadIdentifier, fleet.MDMAppleDeliveryFailed}
	return sql, args
}

func subqueryDiskEncryptionRemovingEnforcement() (string, []interface{}) {
	sql := `
            SELECT
                1 FROM host_mdm_apple_profiles hmap
            WHERE
                h.uuid = hmap.host_uuid
                AND hmap.profile_identifier = ?
                AND (hmap.status IS NULL OR hmap.status = ?)
                AND hmap.operation_type = ?`
	args := []interface{}{mobileconfig.FleetFileVaultPayloadIdentifier, fleet.MDMAppleDeliveryPending, fleet.MDMAppleOperationTypeRemove}
	return sql, args
}

func (ds *Datastore) GetMDMAppleFileVaultSummary(ctx context.Context, teamID *uint) (*fleet.MDMAppleFileVaultSummary, error) {
	sqlFmt := `
SELECT
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS verifying,
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS action_required,
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS enforcing,
    COUNT(
		CASE WHEN EXISTS (%s)
            THEN 1
        END) AS failed,
    COUNT(
        CASE WHEN EXISTS (%s)
            THEN 1
        END) AS removing_enforcement
FROM
    hosts h
    LEFT JOIN host_disk_encryption_keys hdek ON h.id = hdek.host_id
WHERE
    %s`

	var args []interface{}
	subqueryVerifying, subqueryVerifyingArgs := subqueryDiskEncryptionVerifying()
	args = append(args, subqueryVerifyingArgs...)
	subqueryActionRequired, subqueryActionRequiredArgs := subqueryDiskEncryptionActionRequired()
	args = append(args, subqueryActionRequiredArgs...)
	subqueryEnforcing, subqueryEnforcingArgs := subqueryDiskEncryptionEnforcing()
	args = append(args, subqueryEnforcingArgs...)
	subqueryFailed, subqueryFailedArgs := subqueryDiskEncryptionFailed()
	args = append(args, subqueryFailedArgs...)
	subqueryRemovingEnforcement, subqueryRemovingEnforcementArgs := subqueryDiskEncryptionRemovingEnforcement()
	args = append(args, subqueryRemovingEnforcementArgs...)

	teamFilter := "h.team_id IS NULL"
	if teamID != nil && *teamID > 0 {
		teamFilter = "h.team_id = ?"
		args = append(args, *teamID)
	}

	stmt := fmt.Sprintf(sqlFmt, subqueryVerifying, subqueryActionRequired, subqueryEnforcing, subqueryFailed, subqueryRemovingEnforcement, teamFilter)

	var res fleet.MDMAppleFileVaultSummary
	err := sqlx.GetContext(ctx, ds.reader, &res, stmt, args...)
	if err != nil {
		return nil, err
	}

	return &res, nil
}

func (ds *Datastore) BulkUpsertMDMAppleConfigProfiles(ctx context.Context, payload []*fleet.MDMAppleConfigProfile) error {
	if len(payload) == 0 {
		return nil
	}

	var args []any
	var sb strings.Builder
	for _, cp := range payload {
		var teamID uint
		if cp.TeamID != nil {
			teamID = *cp.TeamID
		}

		args = append(args, teamID, cp.Identifier, cp.Name, cp.Mobileconfig)
		sb.WriteString("(?, ?, ?, ?, UNHEX(MD5(mobileconfig))),")
	}

	stmt := fmt.Sprintf(`
          INSERT INTO
              mdm_apple_configuration_profiles (team_id, identifier, name, mobileconfig, checksum)
          VALUES %s
          ON DUPLICATE KEY UPDATE
            mobileconfig = VALUES(mobileconfig),
	    checksum = UNHEX(MD5(VALUES(mobileconfig)))`, strings.TrimSuffix(sb.String(), ","))

	if _, err := ds.writer.ExecContext(ctx, stmt, args...); err != nil {
		return ctxerr.Wrapf(ctx, err, "upsert mdm config profiles")
	}

	return nil
}

func (ds *Datastore) InsertMDMAppleBootstrapPackage(ctx context.Context, bp *fleet.MDMAppleBootstrapPackage) error {
	stmt := `
          INSERT INTO mdm_apple_bootstrap_packages (team_id, name, sha256, bytes, token)
	  VALUES (?, ?, ?, ?, ?)
	`

	_, err := ds.writer.ExecContext(ctx, stmt, bp.TeamID, bp.Name, bp.Sha256, bp.Bytes, bp.Token)
	if err != nil {
		if isDuplicate(err) {
			return ctxerr.Wrap(ctx, alreadyExists("BootstrapPackage", fmt.Sprintf("for team %d", bp.TeamID)))
		}
		return ctxerr.Wrap(ctx, err, "create bootstrap pacckage")
	}

	return nil
}

func (ds *Datastore) DeleteMDMAppleBootstrapPackage(ctx context.Context, teamID uint) error {
	stmt := "DELETE FROM mdm_apple_bootstrap_packages WHERE team_id = ?"
	res, err := ds.writer.ExecContext(ctx, stmt, teamID)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "delete bootstrap package")
	}

	deleted, _ := res.RowsAffected()
	if deleted != 1 {
		return ctxerr.Wrap(ctx, notFound("BootstrapPackage").WithID(teamID))
	}
	return nil
}

func (ds *Datastore) GetMDMAppleBootstrapPackageBytes(ctx context.Context, token string) (*fleet.MDMAppleBootstrapPackage, error) {
	stmt := "SELECT name, bytes FROM mdm_apple_bootstrap_packages WHERE token = ?"
	var bp fleet.MDMAppleBootstrapPackage
	if err := sqlx.GetContext(ctx, ds.reader, &bp, stmt, token); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("BootstrapPackage").WithMessage(token))
		}
		return nil, ctxerr.Wrap(ctx, err, "get bootstrap package bytes")
	}
	return &bp, nil
}

func (ds *Datastore) GetMDMAppleBootstrapPackageSummary(ctx context.Context, teamID uint) (*fleet.MDMAppleBootstrapPackageSummary, error) {
	stmt := `
          SELECT
              COUNT(IF(ncr.status = 'Acknowledged', 1, NULL)) AS installed,
              COUNT(IF(ncr.status = 'Error', 1, NULL)) AS failed,
              COUNT(IF(ncr.status IS NULL OR (ncr.status != 'Acknowledged' AND ncr.status != 'Error'), 1, NULL)) AS pending
          FROM
              hosts h
          LEFT JOIN host_mdm_apple_bootstrap_packages hmabp ON
              hmabp.host_uuid = h.uuid
          LEFT JOIN nano_command_results ncr ON
              ncr.command_uuid  = hmabp.command_uuid
          JOIN host_mdm hm ON
              hm.host_id = h.id
          WHERE
              hm.installed_from_dep = 1 AND COALESCE(h.team_id, 0) = ?`

	var bp fleet.MDMAppleBootstrapPackageSummary
	if err := sqlx.GetContext(ctx, ds.reader, &bp, stmt, teamID); err != nil {
		return nil, ctxerr.Wrap(ctx, err, "get bootstrap package summary")
	}
	return &bp, nil
}

func (ds *Datastore) RecordHostBootstrapPackage(ctx context.Context, commandUUID string, hostUUID string) error {
	stmt := `INSERT INTO host_mdm_apple_bootstrap_packages (command_uuid, host_uuid) VALUES (?, ?)
        ON DUPLICATE KEY UPDATE command_uuid = command_uuid`
	_, err := ds.writer.ExecContext(ctx, stmt, commandUUID, hostUUID)
	return ctxerr.Wrap(ctx, err, "record bootstrap package command")
}

func (ds *Datastore) GetHostMDMMacOSSetup(ctx context.Context, hostID uint) (*fleet.HostMDMMacOSSetup, error) {
	stmt := `
SELECT
    CASE
        WHEN ncr.status = 'Acknowledged' THEN ?
        WHEN ncr.status = 'Error' THEN ?
        ELSE ?
    END AS bootstrap_package_status,
    COALESCE(ncr.result, '') AS result,
		mabs.name AS bootstrap_package_name
FROM
    hosts h
JOIN host_mdm_apple_bootstrap_packages hmabp ON
    hmabp.host_uuid = h.uuid
LEFT JOIN nano_command_results ncr ON
    ncr.command_uuid = hmabp.command_uuid
JOIN host_mdm hm ON
    hm.host_id = h.id
JOIN mdm_apple_bootstrap_packages mabs ON
		COALESCE(h.team_id, 0) = mabs.team_id
WHERE
    h.id = ? AND hm.installed_from_dep = 1`

	args := []interface{}{fleet.MDMBootstrapPackageInstalled, fleet.MDMBootstrapPackageFailed, fleet.MDMBootstrapPackagePending, hostID}

	var dest fleet.HostMDMMacOSSetup
	if err := sqlx.GetContext(ctx, ds.reader, &dest, stmt, args...); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("HostMDMMacOSSetup").WithID(hostID))
		}
		return nil, ctxerr.Wrap(ctx, err, "get host mdm macos setup")
	}

	if dest.BootstrapPackageStatus == fleet.MDMBootstrapPackageFailed {
		decoded, err := mdm.DecodeCommandResults(dest.Result)
		if err != nil {
			dest.Detail = "Unable to decode command result"
		} else {
			dest.Detail = apple_mdm.FmtErrorChain(decoded.ErrorChain)
		}
	}
	return &dest, nil
}

func (ds *Datastore) GetMDMAppleBootstrapPackageMeta(ctx context.Context, teamID uint) (*fleet.MDMAppleBootstrapPackage, error) {
	stmt := "SELECT team_id, name, sha256, token, created_at, updated_at FROM mdm_apple_bootstrap_packages WHERE team_id = ?"
	var bp fleet.MDMAppleBootstrapPackage
	if err := sqlx.GetContext(ctx, ds.reader, &bp, stmt, teamID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("BootstrapPackage").WithID(teamID))
		}
		return nil, ctxerr.Wrap(ctx, err, "get bootstrap package meta")
	}
	return &bp, nil
}

func (ds *Datastore) CleanupDiskEncryptionKeysOnTeamChange(ctx context.Context, hostIDs []uint, newTeamID *uint) error {
	return ds.withTx(ctx, func(tx sqlx.ExtContext) error {
		return cleanupDiskEncryptionKeysOnTeamChangeDB(ctx, tx, hostIDs, newTeamID)
	})
}

func cleanupDiskEncryptionKeysOnTeamChangeDB(ctx context.Context, tx sqlx.ExtContext, hostIDs []uint, newTeamID *uint) error {
	_, err := getMDMAppleConfigProfileByTeamAndIdentifierDB(ctx, tx, newTeamID, mobileconfig.FleetFileVaultPayloadIdentifier)
	if err != nil {
		if fleet.IsNotFound(err) {
			// the new team does not have a filevault profile so we need to delete the existing ones
			if err := bulkDeleteHostDiskEncryptionKeysDB(ctx, tx, hostIDs); err != nil {
				return ctxerr.Wrap(ctx, err, "reconcile filevault profiles on team change bulk delete host disk encryption keys")
			}
		} else {
			return ctxerr.Wrap(ctx, err, "reconcile filevault profiles on team change get profile")
		}
	}
	return nil
}

func getMDMAppleConfigProfileByTeamAndIdentifierDB(ctx context.Context, tx sqlx.QueryerContext, teamID *uint, profileIdentifier string) (*fleet.MDMAppleConfigProfile, error) {
	if teamID == nil {
		teamID = ptr.Uint(0)
	}

	stmt := `
SELECT
	profile_id,
	team_id,
	name,
	identifier,
	mobileconfig,
	created_at,
	updated_at
FROM
	mdm_apple_configuration_profiles
WHERE
	team_id=? AND identifier=?`

	var profile fleet.MDMAppleConfigProfile
	err := sqlx.GetContext(ctx, tx, &profile, stmt, teamID, profileIdentifier)
	if err != nil {
		if err == sql.ErrNoRows {
			return &fleet.MDMAppleConfigProfile{}, ctxerr.Wrap(ctx, notFound("MDMAppleConfigProfile").WithName(profileIdentifier))
		}
		return &fleet.MDMAppleConfigProfile{}, ctxerr.Wrap(ctx, err, "get mdm apple config profile by team and identifier")
	}
	return &profile, nil
}

func bulkDeleteHostDiskEncryptionKeysDB(ctx context.Context, tx sqlx.ExtContext, hostIDs []uint) error {
	if len(hostIDs) == 0 {
		return nil
	}

	query, args, err := sqlx.In(
		"DELETE FROM host_disk_encryption_keys WHERE host_id IN (?)",
		hostIDs,
	)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "building query")
	}

	_, err = tx.ExecContext(ctx, query, args...)
	return err
}

func (ds *Datastore) MDMAppleGetEULAMetadata(ctx context.Context) (*fleet.MDMAppleEULA, error) {
	// Currently, there can only be one EULA in the database, and we're
	// hardcoding it's id to be 1 in order to enforce this restriction.
	stmt := "SELECT name, created_at, token FROM eulas WHERE id = 1"
	var eula fleet.MDMAppleEULA
	if err := sqlx.GetContext(ctx, ds.reader, &eula, stmt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("MDMAppleEULA"))
		}
		return nil, ctxerr.Wrap(ctx, err, "get EULA metadata")
	}
	return &eula, nil
}

func (ds *Datastore) MDMAppleGetEULABytes(ctx context.Context, token string) (*fleet.MDMAppleEULA, error) {
	stmt := "SELECT name, bytes FROM eulas WHERE token = ?"
	var eula fleet.MDMAppleEULA
	if err := sqlx.GetContext(ctx, ds.reader, &eula, stmt, token); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("MDMAppleEULA"))
		}
		return nil, ctxerr.Wrap(ctx, err, "get EULA bytes")
	}
	return &eula, nil
}

func (ds *Datastore) MDMAppleInsertEULA(ctx context.Context, eula *fleet.MDMAppleEULA) error {
	// We're intentionally hardcoding the id to be 1 because we only want to
	// allow one EULA.
	stmt := `
          INSERT INTO eulas (id, name, bytes, token)
	  VALUES (1, ?, ?, ?)
	`

	_, err := ds.writer.ExecContext(ctx, stmt, eula.Name, eula.Bytes, eula.Token)
	if err != nil {
		if isDuplicate(err) {
			return ctxerr.Wrap(ctx, alreadyExists("MDMAppleEULA", eula.Token))
		}
		return ctxerr.Wrap(ctx, err, "create EULA")
	}

	return nil
}

func (ds *Datastore) MDMAppleDeleteEULA(ctx context.Context, token string) error {
	stmt := "DELETE FROM eulas WHERE token = ?"
	res, err := ds.writer.ExecContext(ctx, stmt, token)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "delete EULA")
	}

	deleted, _ := res.RowsAffected()
	if deleted != 1 {
		return ctxerr.Wrap(ctx, notFound("MDMAppleEULA"))
	}
	return nil
}

func (ds *Datastore) SetOrUpdateMDMAppleSetupAssistant(ctx context.Context, asst *fleet.MDMAppleSetupAssistant) (*fleet.MDMAppleSetupAssistant, error) {
	const stmt = `
		INSERT INTO
			mdm_apple_setup_assistants (team_id, global_or_team_id, name, profile)
		VALUES
			(?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			updated_at = IF(profile = VALUES(profile) AND name = VALUES(name), updated_at, CURRENT_TIMESTAMP),
			profile_uuid = IF(profile = VALUES(profile) AND name = VALUES(name), profile_uuid, ''),
			name = VALUES(name),
			profile = VALUES(profile)
`
	var globalOrTmID uint
	if asst.TeamID != nil {
		globalOrTmID = *asst.TeamID
	}
	_, err := ds.writer.ExecContext(ctx, stmt, asst.TeamID, globalOrTmID, asst.Name, asst.Profile)
	if err != nil {
		return nil, ctxerr.Wrap(ctx, err, "upsert mdm apple setup assistant")
	}

	// reload to return the proper timestamp and id
	return ds.getMDMAppleSetupAssistant(ctx, ds.writer, asst.TeamID)
}

func (ds *Datastore) SetMDMAppleSetupAssistantProfileUUID(ctx context.Context, teamID *uint, profileUUID string) error {
	const stmt = `
	UPDATE
		mdm_apple_setup_assistants
	SET
		profile_uuid = ?,
		-- ensure updated_at does not change, as it is used to reflect the time
		-- the setup assistant was uploaded, not when its profile was defined
		-- with Apple's API.
		updated_at = updated_at
	WHERE global_or_team_id = ?`

	var globalOrTmID uint
	if teamID != nil {
		globalOrTmID = *teamID
	}
	res, err := ds.writer.ExecContext(ctx, stmt, profileUUID, globalOrTmID)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "set mdm apple setup assistant profile uuid")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ctxerr.Wrap(ctx, notFound("MDMAppleSetupAssistant").WithID(globalOrTmID))
	}
	return nil
}

func (ds *Datastore) GetMDMAppleSetupAssistant(ctx context.Context, teamID *uint) (*fleet.MDMAppleSetupAssistant, error) {
	return ds.getMDMAppleSetupAssistant(ctx, ds.reader, teamID)
}

func (ds *Datastore) getMDMAppleSetupAssistant(ctx context.Context, q sqlx.QueryerContext, teamID *uint) (*fleet.MDMAppleSetupAssistant, error) {
	const stmt = `
	SELECT
		id,
		team_id,
		name,
		profile,
		profile_uuid,
		updated_at as uploaded_at
	FROM
		mdm_apple_setup_assistants
	WHERE global_or_team_id = ?`

	var asst fleet.MDMAppleSetupAssistant
	var globalOrTmID uint
	if teamID != nil {
		globalOrTmID = *teamID
	}
	if err := sqlx.GetContext(ctx, q, &asst, stmt, globalOrTmID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ctxerr.Wrap(ctx, notFound("MDMAppleSetupAssistant").WithID(globalOrTmID))
		}
		return nil, ctxerr.Wrap(ctx, err, "get mdm apple setup assistant")
	}
	return &asst, nil
}

func (ds *Datastore) DeleteMDMAppleSetupAssistant(ctx context.Context, teamID *uint) error {
	const stmt = `
		DELETE FROM mdm_apple_setup_assistants
		WHERE global_or_team_id = ?`

	var globalOrTmID uint
	if teamID != nil {
		globalOrTmID = *teamID
	}
	_, err := ds.writer.ExecContext(ctx, stmt, globalOrTmID)
	if err != nil {
		return ctxerr.Wrap(ctx, err, "delete mdm apple setup assistant")
	}
	return nil
}
