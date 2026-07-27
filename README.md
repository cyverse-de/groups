# groups

A group management API for the CyVerse Discovery Environment. It is the intended
replacement for `iplant-groups` (which is backed by Grouper).

## Overview

- Groups and membership live in the `permissions` schema of the DE database,
  alongside the tables the [`permissions`](https://github.com/cyverse-de/permissions)
  service owns. Co-locating them lets a permission lookup expand a user to their
  groups with a join instead of a call to another service. The schema is defined
  in [`de-database`](https://github.com/cyverse-de/de-database).
- User attributes — names, email addresses, institutions — come from Keycloak,
  read-only. A Keycloak outage degrades display data rather than authorization,
  with one deliberate exception: adding a member who has no subject row yet
  requires Keycloak, because that is where the username is verified. See
  [Member validation](#member-validation).
- A group's identity is structured: a type (`collaborator_list`, `team`,
  `community`, or `system`), an owning user for the types that have one, and a
  short name, unique within that type and owner. Grouper's colon-delimited paths
  are not reproduced.
- Membership may nest: a group can be a member of another group. The transitive
  expansion is materialized in `group_effective_members` and maintained on write,
  so reads never recurse.
- Authorization (which users may manage which groups) is delegated to the
  `permissions` service, where each group is a resource of type `group`. Any
  authenticated user may create a group in their own namespace and becomes its
  owner.
- Group create/update/delete events are published over AMQP for downstream
  re-indexing, matching the messages emitted by `iplant-groups`.

## Table ownership

The groups and permissions services share one schema until they are merged.
Until then:

| Tables | Written by | Read by |
| --- | --- | --- |
| `resource_types`, `resources`, `permissions`, `permission_levels` | permissions | both |
| `group_types`, `groups`, `group_memberships`, `group_effective_members` | groups | both |
| `subjects` rows of type `user` | permissions owns updates and deletes; groups inserts idempotently | both |
| `subjects.user_id` | groups populates on insert; the Grouper importer backfills | both |
| `subjects` rows of type `group` | groups | both |

Groups owns group-typed subject rows outright because deleting a group must
delete its subject row — that cascade is what removes permissions granted *to*
the group.

### Correlating subjects with DE users

`subjects.subject_id` is a bare username while `public.users.username` carries a
domain suffix, so `subjects.user_id` records the DE user a subject corresponds to
and saves every consumer from reconstructing the suffixed form. It is populated
when this service creates a subject row, using `users.suffix` from the
configuration.

The column is **best-effort and nullable**. A subject can be created before the
user has ever logged in to the DE, and some subjects correspond to no DE user at
all. A `NULL` means "not correlated", never "no such user" — treating it as the
latter will silently drop real members. Note also that the permissions service
creates subject rows of its own and does not populate the column, so it is a
lookup shortcut rather than a source of truth.

## Build

```bash
just              # build the bin/groups binary
just test         # run tests
just docs         # regenerate the Swagger docs in docs/
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

The Swagger docs in `docs/` are generated from the handler annotations and
checked in so that local builds work without extra steps. Run `just docs` after
changing an endpoint and commit the result. The container image regenerates them
at build time, and CI fails if the committed copy is out of date. The swag
version is pinned by the `tool` directive in `go.mod`, so the Justfile, the
Dockerfile, and CI all use the same version.

## Endpoints

- `GET /` — service information and backend connectivity (liveness/readiness probe).
- `GET /docs/` — Swagger UI for the API.

Groups:

- `GET /groups?group_type=&owner=&member=&search=&limit=&offset=` — list groups.
  `member` matches groups the user reaches through any depth of nesting.
- `GET /groups/lookup?group_type=&owner=&name=` — resolve a structured identity
  to a group.
- `POST /groups` — create a group (`{group_type, owner, name, display_name, description}`).
  `owner` defaults to the acting user, and only a trusted service account may set
  it to someone else.
- `GET /groups/:id` — get a group by ID.
- `PUT /groups/:id` — update `{name, display_name, description}`. Type and owner
  are immutable: they form the group's identity.
- `DELETE /groups/:id` — delete a group.

Membership. A member is a username, or another group's ID to nest it; nested
members are reported with the `g:gsa` source ID, as Grouper did.

- `GET /groups/:id/members` — list direct members.
- `PUT /groups/:id/members` — replace the full membership (`{members: [...]}`).
- `POST /groups/:id/members` — bulk-add members.
- `POST /groups/:id/members/deleter` — bulk-remove members.
- `PUT /groups/:id/members/:subject` — add a single member.
- `DELETE /groups/:id/members/:subject` — remove a single member.

Bulk operations report a per-member outcome rather than failing the batch. A
member that would create a membership cycle is rejected; adding a group to a
group that already contains it returns a conflict.

### Member validation

Adding a member whose identifier has no `subjects` row would create one. Because
nothing else constrains that identifier, a typo would otherwise become a
permanent subject row and a member who is nobody, with no error. So before
creating a subject row, the service verifies the username in Keycloak:

- Identifiers that already have a subject row are **not** re-validated. They are
  either groups, users vetted when their row was created, or users imported from
  Grouper who may since have left the directory — none of which should block a
  membership change.
- An identifier Keycloak does not resolve is reported as a failed member in bulk
  operations, and returns 400 on `PUT /groups/:id/members/:subject`.
- **Removal is never validated**, so a user who has left the directory can still
  be removed from a group.
- If Keycloak cannot be reached, the add fails with 502 rather than creating
  unverifiable subject rows. This is the one place a directory outage blocks a
  write.

Permissions:

- `GET /groups/:id/permissions` — list the permissions granted on a group.
- `PUT /groups/:id/permissions/:subject-type/:subject-id` — grant a subject a level (`{level}`).
- `DELETE /groups/:id/permissions/:subject-type/:subject-id` — revoke a subject's permission.

Subjects:

- `GET /subjects?search=` — search subjects.
- `POST /subjects/lookup` — look up multiple subjects by ID (`{subject_ids: [...]}`).
- `GET /subjects/:subject-id` — get a subject.
- `GET /subjects/:subject-id/groups?group_type=` — list the groups a subject
  belongs to, including those reached through nesting.

## Events

When a group is created, updated, deleted, or has its membership changed, the
service publishes a message to the configured AMQP topic exchange with a routing
key of `index.group.<id>` and a JSON body of `{"message":"","timestamp_ms":<ms>}`,
matching iplant-groups so existing reindexing consumers keep working. Eventing is
disabled when `amqp.uri` is not configured.

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

Trusted service accounts may be configured to bypass these per-group checks:

- `admin.users` — list of service account usernames that bypass ALL per-group
  permission checks, granting full administrative control over every group:
  reading and modifying membership, updating or deleting any group, and managing
  group permissions. List only fully trusted internal DE services (for example,
  the DE permissions service). Default: empty.
