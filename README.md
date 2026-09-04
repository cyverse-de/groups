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
  owner; `system` groups are restricted to trusted service accounts. See
  [Authorization](#authorization).
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

## Importing from Grouper

`cmd/grouper-import` copies DE group data out of Grouper. It is a standalone
command rather than a migration: it reads a database that does not exist in every
environment, and keeping it out of the migration chain means a rollback removes
structure without destroying group data while Grouper is still the recoverable
source of truth.

```bash
go run ./cmd/grouper-import \
  -grouper-uri "postgres://GrouperSystem@grouper-db/grouper" \
  -grouper-prefix iplant:de:prod \
  -db-uri "postgres://de@de-db/de" \
  -permissions-base http://permissions \
  -phase all -dry-run
```

Credentials may be supplied as `GROUPS_IMPORT_*` environment variables instead of
flags, so they need not appear in a command line that `ps` can read.

The import is **convergent, not merely idempotent**. It is expected to run many
times while Grouper is still taking writes, so membership and grants are
reconciled to Grouper's current state — including removals — rather than
accumulated. Notable consequences:

- **It never deletes groups.** A group that disappears from Grouper is reported,
  not removed: deleting one cascades away its subject row and with it every
  permission granted to the group.
- **It refuses to run once Grouper is no longer authoritative.** `RequireGrouperAuthoritative`
  checks `group_data_source`, and there is no override flag — after cutover, a
  reconcile would delete whatever had been created natively. Setting the marker
  back to `grouper` is the deliberate, attributed act that re-enables it.
- **It takes an advisory lock**, so two runs cannot interleave.
- **`-grouper-prefix` is required.** A wider scope pulls in another environment's
  `de-users`, which collides with this one on `(group_type, owner, name)`.
- **An unparsable name aborts the run.** Every lazy handling of an unknown name
  produces a clean-looking run with groups silently missing.
- Membership is written through this service's own store, so identifier
  validation, DE-user correlation, and closure maintenance have one
  implementation. The Keycloak check does not apply: Grouper is the source of
  truth for the import, and rejecting a member the directory has forgotten would
  silently shrink membership.

Every run prints created / updated / unchanged / added / removed counts, so a
no-op run is visibly a no-op rather than indistinguishable from one that crashed
early. It also verifies the effective-membership closure against Grouper's own
expansion, per group — the strongest check available on the nesting logic.

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

- `GET /` — service information and backend connectivity (readiness probe). It
  answers 503 while the database is unreachable.
- `GET /healthz` — liveness probe. It touches no backend, so a database outage
  does not restart every replica.
- `GET /docs/` — Swagger UI for the API.

Groups:

- `GET /groups?group_type=&owner=&member=&search=&limit=&offset=` — list groups.
  `member` matches groups the user reaches through any depth of nesting.
- `GET /groups/lookup?group_type=&owner=&name=` — resolve a structured identity
  to a group.
- `POST /groups` — create a group
  (`{group_type, owner, name, display_name, description, members_public, joinable}`).
  `owner` defaults to the acting user, and only a trusted service account may set
  it to someone else. `members_public` makes a public group's member list public
  too, and `joinable` lets a user add themselves without approval; both mean
  nothing unless the group also carries a read grant to the public subject.
- `GET /groups/:id` — get a group by ID.
- `PUT /groups/:id` — update
  `{name, display_name, description, members_public, joinable}`. Type and owner
  are immutable: they form the group's identity.
- `DELETE /groups/:id` — delete a group. Deleting one that is already gone
  succeeds.

Membership. A member is a username, or another group's ID to nest it; nested
members are reported with the `g:gsa` source ID, as Grouper did.

- `GET /groups/:id/members?limit=&offset=` — list direct members, with `total`
  reporting the group's whole membership. A group larger than
  `groups.max-member-listing` (1000 by default) is **refused** with a 413 unless
  the caller asks for a page: this response is what group-propagator replaces an
  iRODS group's membership from, so a page mistaken for the whole list would
  delete everyone past it. Page through a large group with `limit` and `offset`.
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
  Keycloak has no bulk lookup by username, so each ID costs a round trip to it
  and the list is capped like a bulk membership request.
- `GET /subjects/:subject-id` — get a subject.
- `GET /subjects/:subject-id/groups?group_type=` — list the groups a subject
  belongs to, including those reached through nesting.
- `GET /subjects/:subject-id/permissions` — list the level the subject holds on
  every group, as `{permissions: [{group_id, level}]}`, with permissions
  inherited through group membership included. A caller may ask about itself; an
  `admin.users` account may ask about anyone. It answers in one request what a
  listing would otherwise assemble one `GET /groups/:id/permissions` at a time.

## Where user attributes come from

Groups and membership live in this database; the display name, email and
institution reported for each member come from elsewhere, selected by
`userinfo.backend`:

- **`portal-conductor`** (default) reads the directory Keycloak federates,
  through `POST /ldap/users/search`. One request resolves a whole listing, and
  the institution comes straight from the directory's `o` attribute.
- **`keycloak`** reads the realm through the admin API. It has no bulk lookup by
  username, so resolving a member listing costs one HTTP call per member, and it
  reports an institution only if the realm carries an LDAP attribute mapper for
  `o` — the DE's realm does not, so under this backend the institution is always
  empty.

Either way a directory outage degrades display data rather than authorization:
members are reported by bare identifier and the listing still succeeds.

## Events

When a group is created, updated, deleted, or has its membership changed, the
service publishes a message to the configured AMQP topic exchange with a routing
key of `index.group.<id>` and a JSON body of `{"message":"","timestamp_ms":<ms>}`,
matching iplant-groups so existing reindexing consumers keep working. Eventing is
disabled when `amqp.uri` is not configured, and a broker that cannot be reached
at startup is logged and treated the same way — events are a reindex hint, so a
broker outage must not take group management down with it. Publishing resumes
only after a restart.

## Authorization

This service is not the OIDC boundary. Following the DE convention, `terrain`
validates the user's token and passes the acting user via a `user` query
parameter over intra-cluster traffic, which this service trusts. Every `/groups`
request must include `?user=<username>`.

Group-management rights are stored in the permissions service as grants on the
`group` resource type. The service enforces this matrix before each operation:

| Operation | Required |
| --- | --- |
| create a `collaborator_list`, `team`, or `community` | any authenticated user (the creator is granted `own`) |
| create a `system` group | a trusted service account (`admin.users`) |
| get a group / list members | `read` or membership in the group |
| update a group / manage members / manage group permissions | `admin` |
| delete a group | `admin` |

Mutating operations ask for `admin` rather than `write` or `own`. The DE's level
precedence is `own` = 0, `write` = 1, `admin` = 2, `read` = 3, so `admin` sits
*below* `write`: requiring `write` would lock out every co-admin — exactly the
subjects Grouper's `admins` privilege produced and the ones terrain lists as a
team's admins. `own` is stronger and still passes.

A `system` group (`de-users`, `workshop-users`) is deployment-wide and has no
owning namespace to confine who may create it, so only a trusted service account
may. A `community` has no owner either, but creating one is open to any
authenticated user by design.

The creator of a group is granted `own` automatically; if that grant fails the
group is rolled back. Deleting a group also removes its permissions resource,
which is why a repeated `DELETE` short-circuits to success before the permission
check: the grant it would be checked against went with the group.

Trusted service accounts may be configured to bypass these per-group checks:

- `admin.users` — list of service account usernames that bypass ALL per-group
  permission checks, granting full administrative control over every group:
  reading and modifying membership, updating or deleting any group, and managing
  group permissions. List only fully trusted internal DE services (for example,
  the DE permissions service). Default: empty.
