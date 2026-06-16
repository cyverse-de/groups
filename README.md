# groups

A group management API for the CyVerse Discovery Environment, using Keycloak
to store and manage groups. It is the intended replacement for `iplant-groups`
(which is backed by Grouper).

## Overview

- Groups, group membership, and user (subject) lookups are served from a Keycloak
  realm via the Keycloak Admin REST API.
- Authorization (which users may manage which groups) is delegated to the DE
  [`permissions`](https://github.com/cyverse-de/permissions) service, where each
  group is a resource of type `group`. Any authenticated user may create a group
  and becomes its owner.
- Group create/update/delete events are published over AMQP for downstream
  re-indexing, matching the messages emitted by `iplant-groups`.

## Build

```bash
just              # build the bin/groups binary
just test         # run tests
just build-image  # build the container image
go build ./...    # build all packages
```

## Run

```bash
./bin/groups --config /etc/iplant/de/groups.yml --port 60000
```

See [`configs/default.yml`](configs/default.yml) for configuration options.
Environment variables are read with the `GROUPS_` prefix (e.g.
`GROUPS_KEYCLOAK_CLIENT_SECRET`).

## Endpoints

- `GET /` — service information and Keycloak connectivity (liveness/readiness probe).

Groups:

- `GET /groups?search=` — search groups.
- `POST /groups` — create a group (`{name, description, display_extension}`).
- `GET /groups/:id` — get a group by UUID.
- `PUT /groups/:id` — update a group.
- `DELETE /groups/:id` — delete a group.

Membership:

- `GET /groups/:id/members` — list members.
- `PUT /groups/:id/members` — replace the full membership (`{members: [...]}`).
- `POST /groups/:id/members` — bulk-add members.
- `POST /groups/:id/members/deleter` — bulk-remove members.
- `PUT /groups/:id/members/:subject` — add a single member.
- `DELETE /groups/:id/members/:subject` — remove a single member.

Permissions:

- `GET /groups/:id/permissions` — list the permissions granted on a group.
- `PUT /groups/:id/permissions/:subject-type/:subject-id` — grant a subject a level (`{level}`).
- `DELETE /groups/:id/permissions/:subject-type/:subject-id` — revoke a subject's permission.

Subjects:

- `GET /subjects?search=` — search subjects.
- `POST /subjects/lookup` — look up multiple subjects by ID (`{subject_ids: [...]}`).
- `GET /subjects/:subject-id` — get a subject.
- `GET /subjects/:subject-id/groups` — list the groups a subject belongs to.

AMQP change events are added in a subsequent milestone; see the implementation
plan.

## Authorization

This service is not the OIDC boundary. Following the DE convention, `terrain`
validates the user's token and passes the acting user via a `user` query
parameter over intra-cluster traffic, which this service trusts. Every `/groups`
request must include `?user=<username>`.

Group-management rights are stored in the permissions service as grants on the
`group` resource type. The service enforces this matrix before each operation:

| Operation | Required |
| --- | --- |
| create a group | any authenticated user (the creator is granted `own`) |
| get a group / list members | `read` or membership in the group |
| update a group / manage members | `write` |
| delete a group | `own` |

The creator of a group is granted `own` automatically; if that grant fails the
group is rolled back. Deleting a group also removes its permissions resource.
