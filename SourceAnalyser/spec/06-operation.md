# SourceAnalyser — 6. Operation

Status: **Draft** · Area code: `OPS`

What an operator can rely on when running SourceAnalyser: how it is
configured, what it does at startup and shutdown, how its version is managed
— which, unlike other modules', changes what it does — and how it reports that
it is alive. How to build, configure and run it step by step is in the
[module README](../README.md).

## 1. Configuration

**SA-OPS-01** Every setting MUST have a default that runs a local development
setup. A setting MUST be taken from the environment before the configuration
file, and from the file before the default. The file is `./config.yaml`, or
the one `SOURCEANALYSER_CONFIG` names.
*Reason: a container sets what differs per deployment in its environment,
without a file of its own. As AG-OPS-01.*

**SA-OPS-02** `config.yaml.example` MUST list every setting, with its default,
its environment variable and what it does.
*Reason: an operator learns the settings from it. A setting missing there is
one nobody sets. (Project rule, see CLAUDE.md.)*

**SA-OPS-03** The execution budget of a routine call (SA-SEC-08) MUST be a
setting, 5 seconds by default. Only the environment MAY switch it off, with
`0`.
*Reason: an operator must be able to raise it for a slow installation without
a rebuild. In the file a `0` cannot be told from an absent key, so the file
cannot switch off a guard that protects the daemon. As AG-OPS-03.*

**SA-OPS-16** The content window MUST be two settings: the bytes read of a
source (`max_content_bytes`, 64 KiB by default) and the bytes read of a PDF
(`max_pdf_bytes`, 32 MiB by default).
*Reason: they bound the memory of one analysis (SA-ANA-06, SA-ANA-07). An
installation that analyses large PDFs must be able to size it.*

**SA-OPS-04** A configuration file that exists MUST be used in full or not at
all: one that cannot be read, cannot be parsed, or contains a setting
SourceAnalyser does not know MUST stop the daemon at startup, with an error
on standard error naming the file and the fault, and exit code 1. A missing
file MUST NOT: the defaults and the environment then apply (SA-OPS-01).
*Reason: running on defaults it was not meant to use — a developer's service
URLs, an empty service key — is worse than not starting, and a misspelt
setting silently keeps its default. As AG-OPS-04.* Values: SA-OPS-15.

**SA-OPS-15** A setting whose value SourceAnalyser cannot use MUST stop the
daemon at startup, whether it comes from the environment or the file. The
error goes to standard error with exit code 1, as in SA-OPS-04, and names
every such setting, where it came from and the values it accepts. A secret's
value MUST NOT be shown. Usable values are:

- the sweep interval and the heartbeat interval: a whole number of seconds
  above 0;
- the JS execution budget: a whole number of seconds above 0, where `0`
  switches the guard off in the environment only (SA-OPS-03);
- the content window: a whole number of bytes above 0;
- the log switches: `true`, `false`, `1` or `0`, in any case;
- the log level: DEBUG, INFO, WARN or ERROR, in any case.

An empty variable counts as not set. In the file a `0` counts as an absent key
(SA-OPS-03); a negative number is refused.
*Reason: a value dropped without a word runs the daemon on a setting nobody
chose; `SOURCEANALYSER_LOG_TO_FILE=yes` once switched the file log off, and a
negative budget in the file switched the guard off. As AG-OPS-15.*

**SA-OPS-17** SourceAnalyser MUST NOT start without a ResourceManager URL and
internal service key, and the error MUST name the settings. For one release, a
SystemManager key that is not set MUST be taken from ResourceManager's, with a
warning; the release after, it is required.
*Reason: without ResourceManager there is no work, and every call would be
refused. Older installations configured one key for both. A key that silently
stands in for another hides a missing setting; every other module gives each
target its own key (decided 2026-11-20).*

**SA-OPS-19** The services' settings MUST follow AssetManager's layout: a file
section per service — `resourcemanager:`, `systemmanager:`, `aimanager:` — with
`url` and `internal_service_key`, and the variables
`SOURCEANALYSER_<SERVICE>_URL` and `SOURCEANALYSER_<SERVICE>_INTERNAL_SERVICE_KEY`.
*Reason: one layout across the modules, so an operator configuring one knows
the others. A variable without the module's prefix (`RESOURCEMANAGER_URL`)
says nothing about who reads it, and on a host running several modules from
one environment it reaches all of them (decided 2026-11-20, OPS question 1,
`docs/plans/sourceanalyser-service-sections.md`).*

**SA-OPS-20** For one release the names before SA-OPS-19 MUST still be read,
each with a warning at startup naming its replacement: the section
`ai_manager:`, and the variables `RESOURCEMANAGER_URL`,
`RESOURCEMANAGER_API_BASE_URL`, `RESOURCEMANAGER_INTERNAL_SERVICE_KEY`,
`SYSTEMMANAGER_URL`, `SYSTEMMANAGER_INTERNAL_SERVICE_KEY`,
`SOURCEANALYSER_AI_MANAGER_URL` and `AIMANAGER_INTERNAL_SERVICE_KEY`. When an old
and a new name are both set, the new one MUST be used and the warning MUST say
the old one is ignored. The release after, `ai_manager:` MUST be refused as
unknown (SA-OPS-04) and the old variables MUST NOT be read.
*Reason: every installed configuration file has `ai_manager:`, and strict
configuration would otherwise stop every installation at the upgrade.*

