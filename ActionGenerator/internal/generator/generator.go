// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package generator is the evaluation engine. For each user it assembles a
// context — assets and their statistics, sources and tools, the organisation
// record, the user's own settings and AI assignments, the capabilities their
// enabled tools provide — runs every loaded rule routine against it, and creates
// the actions the routines return in ActionManager.
//
// A rule that no longer triggers has its outstanding "New" actions withdrawn, so
// an action disappears once the user has done the thing it asked for.
//
// In shared-workspace mode the rules are evaluated once against the aggregate of
// every user rather than once per user, so a team sees one action rather than
// one each.
package generator

import (
	"fmt"
	"sort"
	"sync"

	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/actiongenerator/internal/assetmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/resourcemanager"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/logger"
)

// sortedDistinct returns a sorted slice with duplicates removed.
// Empty input → []string{} (never nil) so the JS context always sees an
// array, never undefined.
func sortedDistinct(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Generator handles the action generation logic
type Generator struct {
	systemMgr   *systemmanager.Client
	resourceMgr *resourcemanager.Client
	assetMgr    *assetmanager.Client
	actionMgr   *actionmanager.Client
	aiMgr       *aimanager.Client
	executors   map[int64]*jsruntime.JSRuleExecutor
	executorMu  sync.RWMutex
}

// New creates a new Generator instance
func New(
	systemMgr *systemmanager.Client,
	resourceMgr *resourcemanager.Client,
	assetMgr *assetmanager.Client,
	actionMgr *actionmanager.Client,
	aiMgr *aimanager.Client,
) *Generator {
	return &Generator{
		systemMgr:   systemMgr,
		resourceMgr: resourceMgr,
		assetMgr:    assetMgr,
		actionMgr:   actionMgr,
		aiMgr:       aiMgr,
		executors:   make(map[int64]*jsruntime.JSRuleExecutor),
	}
}

// SetExecutors replaces the per-rule executor map.
func (g *Generator) SetExecutors(executors map[int64]*jsruntime.JSRuleExecutor) {
	g.executorMu.Lock()
	defer g.executorMu.Unlock()
	g.executors = executors
}

// ProcessRules iterates over all users and checks action rules
func (g *Generator) ProcessRules() error {
	logger.Info("Processing action rules...")

	// Get all users
	users, err := g.systemMgr.GetAllUsers()
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}

	if len(users) == 0 {
		logger.Debug("No users found")
		return nil
	}

	logger.Infof("Checking rules for %d users", len(users))

	// Take a snapshot of current executors
	g.executorMu.RLock()
	executorSnapshot := make(map[int64]*jsruntime.JSRuleExecutor, len(g.executors))
	for k, v := range g.executors {
		executorSnapshot[k] = v
	}
	g.executorMu.RUnlock()

	if len(executorSnapshot) == 0 {
		logger.Warn("No JS rules loaded, skipping rule processing")
		return nil
	}

	totalRules := 0
	for _, exec := range executorSnapshot {
		totalRules += exec.RuleCount()
	}
	logger.Infof("Using %d per-rule executors with %d total rules", len(executorSnapshot), totalRules)

	// Appliance mode is a single shared workspace: every non-anonymous user sees
	// the same data, so generating one action set per user surfaces as duplicates
	// in the shared Actions view. When the instance reports appliance mode with a
	// primary owner, evaluate each rule ONCE against the aggregate of all users'
	// data and create the actions under that one owner. SaaS keeps per-user
	// generation. A lookup failure falls back to per-user (safe default).
	shared := false
	var primaryID int64
	if ws, werr := g.systemMgr.GetWorkspace(); werr != nil {
		logger.Warnf("Could not determine workspace mode, defaulting to per-user generation: %v", werr)
	} else if ws.IsAppliance() {
		if ws.PrimaryUserID > 0 {
			shared, primaryID = true, ws.PrimaryUserID
		} else {
			logger.Warn("Appliance mode but no primary user reported; falling back to per-user generation")
		}
	}

	actionsCreated := 0
	var created int
	if shared {
		logger.Infof("Appliance shared-workspace mode: generating one action set under user %d", primaryID)
		created, err = g.processSharedWorkspace(users, executorSnapshot, primaryID)
	} else {
		created, err = g.processJSRules(users, executorSnapshot)
	}
	if err != nil {
		logger.Errorf("Error processing JS rules: %v", err)
	}
	actionsCreated += created

	if actionsCreated > 0 {
		logger.Infof("Created %d new actions", actionsCreated)
	} else {
		logger.Info("No new actions created")
	}

	return nil
}

