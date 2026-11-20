# AssetManager — 5. Operation

Status: **Accepted** · Area code: `OPS`

What an operator can rely on when running AssetManager: how it is
configured, what it does at startup and shutdown, how its database schema
and its own version are managed, and how it reports its health. How to
build, configure and run it step by step is in the
[module README](../README.md).

## 1. Configuration

**AM-OPS-01** Every setting MUST have a default that runs a local
development setup. A setting MUST be taken from the environment
(`ASSETMANAGER_*`) before the configuration file, and from the file before the
default.
*Reason: a container sets what differs per deployment in its environment,
without a file of its own.*

**AM-OPS-02** `config.yaml.example` MUST list every setting, with its default
and what it does.
*Reason: an operator learns the settings from it. A setting missing there is
one nobody sets. (Project rule, see CLAUDE.md.)*

**AM-OPS-03** The instance mode, `appliance` (one shared workspace, an
instance-wide licence) or `saas`, MUST be SystemManager's: at startup
AssetManager MUST ask SystemManager (`GET /api/internal/workspace`), and MUST
serve nothing until it has the answer, asking again every 2 seconds and
logging every 30 seconds that it waits. An answer naming neither mode, in any
case, MUST stop the service, naming the value. AssetManager's own
`instance.mode` and `ASSETMANAGER_INSTANCE_MODE` MUST NOT be used; for one
release they are still accepted, with a warning that names a disagreement with
SystemManager, and then removed.
*Reason: tenancy (AM-SEC-08) and the limits (AM-DATA-33, -34) depend on it,
and a setup never runs in two modes. Configured in each service, the mode was
missed in one on the production SaaS, which then ran it as an appliance
(decided 2026-11-20; the check of the value, decided 2026-11-13, stays).*

**AM-OPS-16** A configuration file that exists MUST be used in full or not at
all: one that cannot be read, cannot be parsed, or contains a setting
AssetManager does not know MUST stop the service at startup with an error
naming the file and the fault. A missing file MUST NOT: the defaults and the
environment then apply (AM-OPS-01).
*Reason: running on defaults it was not meant to use — the placeholder token
secret, appliance mode, a developer's database path — is worse than not
starting, and a misspelt setting silently keeps its default (decided
2026-11-13). An older AssetManager therefore refuses a file that already has
a newer setting; on a rollback, the file is rolled back with it.* Values:
AM-OPS-03, AM-OPS-19.

**AM-OPS-19** A setting whose value AssetManager cannot use MUST stop the
service at startup, whether it comes from the environment or the file. The
error goes to standard error with exit code 1, names the setting, where it
came from (the variable, or the file and key) and the values it accepts, and
lists every such setting at once. A secret's value MUST NOT be shown. Usable
values are:

- the log switches (`ASSETMANAGER_LOG_TO_CONSOLE`, `ASSETMANAGER_LOG_TO_FILE`):
  `true`, `false`, `1` or `0`, in any case;
- the server port (`ASSETMANAGER_PORT`, `server.port`): a whole number from 1
  to 65535;
- the log level: DEBUG, INFO, WARN or ERROR, in any case.

An empty variable counts as not set. In the file a `0` counts as an absent key.
*Reason: the environment is where a container configures the service
(AM-OPS-01), so a typo there is as likely as one in the file and must not be
treated more mildly. A value dropped without a word runs the service on a
setting nobody chose. For the log switches it was worse than a default:
`LOG_TO_FILE=yes` switched the file log off, and an unknown log level fell
back to INFO. This is the same rule as AM-OPS-03 and AM-OPS-16 (decided
2026-11-18).*

**AM-OPS-04** At startup, AssetManager MUST log the effective settings that
explain the usual faults: the database path, the address it listens on, the
logging, and the WebSocket origin allow-list. Secrets MUST NOT be logged.
*Reason: a browser whose origin is missing from the list shows the service as
disconnected. The log is where an operator looks first. Secrets: AM-SEC-07.*

## 2. Startup

**AM-OPS-05** At startup, AssetManager MUST do these in this order, before it
reports ready:
1. open the database;
2. migrate the schema (§3);
3. report assets of unknown type (AM-DATA-04);
4. start serving.