**SA-OPS-18** `analyzer.poll_interval` and `SOURCEANALYSER_POLL_INTERVAL` MUST
be accepted for one release, their value ignored, with a warning at startup
naming `sweep_interval_seconds` and the interval in effect. The release after,
the key MUST be refused as unknown (SA-OPS-04).
*Reason: every installed configuration carries `poll_interval: 5` from the
template. Refusing it would stop every installation at the upgrade; reading it
as the sweep interval would keep the five-second listing this change removes
(decided 2026-11-20).*

**SA-OPS-05** Which AI generates routines, and with which system prompt, MUST
NOT be configuration of this module: they are read from AIManager (SA-GEN-05).
*Reason: they are set by an administrator in the WebUI and change at run time.
As AG-OPS-05.*

**SA-OPS-06** At startup, SourceAnalyser MUST log its version and the
effective settings: the URLs of the services it uses, the sweep interval, the
execution budget, the content window and the logging. Secrets MUST NOT be
logged (SA-SEC-02).
It MUST log the warnings about settings that are accepted but no longer
apply (SA-OPS-18).
*Reason: a wrong URL, a budget or a window too small for the installation's
files is the usual fault, and the log is where an operator looks first.*

## 2. Startup and shutdown

**SA-OPS-07** At startup, SourceAnalyser MUST, in this order:
1. read the configuration and set up logging;
2. read the module's AI setting and system prompt from AIManager;
3. load the rules and their active routines (SA-GEN-16), and clear the
   generation requests an earlier run left standing (SA-GEN-03);
4. connect to AIManager for rule and setting changes;
5. start the heartbeat (SA-OPS-10);
6. connect to ResourceManager for sources announced as waiting (SA-IF-10), and
   start the worker with its first sweep (SA-ANA-19).

A service that cannot be reached MUST NOT stop the daemon: it MUST be logged,
and its connection retried until it succeeds.
*Reason: the platform's services start in any order, and restart
independently. A daemon that gives up because AIManager was a second late
must be restarted by hand. As AG-OPS-07.*

**SA-OPS-08** When the rules cannot be loaded at startup, SourceAnalyser MUST
load them as soon as AIManager can be reached. It MUST also load them again
after every reconnect to AIManager.
*Reason: otherwise every source is `Unsupported` until someone happens to
change a rule or import a feed. A connection that was down has missed the
rule changes announced meanwhile. SourceAnalyser tries again every 30 s and
on every reconnect. As AG-OPS-08.*

**SA-OPS-09** On SIGINT or SIGTERM, SourceAnalyser MUST finish the source it is
analysing, close its connections and exit. The analysis of a further source,
and a sweep, MUST NOT start after the signal; the queue is dropped
(SA-ANA-20). A generation in progress is
not waited for; its request is cleared at the next start (SA-GEN-03).
*Reason: a source stopped between being set to `Analysing` and its result
stays `Analysing` — which the next start picks up again — but its result must
not be lost when it is about to be written. A queue of many sources can take
longer than a container is given to stop (30 seconds), and an AI call takes
minutes.*

## 3. Liveness

**SA-OPS-10** SourceAnalyser MUST send a heartbeat to SystemManager under the
name `SourceAnalyser`, every `heartbeat.interval_seconds` (10 by default).
*Reason: it has no HTTP endpoint for `/health` (SA-PUR-07). The heartbeat is
how the platform's system status knows it runs. As AG-OPS-10.*

## 4. Version

**SA-OPS-11** The version of a SourceAnalyser binary MUST come from the git
tag `sourceanalyser/X.Y.Z` it was built from, in all three build paths (the
module's `make build`, the repository's `make go-build`, the container image).
It MUST be shown by `sourceanalyser version` (also `--version`, `-v`) without
reading the configuration, and logged at startup. A build from an untagged or
changed tree MUST say so in its version; one with no tag at all reports
`0.0.0-dev`.
*Reason: an operator reporting a fault must be able to name exactly what runs,
also on a host where the configuration or the log path is not usable. A build
path that does not inject it ships `0.0.0-dev`, which SA-OPS-12 treats as
older than every release.*

**SA-OPS-12** Unlike other modules', the version is data: it is stamped on
every source analysed to the end (SA-ANA-14), and a source left `Unsupported`
by an older version is analysed again (SA-ANA-03). The version MUST therefore
be raised whenever a change could make SourceAnalyser recognise a source it
left `Unsupported` before — not only when something visible changes. Versions
MUST be compared numerically, part by part, a missing part counting as 0;
`0.0.0-dev` MUST compare as `0.0.0`.
*Reason: without a new version, those sources are never looked at again. A
development build must never revisit a release's sources, while a release
must revisit a development build's, whose verdict is provisional.*

## 5. Logging

**SA-OPS-13** The log MUST support the levels DEBUG, INFO, WARN and ERROR, to
the console, to a file, or both. It MUST record every failed call to a
service, every routine that failed to load or run (with the rule's name),
every source analysed with its result and the rule that decided it, and every
source analysed again with the reason.
*Reason: "why was this file recognised as that, or not at all" is diagnosed
from the log. DEBUG adds each rule's verdict and a preview of the content
(SA-SEC-07), and is too much for production.*

**SA-OPS-14** `log.to_console` and `log.to_file` MUST be taken from the file as
written, `false` included.
*Reason: YAML cannot tell an absent boolean from `false`. Taking it as written
lets the file switch a sink off; the example states both explicitly. As
AG-OPS-14.*

## 6. Known deviations

None known.

## 7. Open questions

None open.