// ProcessRuleForUser evaluates a single action rule for a single user and
// creates actions if the rule's JS routine produces any. Returns the number
// of actions created.
func (g *Generator) ProcessRuleForUser(ruleID int64, userID int64, force bool) (int, error) {
	g.executorMu.RLock()
	executor, ok := g.executors[ruleID]
	g.executorMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("no executor loaded for rule %d", ruleID)
	}

	user, err := g.systemMgr.GetUser(userID)
	if err != nil {
		return 0, fmt.Errorf("failed to get user %d: %w", userID, err)
	}

	ctx := g.buildJSRuleContext(user)

	actions, err := executor.EvaluateRules(ctx, force)
	if err != nil {
		return 0, fmt.Errorf("error evaluating rule %d for user %d: %w", ruleID, userID, err)
	}

	logger.Infof("Rule %d produced %d candidate actions for user %d (force=%v)", ruleID, len(actions), userID, force)

	// The rule no longer matches: collect the stale action it left behind,
	// exactly as the full sweep does. Without this the single-rule path could
	// only ever ADD — asking for a re-evaluation of a rule that had stopped
	// matching did nothing at all, which is the opposite of what asking for it
	// implies. Actions already acted on (Acknowledged/InProgress/Done) are
	// preserved by the server-side filter.
	if len(actions) == 0 {
		if n, derr := g.actionMgr.DeleteNewActionsByRule(userID, ruleID); derr != nil {
			logger.Warnf("Stale-action cleanup failed for user %d rule %d: %v", userID, ruleID, derr)
		} else if n > 0 {
			logger.Infof("Deleted %d stale action(s) for user %d rule %d (rule no longer matches)", n, userID, ruleID)
		}
		return 0, nil
	}

	actionsCreated := 0
	for _, actionReq := range actions {
		exists, err := g.actionMgr.ActionExists(userID, actionReq.Title)
		if err != nil {
			logger.Errorf("Error checking if action exists for user %d: %v", userID, err)
			continue
		}
		if exists {
			logger.Infof("Skipping existing action '%s' for user %d (rule %d)", actionReq.Title, userID, ruleID)
			continue
		}

		actionReq.UserID = userID
		ruleIDCopy := ruleID
		actionReq.ActionRuleID = &ruleIDCopy

		action, err := g.actionMgr.CreateAction(actionReq)
		if err != nil {
			return actionsCreated, fmt.Errorf("failed to create action for user %d: %w", userID, err)
		}

		logger.Infof("Successfully created action ID %d for user %d via trigger", action.ID, userID)
		actionsCreated++
	}

	return actionsCreated, nil
}

