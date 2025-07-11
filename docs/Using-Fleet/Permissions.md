# Permissions

Users have different abilities depending on the access level they have.

## Roles

### Admin

Users with the admin role receive all permissions.

### Maintainer

Maintainers can manage most entities in Fleet, like queries, policies, labels and schedules.
Unlike admins, maintainers cannot edit higher level settings like application configuration, teams or users.

### Observer

The Observer role is a read-only role. It can access most entities in Fleet, like queries, policies, labels, schedules, application configuration, teams, etc.
They can also run queries configured with the `observer_can_run` flag set to `true`.

### Observer+

`Applies only to Fleet Premium`

Observer+ is an Observer with the added ability to run *any* query.

### GitOps

`Applies only to Fleet Premium`

GitOps is a modern approach to Continuous Deployment (CD) that uses Git as the single source of truth for declarative infrastructure and application configurations.
GitOps is an API-only and write-only role that can be used on CI/CD pipelines.

## User permissions

| **Action**                                                                                                                                 | Observer | Observer+ *| Maintainer | Admin | GitOps *|
| ------------------------------------------------------------------------------------------------------------------------------------------ | -------- | --------- | ---------- | ----- | ------ |
| View all [activity](https://fleetdm.com/docs/using-fleet/rest-api#activities)                                                              | ✅        | ✅         | ✅          | ✅     |        |
| View all hosts                                                                                                                             | ✅        | ✅         | ✅          | ✅     |        |
| Filter hosts using [labels](https://fleetdm.com/docs/using-fleet/rest-api#labels)                                                          | ✅        | ✅         | ✅          | ✅     |        |
| Target hosts using labels                                                                                                                  | ✅        | ✅         | ✅          | ✅     |        |
| Add and delete hosts                                                                                                                       |          |           | ✅          | ✅     |        |
| Transfer hosts between teams\*                                                                                                             |          |           | ✅          | ✅     | ✅      |
| Create, edit, and delete labels                                                                                                            |          |           | ✅          | ✅     | ✅      |
| View all software                                                                                                                          | ✅        | ✅         | ✅          | ✅     |        |
| Filter software by [vulnerabilities](https://fleetdm.com/docs/using-fleet/vulnerability-processing#vulnerability-processing)               | ✅        | ✅         | ✅          | ✅     |        |
| Filter hosts by software                                                                                                                   | ✅        | ✅         | ✅          | ✅     |        |
| Filter software by team\*                                                                                                                  | ✅        | ✅         | ✅          | ✅     |        |
| Manage [vulnerability automations](https://fleetdm.com/docs/using-fleet/automations#vulnerability-automations)                             |          |           |            | ✅     | ✅      |
| Run only designated, **observer can run**, queries as live queries against all hosts                                                       | ✅        | ✅         | ✅          | ✅     |        |
| Run any query as [live query](https://fleetdm.com/docs/using-fleet/fleet-ui#run-a-query) against all hosts                                 |          | ✅         | ✅          | ✅     |        |
| Create, edit, and delete queries                                                                                                           |          |           | ✅          | ✅     | ✅      |
| View all queries\**                                                                                                                        | ✅        | ✅         | ✅          | ✅     |        |
| Add, edit, and remove queries from all schedules                                                                                           |          |           | ✅          | ✅     | ✅      |
| Create, edit, view, and delete packs                                                                                                       |          |           | ✅          | ✅     | ✅      |
| View all policies                                                                                                                          | ✅        | ✅         | ✅          | ✅     |        |
| Filter hosts using policies                                                                                                                | ✅        | ✅         | ✅          | ✅     |        |
| Create, edit, and delete policies for all hosts                                                                                            |          |           | ✅          | ✅     | ✅      |
| Create, edit, and delete policies for all hosts assigned to team\*                                                                         |          |           | ✅          | ✅     | ✅      |
| Manage [policy automations](https://fleetdm.com/docs/using-fleet/automations#policy-automations)                                           |          |           |            | ✅     | ✅      |
| Create, edit, view, and delete users                                                                                                       |          |           |            | ✅     |        |
| Add and remove team members\*                                                                                                              |          |           |            | ✅     | ✅      |
| Create, edit, and delete teams\*                                                                                                           |          |           |            | ✅     | ✅      |
| Create, edit, and delete [enroll secrets](https://fleetdm.com/docs/deploying/faq#when-do-i-need-to-deploy-a-new-enroll-secret-to-my-hosts) |          |           | ✅          | ✅     | ✅      |
| Create, edit, and delete [enroll secrets for teams](https://fleetdm.com/docs/using-fleet/rest-api#get-enroll-secrets-for-a-team)\*         |          |           | ✅          | ✅     |        |
| Read organization settings and agent options\***                                                                                           | ✅        | ✅         | ✅          | ✅     |        |
| Edit [organization settings](https://fleetdm.com/docs/using-fleet/configuration-files#organization-settings)                               |          |           |            | ✅     | ✅      |
| Edit [agent options](https://fleetdm.com/docs/using-fleet/configuration-files#agent-options)                                               |          |           |            | ✅     | ✅      |
| Edit [agent options for hosts assigned to teams](https://fleetdm.com/docs/using-fleet/configuration-files#team-agent-options)\*            |          |           |            | ✅     | ✅      |
| Initiate [file carving](https://fleetdm.com/docs/using-fleet/rest-api#file-carving)                                                        |          |           | ✅          | ✅     |        |
| Retrieve contents from file carving                                                                                                        |          |           |            | ✅     |        |
| View Apple mobile device management (MDM) certificate information                                                                          |          |           |            | ✅     |        |
| View Apple business manager (BM) information                                                                                               |          |           |            | ✅     |        |
| Generate Apple mobile device management (MDM) certificate signing request (CSR)                                                            |          |           |            | ✅     |        |
| View disk encryption key for macOS hosts enrolled in Fleet's MDM                                                                           | ✅        | ✅         | ✅          | ✅     |        |
| Create edit and delete configuration profiles for macOS hosts enrolled in Fleet's MDM                                                      |          |           | ✅          | ✅     | ✅      |
| Execute MDM commands on macOS hosts enrolled in Fleet's MDM                                                                                |          |           | ✅          | ✅     |        |
| View results of MDM commands executed on macOS hosts enrolled in Fleet's MDM                                                               | ✅        | ✅         | ✅          | ✅     |        |
| Edit [MDM settings](https://fleetdm.com/docs/using-fleet/mdm-macos-settings)                                                               |          |           |            | ✅     | ✅      |
| Edit [MDM settings for teams](https://fleetdm.com/docs/using-fleet/mdm-macos-settings)                                                     |          |           |            | ✅     | ✅      |
| Upload an EULA file for MDM automatic enrollment\*                                                                                         |          |           |            | ✅     |         |
| View/download MDM macOS setup assistant\*                                                                                                  |          |           | ✅          | ✅     |        |
| Edit/upload MDM macOS setup assistant\*                                                                                                    |          |           | ✅          | ✅     |       |

\* Applies only to Fleet Premium

\** Global observers can view all queries but the UI and fleetctl only list the ones they can run (**observer can run**).

\*** Applies only to [Fleet REST API](https://fleetdm.com/docs/using-fleet/rest-api)

## Team member permissions

`Applies only to Fleet Premium`

Users in Fleet either have team access or global access.

Users with team access only have access to the [hosts](https://fleetdm.com/docs/using-fleet/rest-api#hosts), [software](https://fleetdm.com/docs/using-fleet/rest-api#software), [schedules](https://fleetdm.com/docs/using-fleet/fleet-ui#schedule-a-query) , and [policies](https://fleetdm.com/docs/using-fleet/rest-api#policies) assigned to
their team.

Users with global access have access to all
[hosts](https://fleetdm.com/docs/using-fleet/rest-api#hosts), [software](https://fleetdm.com/docs/using-fleet/rest-api#software), [queries](https://fleetdm.com/docs/using-fleet/rest-api#queries), [schedules](https://fleetdm.com/docs/using-fleet/fleet-ui#schedule-a-query) , and [policies](https://fleetdm.com/docs/using-fleet/rest-api#policies). Check out [the user permissions
table](#user-permissions) above for global user permissions.

Users can be a member of multiple teams in Fleet.

Users that are members of multiple teams can be assigned different roles for each team. For example, a user can be given access to the "Workstations" team and assigned the "Observer" role. This same user can be given access to the "Servers" team and assigned the "Maintainer" role.

| **Action**                                                                                                                       | Team observer | Team observer+ | Team maintainer | Team admin | Team GitOps |
| -------------------------------------------------------------------------------------------------------------------------------- | ------------- | -------------- | --------------- | ---------- | ----------- |
| View hosts                                                                                                                       | ✅             | ✅              | ✅               | ✅          |             |
| Filter hosts using [labels](https://fleetdm.com/docs/using-fleet/rest-api#labels)                                                | ✅             | ✅              | ✅               | ✅          |             |
| Target hosts using labels                                                                                                        | ✅             | ✅              | ✅               | ✅          |             |
| Add and delete hosts                                                                                                             |               |                | ✅               | ✅          |             |
| Filter software by [vulnerabilities](https://fleetdm.com/docs/using-fleet/vulnerability-processing#vulnerability-processing) | ✅             | ✅              | ✅               | ✅          |             |
| Filter hosts by software                                                                                                         | ✅             | ✅              | ✅               | ✅          |             |
| Filter software                                                                                                                  | ✅             | ✅              | ✅               | ✅          |             |
| Run only designated, **observer can run**, queries as live queries against all hosts                                             | ✅             | ✅              | ✅               | ✅          |             |
| Run any query as [live query](https://fleetdm.com/docs/using-fleet/fleet-ui#run-a-query)                                         |               | ✅              | ✅               | ✅          |             |
| Create, edit, and delete only **self authored** queries                                                                          |               |                | ✅               | ✅          | ✅           |
| View all queries\**                                                                                                              | ✅             | ✅              | ✅               | ✅          |             |
| Add, edit, and remove queries from the schedule                                                                                  |               |                | ✅               | ✅          | ✅           |
| View policies                                                                                                                    | ✅             | ✅              | ✅               | ✅          |             |
| View global (inherited) policies                                                                                                 | ✅             | ✅              | ✅               | ✅          |             |
| Filter hosts using policies                                                                                                      | ✅             | ✅              | ✅               | ✅          |             |
| Create, edit, and delete policies                                                                                                |               |                | ✅               | ✅          | ✅           |
| Manage [policy automations](https://fleetdm.com/docs/using-fleet/automations#policy-automations)                                 |               |                |                 | ✅          | ✅           |
| Add and remove team members                                                                                                      |               |                |                 | ✅          | ✅           |
| Edit team name                                                                                                                   |               |                |                 | ✅          | ✅           |
| Create, edit, and delete [team enroll secrets](https://fleetdm.com/docs/using-fleet/rest-api#get-enroll-secrets-for-a-team)      |               |                | ✅               | ✅          |             |
| Read agent options\*                                                                                                             | ✅             | ✅              | ✅               | ✅          |             |
| Edit [agent options](https://fleetdm.com/docs/using-fleet/configuration-files#agent-options)                                     |               |                |                 | ✅          | ✅           |
| Initiate [file carving](https://fleetdm.com/docs/using-fleet/rest-api#file-carving)                                              |               |                | ✅               | ✅          |             |
| View disk encryption key for macOS hosts enrolled in Fleet's MDM                                                                 | ✅             | ✅              | ✅               | ✅          |             |
| Create edit and delete configuration profiles for macOS hosts enrolled in Fleet's MDM                                            |               |                | ✅               | ✅          | ✅           |
| Execute MDM commands on macOS hosts enrolled in Fleet's MDM, and read command results                                            |               |                | ✅               | ✅          |             |
| Execute MDM commands on macOS hosts enrolled in Fleet's MDM                                                                      |               |                | ✅               | ✅          |             |
| View results of MDM commands executed on macOS hosts enrolled in Fleet's MDM                                                     | ✅             | ✅              | ✅               | ✅          |             |
| Edit [team MDM settings](https://fleetdm.com/docs/using-fleet/mdm-macos-settings)                                                |               |                |                 | ✅          | ✅           |
| View/download MDM macOS setup assistant                                                                                          |               |                | ✅              | ✅          |              |
| Edit/upload MDM macOS setup assistant                                                                                            |               |                | ✅              | ✅          |             |

\* Applies only to [Fleet REST API](https://fleetdm.com/docs/using-fleet/rest-api)

\** Team observers can view all queries but the UI and fleetctl only list the ones they can run (**observer can run**).

<meta name="pageOrderInSection" value="900">
