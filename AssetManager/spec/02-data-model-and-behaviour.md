# AssetManager — 2. Data model and behaviour

Status: **Accepted** · Area code: `DATA`

What must always be true of AssetManager's records, whoever writes them and
through whichever endpoint. The terms are those of
[1. Purpose and scope](01-purpose-and-scope.md#2-terms). How the records are
reached over HTTP and the WebSocket is in [3. API](03-api.md); who may reach which is in
[4. Security and tenancy](04-security-and-tenancy.md).

## 1. The records

| Record | Holds | Entered | Derived (§5) |
|---|---|---|---|
| **Asset** | owner, name, type | yes | severity, score, last seen, last scanned, scan coverage |
| **Link** | asset, source id and label, last seen, scan coverage reported by that source, whether it counts as a sighting | by imports | — |
| **Vulnerability** | owner, name, host, port, severity, source id and label, last seen | yes | — |
| **Detection** | owner, name, host, port, source id and label, last seen | yes | — |

An asset's scan coverage is entered only through its links (§7). It is
derived only in the sense that the links' values are combined.

## 2. Assets

**AM-DATA-01** An asset MUST be identified by its owner, name and type. There
MUST be at most one asset per identity. Reporting an existing asset again
MUST update that asset and its link, not create a second one.
*Reason: one host reported by three scans is one host. A duplicate would be
counted three times against the limits and the scores.*

**AM-DATA-02** The asset types MUST be a fixed set, for now `Host` only. An
asset of any other type MUST be refused on create.
A case variant (`host`) MUST be accepted and stored as the canonical spelling.
*Reason: last scanned, the host-asset limit, scan coverage and the dashboard
counts are defined for hosts only. A type nobody defined them for would be
stored but silently treated as nothing. Imports have sent `host`. (Decided
2026-11-03, formerly PUR question 1.)*

**AM-DATA-03** Adding a type MUST be a change to this specification that says
what last scanned, the limits and scan coverage mean for it.
*Reason: the same as AM-DATA-02. The code's list of types is where the change
lands, not where it is decided.*

**AM-DATA-04** Assets stored with a type outside the set (written before the
set was fixed) MUST be left unchanged and reported at startup, with their
count per type.
*Reason: nothing should rewrite or delete records on its own. The operator
needs to know they exist; one who wants them gone deletes their source
(AM-PUR-20).*

**AM-DATA-05** A host asset SHOULD be named by its address.
*Reason: scan coverage tables match hosts by address (AM-DATA-31). A host
named by its hostname is never covered by a network or range. Nothing
enforces this, because imports name hosts as their sources do.*

**AM-DATA-06** No field of an asset MUST be writable. Its name and type are set
when an import creates it and stay with it; its derived values are computed.
*Reason: the derived values are recomputed from the evidence and would be
overwritten. The name is what findings are joined on (AM-DATA-13), so changing
it would silently detach them. The type is what the source said the record
is, and the sources rule the records (AM-PUR-20); with one type there is also
nothing to change it to (decided 2026-11-19: the type change, offered until
then, was removed).*

**AM-DATA-07** Creating an asset MUST name its source by its id; a create
without one MUST be refused with 400.
*Reason: the sources rule the records (AM-PUR-20). Only a source id lets a
link be removed with its source and checked against ResourceManager
(AM-DATA-46).*

## 3. Links

**AM-DATA-08** An asset MUST have at most one link per source. A source is
identified by its id.
*Reason: AM-PUR-04 removes exactly what one source contributed, so each
source's contribution must be held separately.*

**AM-DATA-09** When a source reports an asset again, its link MUST take the
new label and be marked as touched by this import. The link's last seen MUST
only move forward, with one exception: a link that was coverage-only becomes
a sighting, dated by this report.
*Reason: re-importing an older file must not turn the clock back. The touch,
not the date, is what tells a re-import which links it no longer produces
(AM-DATA-28).*

**AM-DATA-10** A link made by a scan coverage table MUST NOT count as a
sighting of the asset, unless the same source has also listed the asset.
*Reason: a schedule saying a host would be scanned does not say it was seen.
Counting it would keep a vanished host looking current.*

## 4. Findings

**AM-DATA-11** A finding MUST name its source as an asset's link does, by
`sourceId` and `sourceLabel`; a create without a source id MUST be refused
with 400 (AM-DATA-07).
*Reason: one way to name a source, not two that must be kept in step.
(Decided 2026-11-03, formerly PUR question 2.)*

**AM-DATA-12** A source's findings MUST be found by the source id. The label
MUST be used for display only.
*Reason: a renamed file is still the same source. Matching by name would
orphan its findings and leave them undeletable by source.*

**AM-DATA-13** A finding MUST refer to the assets of the same owner whose name
equals its host exactly. Nothing else ties a finding to an asset. Every
finding MUST have the host asset of its owner: creating a finding MUST
report that host asset as creating it through the API would — created when
missing, linked to the finding's source and touched by this import
(AM-DATA-08, -09).
*Reason: imports deliver findings and hosts separately and in any order, and
a routine may deliver findings only. A finding is evidence of its host. With
the link, deleting a source (AM-PUR-04) and a re-import (AM-DATA-28) remove
an asset only together with the last finding on it, as they remove an asset
only with its last link (decided 2026-11-19, formerly PUR question 3).*

**AM-DATA-47** A finding MUST be identified by its owner, its source id, its
host, its port and its name, compared exactly. Creating a finding whose
identity is on record MUST report that record again instead of storing a
copy: it keeps its id, takes the new source label and, for a vulnerability,
the new severity; its last seen MUST only move forward; it MUST be marked as
touched by this import (AM-DATA-28). The answer MUST be 200 for a finding
reported again and 201 for a new one.
*Reason: a scanner may report the same check several times — OpenVAS once
per virtual hostname of a host — and a re-import reports every finding
again. Stored as copies, they inflated the lists, the counts and the limits,
and a re-import replaced every finding with a new id (decided 2026-11-19,
formerly DATA question 2). Findings of different sources stay apart: each
source owns its own (AM-DATA-24).*

**AM-DATA-14** A vulnerability's severity MUST be an integer from 0 to 100. A
value outside that range MUST be refused.
*Reason: an asset's score is 100 minus its severity, and the severity bands
(AM-DATA-40) assume that range.*

**AM-DATA-15** A detection MUST have no severity. It MUST count as evidence
about its asset: an asset with detections but no vulnerabilities is not
unmanaged, and scores 100.
*Reason: a detection records that something was observed on a host without
being a weakness, so the host is known and nothing is wrong with it.
(Decided 2026-11-03, formerly PUR question 7.)*

**AM-DATA-16** *Transitional, until the release after schema version 6.* A
create request that has neither `sourceId` nor `sourceLabel` but has the older
string `"filename (ID: N)"` in `source` MUST be read as that id and filename.
The first such request of each kind MUST be logged. In the next release this
requirement is withdrawn.
*Reason: an importer updated after AssetManager keeps working during the
update. The log line shows whether anything still sends the string.*

## 5. Derived values

**AM-DATA-17** An asset's **severity** MUST be the highest severity of its
vulnerabilities, and 0 when it has none.
*Reason: an asset is as exposed as its worst weakness.*

**AM-DATA-18** An asset's **score** MUST be:
- 100 − severity, when the severity is above 0;
- 100, when it has findings but none of severity above 0;
- 0, when it has no findings at all.

*Reason: an asset nothing is known about must not look safe. It scores the
same as one that is fully compromised, and the Coverage score counts it as
uncovered (AM-DATA-42).*

**AM-DATA-19** An asset is **unmanaged** exactly when it has no findings,
neither vulnerabilities nor detections. The same definition MUST hold for its
score, for the statistics and for the Coverage score.
*Reason: one fact must not be counted two ways. The statistics tell a
severity-0 asset's two states apart by its score (AM-DATA-18), 100 with
findings and 0 without. They used to go by its detections alone, so a host
with only severity-0 vulnerabilities counted as unmanaged and as uncovered
(formerly deviation D1, fixed 2026-11-19).*

**AM-DATA-20** An asset's **last seen** MUST be the newest last seen of
anything that refers to it: its sighting links (AM-DATA-10) and its findings.
When nothing does, it MUST keep the value it has. An asset created from a
coverage table starts with the table's verification time, or the time it was
created.
*Reason: an asset is as current as its newest evidence. A deleted source's
date must not linger.*

**AM-DATA-21** A host asset's **last scanned** MUST be the newest last seen of
its findings, and none when it has none.
*Reason: a finding is the trace a scan leaves. A host listed in an inventory
was seen, but not scanned.*

**AM-DATA-22** The derived values MUST be recomputed for every affected asset,
of every affected owner, whenever evidence arrives or goes away. This holds
also when a service acts for all users at once. At startup, all assets MUST be
recomputed.
*Reason: AM-PUR-03. The startup pass repairs values left by an older version.*

**AM-DATA-23** Times MUST be compared as instants, not as stored text.
*Reason: records hold times in more than one textual layout, and those do not
sort as the times they stand for.*

## 6. When a source goes or is re-imported

**AM-DATA-24** Deleting a source's records MUST remove the findings with its id
and its links. It MUST then delete the assets left with no link and recompute
the others (AM-DATA-22).
*Reason: AM-PUR-04.*

**AM-DATA-28** After a re-import, the links and findings of the source that no
import has touched since the re-import started MUST be removed. Assets left
with no link MUST then be deleted and the others recomputed. Only a service
MAY ask for this.
*Reason: AM-PUR-05. The touch decides, not last seen, because re-importing
the same file does not move the dates it contains.*

Nothing else removes a record (AM-PUR-20): there is no deletion by id, no
keeping a source's records without it, and no deletion by source from a user
(formerly AM-DATA-25, -26, -27; decided 2026-11-19).

**AM-DATA-46** AssetManager MUST remove the records of a source that
ResourceManager no longer has, as deleting the source would (AM-DATA-24), and
the records that name no source id at all: findings without one, links known
by a label only, and assets left without a link. It MUST check each source
id against ResourceManager, a minute after the start and then every hour,
and MUST treat only a definite "not found" as gone; any other answer MUST
leave the source's records alone until the next check.
*Reason: ResourceManager deletes a source even when AssetManager does not
answer, which leaves the source's records behind, and a record that names no
source could otherwise never be removed. An outage of ResourceManager must
never remove anything.*

## 7. Scan coverage

**AM-DATA-29** Scan coverage MUST be recorded per link, as the source reports
it: when it was verified that the asset is in a scheduled scan, and that
scan's interval in days. An interval below 1, or a time that cannot be read,
MUST be ignored with a warning, without refusing the asset.
*Reason: AM-PUR-06. One bad cell in a coverage column must not lose the host.*

**AM-DATA-30** An asset's scan coverage MUST be the newest verification and
the shortest interval over its links. It is none when no link reports one,
and it MUST be combined anew whenever a link goes.
*Reason: the asset is covered as well as its best schedule covers it. A
removed source's coverage must not linger.*

**AM-DATA-31** A scan coverage table MUST be applied target by target:
- An address covers the host asset of that name, which is created when it is
  missing, within the limits (§8).
- A network or an address range covers every existing host asset whose name is
  an address inside it, and creates none.
- A hostname is skipped.
- A target that cannot be read, or an interval below 1, is counted as invalid.

The outcome MUST report the counts of each.
*Reason: a schedule naming one address states a host. A network states only
a span, and creating every address in a /16 would invent 65,536 assets.
Host assets are named by address (AM-DATA-05).*

**AM-DATA-32** When several entries of one table cover the same asset, the
asset MUST get the shortest interval and the newest verification among them.
*Reason: the same as AM-DATA-30, within one table.*

## 8. Limits

**AM-DATA-33** Creating a new host asset MUST be refused once the owner holds
as many host assets as their limit allows. On an appliance, which is one
shared workspace, the host assets of all users count against it. A limit of
0 MUST mean no limit. Reporting an existing host asset again creates nothing,
so it MUST NOT be refused. An owner SystemManager does not know MUST be
refused with 400, not created without a limit (AM-SEC-13). A finding whose
host asset would be new (AM-DATA-13) MUST be refused like that host asset.
*Reason: AM-PUR-07. The limit is a count of hosts, and a re-import at the
limit must not be refused and then removed as stale.*

**AM-DATA-34** On an appliance with an active licence, creating a host asset
MUST also be refused once all users together hold the licensed number. When
the licence cannot be read, creating MUST go ahead.
*Reason: the licence is for the appliance, not a user. A passing outage of
AIManager must not stop imports.*

**AM-DATA-35** Creating a new vulnerability or detection MUST be refused once
the owner holds as many as their limit allows. Reporting a known one again
(AM-DATA-47) creates nothing, so it MUST NOT be refused, neither at the limit
nor while SystemManager cannot be asked (AM-DATA-44). A limit of 0 MUST mean no limit.
An owner SystemManager does not know MUST be refused with 400 (AM-SEC-13).
The finding's host asset, when it would be new, is held to AM-DATA-33 as
well.
*Reason: AM-PUR-07. A re-import reports its findings again rather than creating
them, so they are not counted twice (decided 2026-11-19, formerly DATA
question 1).*

**AM-DATA-44** When SystemManager cannot be asked for the owner's limits,
creating a new host asset, a vulnerability or a detection MUST be refused
with 503. Reporting an existing host asset again is not a creation and MUST
NOT be refused for it (AM-DATA-33). A scan coverage table that would create a
host asset (AM-DATA-31) MUST then be answered with 503 as a whole, not with
the outcome. What it applied before stays applied.
*Reason: AM-PUR-07. A limit that cannot be read cannot be kept, and all three
limits are kept alike. The outage is reported as one, not as a reached limit,
so the caller can try again. Applying a table again changes nothing it
already applied (decided 2026-11-11, formerly PUR question 5).*

**AM-DATA-45** AssetManager MUST report to an internal service how many host
assets, vulnerabilities and detections each user holds, and the instance in
all, counted exactly as AM-DATA-33 and AM-DATA-35 count them. It MUST also
say whether the host assets of all users count against each limit (an
appliance) and, while one caps them, the licensed number (AM-DATA-34).
*Reason: an administrator sees in Admin → Users how much of each limit a user
has used. A count that differed from the limit check would show 90 % while
imports are already refused.*

**AM-DATA-36** Each refusal by a limit MUST be audited. The refusals of one
import MUST be combined into one audit entry per owner and source.
*Reason: an import refused 5,000 times is one event to an administrator, not
5,000.*

## 9. A user's records as a whole

**AM-DATA-37** On SystemManager's request, all records of a user MUST be
deleted: assets, links, vulnerabilities and detections, all or nothing. If
any part fails, nothing MUST be deleted and the request MUST be answered as
failed.
*Reason: AM-PUR-11. A reported success that left data behind is worse than a
failure that is retried.*

**AM-DATA-38** On SystemManager's request, all records of a user MUST be moved
to another user, completely or not at all. A record the receiver already has
MUST be merged into the receiver's, not block the move: an asset of the same
name and type gives the receiver's its links, two links of one source
becoming one (as in AM-DATA-09); a finding of the same identity
(AM-DATA-47) is folded into the receiver's, which takes the newer report's
label and severity and the newest last seen. A move that is not complete
MUST be answered as failed, and nothing MUST have moved.
*Reason: an anonymous session that registers must keep everything it
imported, and a user's records are set aside under a hidden holder account
while they are in a group and moved back when they leave. A partial move
splits hosts from their findings: moved kind by kind, a single host the
receiver already had stopped all assets from moving while their findings
moved, and the move was answered as done (formerly deviation D3, fixed
2026-11-19). A member leaving a group often has hosts the group admin has
too.*

**AM-DATA-39** AssetManager MUST report every user it holds any record for.
*Reason: SystemManager compares the list with the users that exist, and
removes what a missed deletion left behind.*

## 10. Statistics and scores

**AM-DATA-40** The statistics MUST count assets into severity bands: critical
(90–100), high (70–89), medium (40–69), low (1–39), no threat (severity 0,
with findings) and unmanaged (AM-DATA-19).
*Reason: the dashboard and the rules use the same bands, so they must not be
defined twice.*

**AM-DATA-48** The statistics MUST count the host assets that are current in
all three host views at once — last seen at most 30 days ago, last scanned at
most 2 days ago, and a scan coverage interval of 1 or 2 days — as
`hostsFullyCovered`, with the same boundaries as the three views' own counts.
*Reason: the dashboard's Asset status chart leads with how many hosts are fully
covered. The three counts alone cannot tell it: a host current in one view may
be stale in another, so the number has to be counted per host, here (decided
2026-11-23, docs/plans/asset-status-lanes.md).*

**AM-DATA-41** The **Exposure** score MUST be the average score of the owner's
assets, and 0 when there are none. On an appliance, which is one shared
workspace, it MUST be the average over the assets of all its users, and be
sent to the organisation of each of them (AM-DATA-43).
*Reason: AM-PUR-08. An organisation that has recorded nothing has not shown
that it is safe.*

**AM-DATA-42** The **Coverage** score MUST be
`100 − (missing + unmanaged) / declared × 100`. Here *declared* is the
organisation's declared number of hosts, *missing* is `max(0, declared −
assets)`, and *unmanaged* is as in AM-DATA-19. On an appliance, *assets* and
*unmanaged* MUST count the whole workspace, as in AM-DATA-41. The result MUST be clamped to
0–100 and rounded to two decimals. It MUST be 0 when nothing is declared.
*Reason: coverage measures how much of the declared estate is actually known.
A host known only by name is not known in the sense that matters. On an
appliance the workspace's statistics already count the whole workspace, and
its scores must say the same about the same records (decided 2026-11-19,
formerly DATA question 4).*

**AM-DATA-43** Both scores MUST be recomputed and sent to SystemManager after
each change to the owner's records, when SystemManager asks, and for every
user at startup.
*Reason: AM-PUR-08. SystemManager holds the overall score but can only
combine what it was sent.*

## 11. Known deviations

None is known.

## 12. Open questions

None is open.