// processJSRules evaluates JS rules from all executors for all users
func (g *Generator) processJSRules(users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor) (int, error) {
	actionsCreated := 0

	for _, user := range users {
		ctx := g.buildJSRuleContext(&user)

		// Evaluate each per-rule executor
		for ruleID, executor := range executors {
			actions, err := executor.EvaluateRules(ctx, false)
			if err != nil {
				logger.Errorf("Error evaluating JS rules for user %d (rule executor %d): %v", user.ID, ruleID, err)
				continue
			}

			// Rule did not match the user's current context. Garbage-collect any
			// New-status action this rule had previously created for the user —
			// it's stale. Actions the user already acted on (Acknowledged,
			// InProgress, Done) are preserved by the server-side filter.
			if len(actions) == 0 {
				if n, derr := g.actionMgr.DeleteNewActionsByRule(user.ID, ruleID); derr != nil {
					logger.Warnf("Stale-action cleanup failed for user %d rule %d: %v", user.ID, ruleID, derr)
				} else if n > 0 {
					logger.Infof("Deleted %d stale action(s) for user %d rule %d (rule no longer matches)", n, user.ID, ruleID)
				}
				continue
			}

			for _, actionReq := range actions {
				// Check if action already exists for this user
				exists, err := g.actionMgr.ActionExists(user.ID, actionReq.Title)
				if err != nil {
					logger.Errorf("Error checking if action exists for user %d: %v", user.ID, err)
					continue
				}

				if exists {
					logger.Debugf("Action '%s' already exists for user %d (%s), skipping",
						actionReq.Title, user.ID, user.Username)
					continue
				}

				logger.Infof("Creating action '%s' for user %d (%s) via JS rule",
					actionReq.Title, user.ID, user.Username)

				actionReq.UserID = user.ID
				ruleIDCopy := ruleID
				actionReq.ActionRuleID = &ruleIDCopy

				action, err := g.actionMgr.CreateAction(actionReq)
				if err != nil {
					logger.Errorf("Failed to create action for user %d: %v", user.ID, err)
					continue
				}

				logger.Infof("Successfully created action ID %d for user %d", action.ID, user.ID)
				actionsCreated++
			}
		}
	}

	return actionsCreated, nil
}

// processSharedWorkspace evaluates every rule ONCE against the aggregate of all
// (non-anonymous) users' data and creates the resulting actions under a single
// primary owner. This is the Appliance-mode counterpart of processJSRules: the
// whole instance is one shared workspace, so one action set — not one per user —
// is correct. Owning everything under the primary user makes the existing
// per-(owner,rule) dedup collapse to one-per-rule automatically, and lets us
// consolidate away the per-user duplicates that earlier per-user generation left.
func (g *Generator) processSharedWorkspace(users []systemmanager.User, executors map[int64]*jsruntime.JSRuleExecutor, primaryID int64) (int, error) {
	var primary *systemmanager.User
	for i := range users {
		if users[i].ID == primaryID {
			primary = &users[i]
			break
		}
	}
	if primary == nil {
		return 0, fmt.Errorf("primary user %d not found among users", primaryID)
	}

	ctx := g.buildSharedContext(users, primary)
	actionsCreated := 0

	for ruleID, executor := range executors {
		actions, err := executor.EvaluateRules(ctx, false)
		if err != nil {
			logger.Errorf("Error evaluating shared JS rule %d: %v", ruleID, err)
			continue
		}

		// Rule no longer matches the shared context — garbage-collect every
		// New-status action it previously created, for any owner.
		if len(actions) == 0 {
			if n, derr := g.actionMgr.DeleteNewActionsByRuleExceptUser(ruleID, 0); derr != nil {
				logger.Warnf("Shared stale-action cleanup failed for rule %d: %v", ruleID, derr)
			} else if n > 0 {
				logger.Infof("Deleted %d shared stale action(s) for rule %d (rule no longer matches)", n, ruleID)
			}
			continue
		}

		for _, actionReq := range actions {
			exists, err := g.actionMgr.ActionExists(primaryID, actionReq.Title)
			if err != nil {
				logger.Errorf("Error checking if shared action exists (rule %d): %v", ruleID, err)
				continue
			}
			if exists {
				logger.Debugf("Shared action '%s' already exists under owner %d (rule %d), skipping", actionReq.Title, primaryID, ruleID)
				continue
			}
			actionReq.UserID = primaryID
			ruleIDCopy := ruleID
			actionReq.ActionRuleID = &ruleIDCopy
			action, err := g.actionMgr.CreateAction(actionReq)
			if err != nil {
				logger.Errorf("Failed to create shared action (rule %d): %v", ruleID, err)
				continue
			}
			logger.Infof("Created shared action ID %d (rule %d, owner %d)", action.ID, ruleID, primaryID)
			actionsCreated++
		}

		// Consolidate: drop any New-status copies of this rule owned by other
		// users (duplicates from prior per-user generation, or from a manual
		// single-user trigger), keeping only the primary owner's.
		if n, derr := g.actionMgr.DeleteNewActionsByRuleExceptUser(ruleID, primaryID); derr != nil {
			logger.Warnf("Shared consolidation failed for rule %d: %v", ruleID, derr)
		} else if n > 0 {
			logger.Infof("Consolidated %d duplicate action(s) for rule %d under owner %d", n, ruleID, primaryID)
		}
	}

	return actionsCreated, nil
}