A failure in steps 1 or 2 MUST stop the service with an error.
*Reason: serving from a database in an unknown state corrupts it further. A
service that stops says so where a running one would not.*

**AM-OPS-06** After it starts serving, AssetManager MUST recompute every asset
(AM-DATA-22), and then send every user's scores (AM-DATA-43). It MUST NOT
wait for this before serving.
*Reason: this repairs whatever an older version or an interrupted change left
behind. On a large database it takes a while, and the platform must not wait
for it.*

**AM-OPS-18** After it starts serving, AssetManager MUST register its API
document — `spec/openapi.yaml`, embedded in the binary — with AIManager
(`aimanager.url`), retrying in the background until AIManager takes it. It
MUST NOT wait for this before serving. When `aimanager.url` is empty, it MUST
say in the log that the document is not registered.
*Reason: the platform API catalogue must describe the AssetManager that runs
(PA-REG-01). AIManager may start later than AssetManager.*

**AM-OPS-07** On SIGINT or SIGTERM, AssetManager MUST stop accepting requests,
and give running ones up to 10 seconds to finish. It MUST write out the limit
refusals still being combined (AM-DATA-36) before it exits.
*Reason: a container stop must not cut an import off mid-request, nor lose
the audit entry of a refusal.*

## 3. The database schema

**AM-OPS-08** The database MUST record its schema version, and AssetManager
MUST migrate it, step by step, to the version it was built for.
*Reason: an update must not need a manual step.*

**AM-OPS-09** AssetManager MUST refuse to start on a database whose schema is
newer than it supports, and say how to recover: restore the snapshot taken
before the update, or install the newer version.
*Reason: an older binary writing to a newer schema damages it silently. This
is what happens after a rollback of the binary without the data.*

**AM-OPS-10** A migration MUST be safe to run again after it failed partway.
*Reason: a migration interrupted by a crash or a full disk runs again at the
next start.*

**AM-OPS-11** Within a release, migrations MUST only add. A column or table
that is no longer used MUST be dropped no earlier than the following release.
*Reason: rolling back the binary by one release must keep working on the
migrated database. (Project rule, migration discipline.)*

## 4. Version

**AM-OPS-12** The version of an AssetManager binary MUST come from the git tag
`assetmanager/X.Y.Z` it was built from. It MUST be shown by
`assetmanager version` and logged at startup. A build from an untagged commit
MUST say so in its version.
*Reason: an operator reporting a fault must be able to name exactly what runs.*

## 5. Health and metrics

**AM-OPS-13** `/health` MUST answer 200 while the process runs. `/ready` MUST
answer 503 until startup (AM-OPS-05) is complete, and 200 after.
*Reason: the container runtime restarts what is not alive, and routes traffic
only to what is ready.*

**AM-OPS-14** `/metrics` MUST report, in the Prometheus format:
- the requests, by method, route template and status;
- their durations;
- how many requests are in flight.

It MUST NOT report record data, names or user ids.
*Reason: route templates, not paths, keep the number of series bounded. Every
authenticated user may read the metrics (AM-OPS-17), so they must not tell
anyone anything about the records.*

**AM-OPS-17** `/metrics` MUST answer only an authenticated caller: a service
with the internal service key, or a user with a session, an anonymous one
included, checked as for the API (AM-SEC-02 to -04). Otherwise it MUST be
refused as the API refuses. `/health` and `/ready` MUST stay open.
*Reason: the metrics show which endpoints are called how often, which is not
for anyone who can reach the port. The probes say nothing but "running" and
"ready", and the container runtime asks them without a key (decided
2026-11-13).* Prometheus sends the service key (deployment/observability).

**AM-OPS-15** The log MUST record every refused or failed request with its
reason, at warning or error level. It MUST support the levels DEBUG, INFO,
WARN and ERROR.
*Reason: a refused import is diagnosed from the log. DEBUG is for
troubleshooting, and is too much for production.*

## 6. Known deviations

None known.

## 7. Open questions

None open.
