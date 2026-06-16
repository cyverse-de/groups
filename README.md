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

Authorization, the `/groups/:id/permissions` endpoints, subject lookups, and
AMQP change events are added in subsequent milestones; see the implementation
plan.