// buildSharedContext builds a single RuleContext representing the whole shared
// workspace: the aggregate of every non-anonymous user's data. It reuses
// buildJSRuleContext per user and merges the results — concatenating collections
// and summing counts/stats, mirroring what the shared Actions/Assets views show.
// The User/organisation identity is taken from the primary owner.
func (g *Generator) buildSharedContext(users []systemmanager.User, primary *systemmanager.User) jsruntime.RuleContext {
	merged := jsruntime.RuleContext{
		Sources:      []map[string]interface{}{},
		Assets:       []map[string]interface{}{},
		Actions:      []map[string]interface{}{},
		Capabilities: jsruntime.CapabilitySet{Automated: []string{}, Manual: []string{}},
	}
	statKeys := []string{"critical", "high", "medium", "low", "noThreat", "unmanaged", "total"}
	statSum := make(map[string]int, len(statKeys))
	autoSet := map[string]struct{}{}
	manualSet := map[string]struct{}{}
	var highest map[string]interface{}
	highestScore := -1

	for i := range users {
		u := &users[i]
		if u.Role == "anonymous" {
			continue // anonymous sessions are not part of the shared workspace
		}
		ctx := g.buildJSRuleContext(u)
		if u.ID == primary.ID {
			merged.User = ctx.User // primary identity + legacy organisation aliases
			// One shared workspace is one organisation, so the primary owner's
			// record IS the workspace's. Settings follow the same owner: they
			// are per-user, and merging them would invent a person who has
			// everyone's configuration at once.
			merged.Organisation = ctx.Organisation
			merged.Settings = ctx.Settings
			// The questionnaires belong to the organisation, so they follow it.
			merged.Questionnaires = ctx.Questionnaires
		}
		merged.Sources = append(merged.Sources, ctx.Sources...)
		merged.SourceCount += ctx.SourceCount
		if ctx.SourceMax > merged.SourceMax {
			merged.SourceMax = ctx.SourceMax
		}
		merged.Assets = append(merged.Assets, ctx.Assets...)
		merged.AssetCount += ctx.AssetCount
		merged.Actions = append(merged.Actions, ctx.Actions...)
		merged.ActionCount += ctx.ActionCount
		if ctx.HighestScoreAsset != nil {
			if s := toInt(ctx.HighestScoreAsset["score"]); s > highestScore {
				highestScore, highest = s, ctx.HighestScoreAsset
			}
		}
		for _, k := range statKeys {
			statSum[k] += toInt(ctx.AssetStats[k])
		}
		for _, c := range ctx.Capabilities.Automated {
			autoSet[c] = struct{}{}
		}
		for _, c := range ctx.Capabilities.Manual {
			manualSet[c] = struct{}{}
		}
	}

	if merged.User == nil {
		// Primary was filtered out or absent — fall back to a minimal identity.
		merged.User = map[string]interface{}{
			"id": primary.ID, "username": primary.Username, "email": primary.Email, "role": primary.Role,
		}
	}
	// Same fallback for the two objects that follow the primary owner: present
	// and empty rather than missing, so a rule reads a zero value instead of
	// failing on undefined.
	if merged.Organisation == nil {
		merged.Organisation = jsruntime.EmptyOrganisation()
	}
	if merged.Settings == nil {
		merged.Settings = jsruntime.EmptySettings()
	}
	if merged.Questionnaires == nil {
		merged.Questionnaires = jsruntime.EmptyQuestionnaires()
	}
	merged.HighestScoreAsset = highest
	stats := make(map[string]interface{}, len(statKeys))
	for _, k := range statKeys {
		stats[k] = statSum[k]
	}
	merged.AssetStats = stats
	merged.Capabilities.Automated = setToSorted(autoSet)
	merged.Capabilities.Manual = setToSorted(manualSet)
	return merged
}

