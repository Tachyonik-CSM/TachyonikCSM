# ActionGenerator — 6. Operation

Status: **Accepted** · Area code: `OPS`

What an operator can rely on when running ActionGenerator: how it is
configured, what it does at startup and shutdown, how its version is
managed, and how it reports that it is alive. How to build, configure and run
it step by step is in the [module README](../README.md).

## 1. Configuration

**AG-OPS-01** Every setting MUST have a default that runs a local development
setup. A setting MUST be taken from the environment before the configuration
file, and from the file before the default. The file is `./config.yaml`, or
the one `ACTIONGENERATOR_CONFIG` names.
*Reason: a container sets what differs per deployment in its environment,
without a file of its own.*

**AG-OPS-02** `config.yaml.example` MUST list every setting, with its default,
its environment variable and what it does.
*Reason: an operator learns the settings from it. A setting missing there is
one nobody sets. (Project rule, see CLAUDE.md.)*

**AG-OPS-03** The execution budget of a routine call (AG-SEC-09) MUST be a
setting, 5 seconds by default. Only the environment MAY switch it off, with
`0`.
*Reason: an operator must be able to raise it for a slow installation without
a rebuild. In the file a `0` cannot be told from an absent key, so the file
cannot switch off a guard that protects the daemon.*

**AG-OPS-04** A configuration file that exists MUST be used in full or not at
all: one that cannot be read, cannot be parsed, or contains a setting
ActionGenerator does not know MUST stop the daemon at startup, with an error
on standard error naming the file and the fault, and exit code 1. A missing
file MUST NOT: the defaults and the environment then apply (AG-OPS-01).
*Reason: running on defaults it was not meant to use — a developer's service
URLs, an empty service key — is worse than not starting, and a misspelt
setting silently keeps its default (decided 2026-11-13).* Values:
AG-OPS-15.

**AG-OPS-15** A setting whose value ActionGenerator cannot use MUST stop the
daemon at startup, whether it comes from the environment or the file. The
error goes to standard error with exit code 1, as in AG-OPS-04, and names the
setting, where it came from (the variable, or the file and key) and the values
it accepts. A secret's value MUST NOT be shown. Usable values are:

- the AI timeout and the heartbeat interval: a whole number of seconds above 0;
- the JS execution budget: a whole number of seconds, where `0` switches the
  guard off in the environment only (AG-OPS-03);
- the log switches: `true`, `false`, `1` or `0`, in any case;
- the log level: DEBUG, INFO, WARN or ERROR, in any case.

An empty variable counts as not set. In the file a `0` counts as an absent key
(AG-OPS-03); a negative number is refused.
*Reason: the environment is where a container configures the daemon
(AG-OPS-01), so a typo there is as likely as one in the file and must not be
treated more mildly. A value dropped without a word runs the daemon on a
setting nobody chose. For the log switches it was worse than a default:
`LOG_TO_FILE=yes` switched the file log off. This is the same rule as AG-OPS-04
and AssetManager's AM-OPS-03 (decided 2026-11-18).*

**AG-OPS-05** Which AI generates routines, and with which system prompt, MUST
NOT be configuration of this module: they are read from AIManager (AG-GEN-04).
*Reason: they are set by an administrator in the WebUI and change at run time.*

**AG-OPS-06** At startup, ActionGenerator MUST log the effective settings: the
URLs of the services it uses, the AI and execution timeouts, and the logging.
Secrets MUST NOT be logged (AG-SEC-02).
*Reason: a wrong URL or a timeout is the usual fault, and the log is where an
operator looks first.*

## 2. Startup and shutdown

**AG-OPS-07** At startup, ActionGenerator MUST, in this order:
1. read the configuration and set up logging;
2. read the module's AI setting and system prompt from AIManager;
3. read the platform API catalogue and build the bridge (AG-IF-23);
4. load the rules and their active routines (AG-GEN-17);
5. connect to AIManager for rule changes, evaluation requests, setting
   changes and catalogue changes;
6. evaluate every rule once (AG-EVAL-01);
7. connect to AssetManager, SystemManager, ResourceManager and ActionManager;
8. start the heartbeat (AG-OPS-10).

A service that cannot be reached MUST NOT stop the daemon: it MUST be logged,
and its connection retried until it succeeds.
*Reason: the platform's services start in any order, and restart
independently. A daemon that gives up because AIManager was a second late
must be restarted by hand.*

**AG-OPS-08** When the rules cannot be loaded at startup, ActionGenerator MUST
load them as soon as AIManager can be reached. It MUST also load them again
after every reconnect to AIManager.
*Reason: otherwise no rule runs until someone happens to change a rule or
import a feed, and no advice is given or withdrawn meanwhile. A connection that
was down has missed the rule changes announced meanwhile. ActionGenerator tries
again every 30 s and on every reconnect, then evaluates every rule.*

**AG-OPS-09** On SIGINT or SIGTERM, ActionGenerator MUST stop reacting to
messages, let a running evaluation finish, close its connections and exit.
A running pass MUST stop after the rule and user it is evaluating; the rest is
evaluated by the pass at the next start. An evaluation MUST NOT start after
the signal; evaluation requests still waiting (AG-EVAL-13) MUST be dropped
and their number logged. The wait MUST be bounded, and a unit still running
when it ends MUST be logged by name. A generation in progress is not waited for; its
request is cleared at the next start (AG-GEN-02).
*Reason: an evaluation stopped between creating and withdrawing leaves a
half-applied set of actions until the next one. A container stop must not do
that. A whole pass over every user and rule can take longer than a container
is given to stop, and an AI call takes minutes; the deployment gives the
daemon 30 seconds (`stop_grace_period`), of which the wait takes at most 20.*

## 3. Liveness

**AG-OPS-10** ActionGenerator MUST send a heartbeat to SystemManager under the
name `ActionGenerator`, every `heartbeat.interval_seconds` (10 by default).
*Reason: it has no HTTP endpoint for `/health` (AG-PUR-08). The heartbeat is
how the platform's system status knows it runs.*

## 4. Version

**AG-OPS-11** The version of an ActionGenerator binary MUST come from the git
tag `actiongenerator/X.Y.Z` it was built from. It MUST be shown by
`actiongenerator version` (also `--version`, `-v`) without reading the
configuration, and logged at startup. A build from an untagged or changed
tree MUST say so in its version; one with no tag at all reports `0.0.0-dev`.
*Reason: an operator reporting a fault must be able to name exactly what runs,
also on a host where the configuration or the log path is not usable.*

**AG-OPS-12** The module version MUST NOT change behaviour: nothing compares
it and nothing is stamped with it.
*Reason: unlike SourceAnalyser's, a new ActionGenerator release must not cause
anything to be regenerated. Routines carry versions of their own (AG-GEN-14).*

## 5. Logging

**AG-OPS-13** The log MUST support the levels DEBUG, INFO, WARN and ERROR, to
the console, to a file, or both. It MUST record every failed call to a service,
every routine that failed to load or run (with the rule's name), every action
created and every withdrawal with its count.
*Reason: "why was this action raised, or not, or withdrawn" is diagnosed from
the log. DEBUG adds the routine sources and is too much for production.*

**AG-OPS-14** `log.to_console` and `log.to_file` MUST be taken from the file as
written, `false` included.
*Reason: YAML cannot tell an absent boolean from `false`. Taking it as written
lets the file switch a sink off; the example states both explicitly.*

## 6. Known deviations

No deviations are known.

## 7. Open questions

None is open.