// toInt coerces a JSON-decoded numeric (int/int64/float64) to int; 0 otherwise.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// setToSorted returns the set's keys as a sorted, distinct slice (never nil).
func setToSorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return sortedDistinct(out)
}

// buildJSRuleContext creates a JS RuleContext for a user by fetching all raw data
// buildJSRuleContext assembles everything a rule may read about one user.
//
// Each section below fetches from one service and maps the answer in. They are
// deliberately independent: a service that is down costs its own fields and
// nothing else, because rules are told these fields always exist and a
// half-built context is worse than an empty one.
func (g *Generator) buildJSRuleContext(user *systemmanager.User) jsruntime.RuleContext {
	ctx := jsruntime.RuleContext{
		User: map[string]interface{}{
			"id":            user.ID,
			"username":      user.Username,
			"email":         user.Email,
			"role":          user.Role,
			"maxHostAssets": user.MaxHostAssets,
			"createdAt":     user.CreatedAt,
		},
	}

	g.addOrganisation(&ctx, user)
	g.addSettings(&ctx, user)
	g.addSources(&ctx, user)
	g.addAssets(&ctx, user)
	g.addAssetStats(&ctx, user)
	ctx.Capabilities = g.resolveCapabilities(user)
	logger.Infof("Context user=%d assetStats=%+v capabilities=automated=%v manual=%v", user.ID, ctx.AssetStats, ctx.Capabilities.Automated, ctx.Capabilities.Manual)
	g.addActions(&ctx, user)

	return ctx
}

// addOrganisation fills the organisation record and the questionnaire summary,
// plus the three legacy ctx.user.organisation* aliases older routines read.
func (g *Generator) addOrganisation(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch organisation data for the user.
	//
	// Set unconditionally, including when the fetch fails: rules are told these
	// fields are always defined, and a rule doing arithmetic on an undefined
	// host-asset count silently produces NaN rather than failing visibly.
	ctx.Organisation = jsruntime.EmptyOrganisation()
	ctx.Questionnaires = jsruntime.EmptyQuestionnaires()
	org, err := g.systemMgr.GetOrganisationForUser(user.ID)
	if err != nil {
		logger.Warnf("Could not fetch organisation for user %d: %v", user.ID, err)
	} else {
		ctx.Organisation = jsruntime.OrganisationToMap(org)
		// Arrives with the organisation; nil when SystemManager could not
		// compute it, which keeps the empty shape set above.
		if org.Questionnaires != nil {
			ctx.Questionnaires = jsruntime.QuestionnairesToMap(org.Questionnaires)
			logger.Infof("Context user=%d questionnaires: impact=%d%%/%q readiness=%d%%/%q strategy=%d%%",
				user.ID, org.Questionnaires.NIS2Impact.Progress, org.Questionnaires.NIS2Impact.Level,
				org.Questionnaires.NIS2Readiness.Progress, org.Questionnaires.NIS2Readiness.Level,
				org.Questionnaires.CyberStrategy.Progress)
		}
		logger.Infof("Context user=%d organisation: name=%q assetsHosts=%d score=%v nace=%q homepage=%q itProviders=%d",
			user.ID, org.Name, org.AssetsHosts, org.Score, org.NaceCode, org.HomepageURL, len(org.ITProviders))
	}
	// Legacy aliases. Routines already generated against the old context read
	// these three, so they stay — but ctx.organisation is what new code uses,
	// and the prompt says so.
	ctx.User["organisationHostAssets"] = ctx.Organisation["assetsHosts"]
	ctx.User["organisationName"] = ctx.Organisation["name"]
	ctx.User["organisationScore"] = ctx.Organisation["score"]

}

// addSettings fills what the user has configured for themselves.
func (g *Generator) addSettings(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch what the user has configured for themselves. Two services own the
	// answer between them: SystemManager holds the assignments, AIManager knows
	// how many providers the user actually owns.
	settings, err := g.systemMgr.GetUserSettings(user.ID)
	if err != nil {
		logger.Warnf("Could not fetch settings for user %d: %v", user.ID, err)
		ctx.Settings = jsruntime.EmptySettings()
	} else {
		ctx.Settings = jsruntime.SettingsToMap(settings)
	}
	if count, cerr := g.aiMgr.CountPersonalAIs(user.ID); cerr != nil {
		logger.Warnf("Could not count personal AI providers for user %d: %v", user.ID, cerr)
	} else if ai, ok := ctx.Settings["ai"].(map[string]interface{}); ok {
		ai["personalProviderCount"] = count
	}

}

// addSources fills the source list and the account's source limit.
func (g *Generator) addSources(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch sources and source limits
	sourceCount, sourceMax, err := g.resourceMgr.GetSourceCount(user.ID)
	if err != nil {
		logger.Errorf("Error getting source count for user %d: %v", user.ID, err)
	} else {
		ctx.SourceCount = sourceCount
		ctx.SourceMax = sourceMax
	}

	sources, err := g.resourceMgr.GetSourcesForUser(user.ID)
	if err != nil {
		logger.Errorf("Error getting sources for user %d: %v", user.ID, err)
	} else {
		ctx.Sources = make([]map[string]interface{}, len(sources))
		for i, s := range sources {
			ctx.Sources[i] = map[string]interface{}{
				"id":         s.ID,
				"filename":   s.Filename,
				"sourceType": s.SourceType,
				"status":     s.Status,
				"createdAt":  s.CreatedAt,
			}
		}
	}

}

// addAssets fills the asset list and derives the highest-scoring one.
func (g *Generator) addAssets(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch all assets, derive highestScoreAsset locally
	assets, err := g.assetMgr.GetAssetsForUser(user.ID)
	if err != nil {
		logger.Errorf("Error getting assets for user %d: %v", user.ID, err)
	} else {
		logger.Infof("Context user=%d assets fetched: count=%d", user.ID, len(assets))
		ctx.Assets = make([]map[string]interface{}, len(assets))
		ctx.AssetCount = len(assets)
		var highest *assetmanager.Asset
		for i := range assets {
			a := &assets[i]
			ctx.Assets[i] = map[string]interface{}{
				"id":         a.ID,
				"name":       a.Name,
				"type":       a.Type,
				"source":     a.Source,
				"severity":   a.Severity,
				"score":      a.Score,
				"createdAt":  a.CreatedAt,
				"modifiedAt": a.ModifiedAt,
				"lastSeen":   a.LastSeen,
			}
			if a.Score > 0 && (highest == nil || a.Score > highest.Score) {
				highest = a
			}
		}
		if highest != nil {
			ctx.HighestScoreAsset = map[string]interface{}{
				"id":       highest.ID,
				"name":     highest.Name,
				"type":     highest.Type,
				"source":   highest.Source,
				"severity": highest.Severity,
				"score":    highest.Score,
				"lastSeen": highest.LastSeen,
			}
		}
	}

}

// addAssetStats fills the severity buckets. The zeroed shape on failure is
// deliberate: a rule doing arithmetic on an absent count produces NaN silently.
func (g *Generator) addAssetStats(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch asset stats (severity buckets + unmanaged/noThreat counts)
	stats, err := g.assetMgr.GetAssetStats(user.ID)
	if err != nil {
		logger.Warnf("Could not fetch asset stats for user %d: %v", user.ID, err)
		ctx.AssetStats = map[string]interface{}{
			"critical": 0, "high": 0, "medium": 0, "low": 0,
			"noThreat": 0, "unmanaged": 0, "total": 0,
		}
	} else {
		ctx.AssetStats = map[string]interface{}{
			"critical":  stats.Critical,
			"high":      stats.High,
			"medium":    stats.Medium,
			"low":       stats.Low,
			"noThreat":  stats.NoThreat,
			"unmanaged": stats.Unmanaged,
			"total":     stats.Total,
		}
	}

}

// resolveCapabilities works out what the user's tools can do, split the way the
// Resources/Tools page splits them.
func (g *Generator) resolveCapabilities(user *systemmanager.User) jsruntime.CapabilitySet {
	capabilities := jsruntime.CapabilitySet{Automated: []string{}, Manual: []string{}}
	// Fetch user's tools and resolve capabilities, mirroring the
	// Resources/Tools page: split into "automated" (covered by an
	// enabled tool_rule) and "manual" (listed in manualCapabilityIds
	// on the tool's ToolOverview). Both sets are distinct + sorted.
	tools, err := g.resourceMgr.GetToolsForUser(user.ID)
	if err != nil {
		logger.Warnf("Could not fetch tools for user %d: %v", user.ID, err)
	} else if len(tools) > 0 && g.aiMgr != nil {
		// Set of overview IDs the user owns. Skip unmanaged rows
		// (ToolID nil) — no overview, nothing to resolve.
		ownedOverviewIDs := make(map[int64]struct{}, len(tools))
		for _, t := range tools {
			if t.ToolID == nil {
				continue
			}
			ownedOverviewIDs[*t.ToolID] = struct{}{}
		}

		if len(ownedOverviewIDs) > 0 {
			// Automated: enabled tool_rules whose toolOverviewId is owned.
			// Pass their IDs to the existing capability-name resolver.
			rules, rerr := g.aiMgr.GetEnabledToolRules()
			if rerr != nil {
				logger.Warnf("Could not fetch enabled tool rules for user %d: %v", user.ID, rerr)
			} else {
				ownedRuleIDs := make([]int64, 0, len(rules))
				for _, r := range rules {
					if r.ToolOverviewID == nil {
						continue
					}
					if _, ok := ownedOverviewIDs[*r.ToolOverviewID]; ok {
						ownedRuleIDs = append(ownedRuleIDs, r.ID)
					}
				}
				if len(ownedRuleIDs) > 0 {
					autoNames, capErr := g.aiMgr.GetCapabilityNamesForToolRules(ownedRuleIDs)
					if capErr != nil {
						logger.Warnf("Could not resolve automated capabilities for user %d: %v", user.ID, capErr)
					} else {
						capabilities.Automated = sortedDistinct(autoNames)
					}
				}
			}

			// Manual: union of manualCapabilityIds across owned
			// overviews → name lookup via tool_capabilities.
			overviews, oerr := g.aiMgr.GetToolOverviews()
			if oerr != nil {
				logger.Warnf("Could not fetch tool overviews for user %d: %v", user.ID, oerr)
			} else {
				caps, cerr := g.aiMgr.GetToolCapabilities()
				nameByID := make(map[int64]string, len(caps))
				if cerr != nil {
					logger.Warnf("Could not fetch tool capabilities for user %d: %v", user.ID, cerr)
				} else {
					for _, c := range caps {
						nameByID[c.ID] = c.Name
					}
				}
				manualSet := make(map[string]struct{})
				for _, o := range overviews {
					if _, ok := ownedOverviewIDs[o.ID]; !ok {
						continue
					}
					for _, capID := range o.ManualCapabilityIDs {
						if name, ok := nameByID[capID]; ok && name != "" {
							manualSet[name] = struct{}{}
						}
					}
				}
				if len(manualSet) > 0 {
					manualNames := make([]string, 0, len(manualSet))
					for name := range manualSet {
						manualNames = append(manualNames, name)
					}
					capabilities.Manual = sortedDistinct(manualNames)
				}
			}
		}
	}
	return capabilities
}

// addActions fills the actions the user already has, so a rule can avoid
// raising one it has raised before.
func (g *Generator) addActions(ctx *jsruntime.RuleContext, user *systemmanager.User) {
	// Fetch all actions
	actions, err := g.actionMgr.GetActionsForUser(user.ID)
	if err != nil {
		logger.Errorf("Error getting actions for user %d: %v", user.ID, err)
	} else {
		ctx.Actions = make([]map[string]interface{}, len(actions))
		ctx.ActionCount = len(actions)
		for i, a := range actions {
			ctx.Actions[i] = map[string]interface{}{
				"id":         a.ID,
				"title":      a.Title,
				"type":       a.Type,
				"status":     a.Status,
				"priority":   a.Priority,
				"assignedTo": a.AssignedTo,
				"issuedBy":   a.IssuedBy,
				"trigger":    a.Trigger,
				"createdAt":  a.CreatedAt,
			}
		}
	}

}
