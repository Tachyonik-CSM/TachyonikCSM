// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsruntime executes the AI-generated action rules in an embedded
// JavaScript engine (goja) and builds the context they are evaluated against.
//
// A routine is an array of rules, each a check(ctx) that decides whether the
// rule applies and a createAction(ctx) that describes the action to raise. The
// context carries what the rule may reason about: the user, their organisation,
// their settings and per-purpose AI assignments, their assets, sources, tools
// and tool capabilities. Its shape is fixed and every key is always present —
// a rule reading something the installation has not recorded must see an empty
// value rather than undefined.
//
// ValidateWithMockCtxReport gates a newly generated routine before it is ever
// trusted: it runs every rule against mock contexts and rejects routines that
// throw, and routines that are degenerate — a check() answering the same way
// whatever it is given is not a trigger, however plausible its code looks.
//
// Everything a routine does happens under a guard, because the code was written
// by a model from a prompt and goja cannot be preempted. Every call into
// JavaScript goes through runGuarded, which interrupts the engine once the
// configured budget is spent, and the call stack is capped so runaway recursion
// fails fast instead of taking the process's memory with it.
//
// What a routine returns is checked too. validateAction rejects an action whose
// type, status, assignment or priority falls outside the vocabulary
// ActionManager accepts, and truncates the free-text fields to their limits, so
// a routine cannot widen the platform's enums by inventing a value.
package jsruntime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/actiongenerator/internal/systemmanager"
	"tachyonik/lib/jsctxtrace"
	"tachyonik/lib/jsmap"
	"tachyonik/lib/logger"
)

// CapabilitySet partitions the user's available tool capabilities into
// the two buckets shown in the Resources/Tools page.
//
// Automated: distinct, sorted names of capabilities covered by an
// enabled tool_rule on at least one of the user's tools (Tachyonik can
// drive the tool to perform this capability).
//
// Manual: distinct, sorted names of capabilities listed in the
// manualCapabilityIds of the ToolOverview of at least one of the
// user's tools (the tool itself can't do it; the operator must invoke
// another way or pick a different tool).
//
// A capability can appear in both arrays when different tools cover it
// differently — the Resources/Tools page shows it the same way row-by-
// row, so we don't try to deduplicate across the two sets.
type CapabilitySet struct {
	Automated []string `json:"automated"`
	Manual    []string `json:"manual"`
}

// capabilitiesToMap converts CapabilitySet into a goja-friendly map.
// goja's ToValue exposes Go struct fields by their (capitalised) Go
// names, so binding the struct directly would surface
// ctx.capabilities.Automated / .Manual instead of the lowercase JSON
// tag names. The boundary conversion to map[string]interface{} matches
// how the rest of RuleContext is exposed (User, AssetStats, etc.) and
// gives the JS rule code the lowercase paths it expects:
//
//	ctx.capabilities.automated.indexOf("X")
//	ctx.capabilities.manual.indexOf("X")
//
// Nil slices are coalesced to empty arrays so .indexOf never sees null.
func capabilitiesToMap(c CapabilitySet) map[string]interface{} {
	auto := c.Automated
	if auto == nil {
		auto = []string{}
	}
	manual := c.Manual
	if manual == nil {
		manual = []string{}
	}
	return map[string]interface{}{
		"automated": auto,
		"manual":    manual,
	}
}

// RuleContext contains all data available for JS rule evaluation
type RuleContext struct {
	User              map[string]interface{}   `json:"user"`
	Organisation      map[string]interface{}   `json:"organisation"`
	Settings          map[string]interface{}   `json:"settings"`
	Questionnaires    map[string]interface{}   `json:"questionnaires"`
	SourceCount       int                      `json:"sourceCount"`
	SourceMax         int                      `json:"sourceMax"`
	Sources           []map[string]interface{} `json:"sources"`
	AssetCount        int                      `json:"assetCount"`
	Assets            []map[string]interface{} `json:"assets"`
	HighestScoreAsset map[string]interface{}   `json:"highestScoreAsset"`
	AssetStats        map[string]interface{}   `json:"assetStats"`
	Capabilities      CapabilitySet            `json:"capabilities"`
	ActionCount       int                      `json:"actionCount"`
	Actions           []map[string]interface{} `json:"actions"`
}

// ToMap renders the context as the `ctx` object a rule sees.
//
// ONE conversion, used by both the evaluation path and the mock-context
// validation below. It used to be written out twice in this file, and the
// duplication was not free: a field added to RuleContext and to one copy is a
// field that validates and then fails in production, or the reverse. The same
// shape is mirrored again in ActionExecutor's dry run and in the WebUI's test
// presets, which cannot share this code — those two are what most recently
// drifted, so anything added here belongs in all four places.
//
// Nil maps are replaced with empty objects: a rule reading ctx.organisation.x
// on an organisation that could not be fetched should see "" or 0, not fail on
// undefined.
func (c RuleContext) ToMap() map[string]interface{} {
	orDefault := func(m map[string]interface{}) map[string]interface{} {
		if m == nil {
			return map[string]interface{}{}
		}
		return m
	}
	return map[string]interface{}{
		"user":         orDefault(c.User),
		"organisation": orDefault(c.Organisation),
		"settings":     orDefault(c.Settings),
		// Never an empty object: a rule reads ctx.questionnaires.nis2Impact.progress
		// straight away, and must get zeros rather than fail on undefined.
		"questionnaires": questionnairesOrEmpty(c.Questionnaires),
		"sourceCount":    c.SourceCount,
		"sourceMax":      c.SourceMax,
		"sources":        c.Sources,
		"assetCount":     c.AssetCount,
		"assets":         c.Assets,
		// Null when no asset is scored — a real state the rule must check for,
		// so it is NOT defaulted to an empty object.
		"highestScoreAsset": c.HighestScoreAsset,
		"assetStats":        orDefault(c.AssetStats),
		"capabilities":      capabilitiesToMap(c.Capabilities),
		"actionCount":       c.ActionCount,
		"actions":           c.Actions,
	}
}

// OrganisationToMap renders the organisation for the JS context.
//
// Every field the internal endpoint returns, because a rule about something an
// organisation has NOT recorded — no homepage, no NACE code, no address — can
// only be written if the empty value is visible. Keys are the endpoint's own
// JSON names, so the record reads the same here as it does in the browser.
//
// This lives here rather than in the generator so that the populated shape and
// the empty one below have a single definition. Two of them is how a field ends
// up present when the fetch succeeds and undefined when it does not.
func OrganisationToMap(org *systemmanager.Organisation) map[string]interface{} {
	return map[string]interface{}{
		"name":        org.Name,
		"assetsHosts": org.AssetsHosts,
		"score":       org.Score,

		"homepageUrl":         org.HomepageURL,
		"naceCode":            org.NaceCode,
		"employees":           org.Employees,
		"employeesAffiliated": org.EmployeesAffiliated,

		"addressStreet":     org.AddressStreet,
		"addressPostalCode": org.AddressPostalCode,
		"addressCity":       org.AddressCity,
		"addressCountry":    org.AddressCountry,

		"companyLegalName": org.CompanyLegalName,
		"vatNumber":        org.VATNumber,
		"vatNumberValid":   org.VATNumberValid,

		"itStaffHourlyCost": org.ITStaffHourlyCost,
		"currency":          org.Currency,

		"itProviders": itProvidersToList(org.ITProviders),
	}
}

// itProvidersToList renders the providers as a JS array of plain objects.
//
// Always an array, never null, and each provider's types always an array too:
// the rules this exists for are written as
// `ctx.organisation.itProviders.some(function (p) { return p.types.indexOf("cybersecurity") >= 0; })`
// (ES5.1: goja has no arrow functions and no .includes), and an organisation with no providers — the case such a rule is looking for —
// must reach that expression as an empty array rather than fail on null.
func itProvidersToList(providers []systemmanager.ITProvider) []interface{} {
	out := make([]interface{}, 0, len(providers))
	for _, p := range providers {
		types := make([]interface{}, 0, len(p.Types))
		for _, t := range p.Types {
			types = append(types, t)
		}
		out = append(out, map[string]interface{}{
			"name":         p.Name,
			"homepageUrl":  p.HomepageURL,
			"contactEmail": p.ContactEmail,
			"types":        types,
		})
	}
	return out
}

// EmptyOrganisation is what a rule sees when the organisation could not be
// fetched: every field present, at its zero value. Same shape, no undefined.
func EmptyOrganisation() map[string]interface{} {
	return OrganisationToMap(&systemmanager.Organisation{})
}

// QuestionnairesToMap renders SystemManager's questionnaire summary for the JS
// context, under the names the Dialog action options use for the same three.
//
// One conversion on purpose: the readiness reporting score. SystemManager sends
// null while the reporting area has no answers, and in JavaScript
// `null < 50` is TRUE — a rule written "reporting score below 50" would fire
// for an organisation that has not answered a single reporting question. So a
// rule gets a number (0 when unanswered) and reportingAnswered to tell the two
// apart.
func QuestionnairesToMap(q *systemmanager.Questionnaires) map[string]interface{} {
	if q == nil {
		return EmptyQuestionnaires()
	}
	strings := func(in []string) []interface{} {
		out := make([]interface{}, 0, len(in))
		for _, v := range in {
			out = append(out, v)
		}
		return out
	}
	reportingScore, reportingAnswered := 0.0, false
	if q.NIS2Readiness.ReportingScore != nil {
		reportingScore, reportingAnswered = *q.NIS2Readiness.ReportingScore, true
	}
	return map[string]interface{}{
		"nis2Impact": map[string]interface{}{
			"progress":            q.NIS2Impact.Progress,
			"answered":            q.NIS2Impact.Answered,
			"total":               q.NIS2Impact.Total,
			"complete":            q.NIS2Impact.Complete,
			"level":               q.NIS2Impact.Level,
			"size":                q.NIS2Impact.Size,
			"unclearTreatedAsYes": q.NIS2Impact.UnclearTreatedAsYes,
			"lastUpdated":         q.NIS2Impact.LastUpdated,
			"expired":             q.NIS2Impact.Expired,
		},
		"nis2Readiness": map[string]interface{}{
			"progress":          q.NIS2Readiness.Progress,
			"answered":          q.NIS2Readiness.Answered,
			"total":             q.NIS2Readiness.Total,
			"complete":          q.NIS2Readiness.Complete,
			"score":             q.NIS2Readiness.Score,
			"level":             q.NIS2Readiness.Level,
			"unknownCount":      q.NIS2Readiness.UnknownCount,
			"gaps":              strings(q.NIS2Readiness.Gaps),
			"reportingScore":    reportingScore,
			"reportingAnswered": reportingAnswered,
			"reportingCritical": q.NIS2Readiness.ReportingCritical,
			"lastUpdated":       q.NIS2Readiness.LastUpdated,
			"expired":           q.NIS2Readiness.Expired,
		},
		"cyberStrategy": map[string]interface{}{
			"progress":        q.CyberStrategy.Progress,
			"answered":        q.CyberStrategy.Answered,
			"total":           q.CyberStrategy.Total,
			"complete":        q.CyberStrategy.Complete,
			"missingRequired": strings(q.CyberStrategy.MissingRequired),
			"lastUpdated":     q.CyberStrategy.LastUpdated,
			"expired":         q.CyberStrategy.Expired,
		},
	}
}

// EmptyQuestionnaires is what a rule sees when the summary could not be
// fetched: every key present, nothing started. The same shape as a populated
// summary, so no rule reads undefined.
func EmptyQuestionnaires() map[string]interface{} {
	return QuestionnairesToMap(&systemmanager.Questionnaires{
		NIS2Impact: systemmanager.QuestionnaireImpact{Size: "small"},
	})
}

func questionnairesOrEmpty(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return EmptyQuestionnaires()
	}
	return m
}

// SettingsToMap renders the user's own configuration for the JS context.
// PersonalProviderCount is filled in by the caller — AIManager owns that number
// and SystemManager, which supplies the rest, does not know it.
func SettingsToMap(s *systemmanager.UserSettings) map[string]interface{} {
	return map[string]interface{}{
		"ai": map[string]interface{}{
			"research":              s.AI.Research,
			"chat":                  s.AI.Chat,
			"dashboard":             s.AI.Dashboard,
			"personalProviderCount": 0,
		},
		"language": s.Language,
	}
}

// EmptySettings is the fallback when the settings could not be fetched. The
// values are the defaults an account that has configured nothing reports, so a
// rule about unconfigured features behaves the same either way.
func EmptySettings() map[string]interface{} {
	return SettingsToMap(&systemmanager.UserSettings{
		AI: systemmanager.UserAISettings{
			Research: "none", Chat: "internal", Dashboard: "internal",
		},
	})
}

// jsRule represents a loaded JS rule with callable functions
type jsRule struct {
	name     string
	ruleID   int64
	checkFn  goja.Callable
	actionFn goja.Callable
}

// JSRuleExecutor manages and executes JavaScript-based rules
type JSRuleExecutor struct {
	mu          sync.RWMutex
	vm          *goja.Runtime
	rules       []jsRule
	execTimeout time.Duration
}

// New creates a new JSRuleExecutor. execTimeout bounds a single JS execution:
// loading a routine, one check(), one createAction(). A non-positive value
// disables the guard, which is only ever wanted in a test that wants to observe
// an unbounded run.
func New(execTimeout time.Duration) *JSRuleExecutor {
	return &JSRuleExecutor{execTimeout: execTimeout}
}

// runGuarded runs fn, interrupting vm if it exceeds timeout. goja has no
// preemption, so an infinite loop or catastrophic regex in routine code would
// otherwise hang the caller forever — and the code here is written by an AI
// from a prompt, which is exactly the code least worth trusting to terminate.
// Runtime.Interrupt is the sanctioned way to abort from another goroutine. Note
// it aborts JS, not Go: a loop inside a Go callee is not interruptible and
// needs fixing at the source.
//
// The mutex orders the timer's Interrupt against our ClearInterrupt.
// time.Timer.Stop does not wait for a timer that is already firing, so without
// it an Interrupt could land after ClearInterrupt and leave the flag set — and
// the next execution on this VM would abort instantly with "budget exceeded", a
// slow rule silently poisoning its successor.
//
// The recover is load-bearing. goja reports an interrupt (and a JS throw) by
// panicking out of APIs that have no error return — Value.String, Value.Export,
// ToInteger — and only RunString and Callable.Call install a recover of their
// own. Every JS-touching operation runs through here, so catching it here is
// what keeps a routine that overruns its budget from taking the daemon down
// instead of just being rejected.
//
// Lifted from SourceAnalyser, SourceImporter and ActionExecutor, which have
// carried this for longer; ActionGenerator had no guard at all.
func runGuarded(vm *goja.Runtime, timeout time.Duration, fn func() (goja.Value, error)) (v goja.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			v = nil
			err = fmt.Errorf("JS execution aborted: %v", r)
		}
	}()

	if timeout <= 0 {
		return fn()
	}

	var mu sync.Mutex
	finished := false

	t := time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			vm.Interrupt("JS execution budget exceeded")
		}
	})

	// Deferred so it also runs when fn panics: otherwise the timer stays armed
	// and the interrupt flag survives into the next execution on this VM.
	defer func() {
		mu.Lock()
		finished = true
		mu.Unlock()
		t.Stop()
		vm.ClearInterrupt()
	}()

	return fn()
}

// LoadFromString parses JavaScript code and extracts the rules array
func (e *JSRuleExecutor) LoadFromString(code string) error {
	vm := goja.New()
	// Bound recursion. The execution budget stops a routine that loops; it does
	// not stop one that recurses, which exhausts memory rather than time and
	// can do it faster than the timer fires. goja reports the overflow as a
	// JS exception, so an offending rule is rejected like any other bad one.
	vm.SetMaxCallStackSize(maxCallStackSize)

	var loaded []jsRule
	// One budget covers the top-level evaluation AND the extraction below.
	// Extraction is not inert bookkeeping: Export, indexed and named property
	// reads, String() and ToInteger() all invoke getters, toString and valueOf
	// that the routine itself defines, so guarding only RunString would leave
	// those hooks unbounded.
	_, err := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
		var lerr error
		loaded, lerr = loadRules(vm, code)
		return nil, lerr
	})
	if err != nil {
		return err
	}

	// Atomically swap rules
	e.mu.Lock()
	e.vm = vm
	e.rules = loaded
	e.mu.Unlock()

	logger.Infof("Loaded %d JS rules", len(loaded))
	return nil
}

// loadRules evaluates the routine and extracts its rules array. Always called
// inside runGuarded — every step here can run routine-defined code.
func loadRules(vm *goja.Runtime, code string) ([]jsRule, error) {
	// Execute the code
	_, err := vm.RunString(code)
	if err != nil {
		return nil, fmt.Errorf("failed to execute JS code: %w", err)
	}

	// Extract the rules array
	rulesVal := vm.Get("rules")
	if rulesVal == nil || goja.IsUndefined(rulesVal) || goja.IsNull(rulesVal) {
		return nil, fmt.Errorf("JS code does not define a 'rules' variable")
	}

	rulesObj := rulesVal.Export()
	rulesSlice, ok := rulesObj.([]interface{})
	if !ok {
		return nil, fmt.Errorf("'rules' is not an array")
	}

	var loaded []jsRule
	for i, item := range rulesSlice {
		ruleObj, ok := item.(*goja.Object)
		if !ok {
			// Try getting it from the VM directly
			ruleObj = vm.ToValue(item).ToObject(vm)
		}
		if ruleObj == nil {
			return nil, fmt.Errorf("rule[%d] is not an object", i)
		}

		// Actually, we need the goja objects. Let's use the VM's array directly.
		arrObj := rulesVal.ToObject(vm)
		ruleVal := arrObj.Get(fmt.Sprintf("%d", i))
		if ruleVal == nil {
			return nil, fmt.Errorf("rule[%d] is nil", i)
		}

		rObj := ruleVal.ToObject(vm)

		// Extract name
		nameVal := rObj.Get("name")
		if nameVal == nil || goja.IsUndefined(nameVal) {
			return nil, fmt.Errorf("rule[%d] missing 'name'", i)
		}
		name := nameVal.String()

		// Extract ruleId
		var ruleID int64
		ruleIDVal := rObj.Get("ruleId")
		if ruleIDVal != nil && !goja.IsUndefined(ruleIDVal) {
			ruleID = ruleIDVal.ToInteger()
		}

		// Extract check function
		checkVal := rObj.Get("check")
		if checkVal == nil || goja.IsUndefined(checkVal) {
			return nil, fmt.Errorf("rule[%d] (%s) missing 'check' function", i, name)
		}
		checkFn, ok := goja.AssertFunction(checkVal)
		if !ok {
			return nil, fmt.Errorf("rule[%d] (%s) 'check' is not a function", i, name)
		}

		// Extract createAction function
		actionVal := rObj.Get("createAction")
		if actionVal == nil || goja.IsUndefined(actionVal) {
			return nil, fmt.Errorf("rule[%d] (%s) missing 'createAction' function", i, name)
		}
		actionFn, ok := goja.AssertFunction(actionVal)
		if !ok {
			return nil, fmt.Errorf("rule[%d] (%s) 'createAction' is not a function", i, name)
		}

		loaded = append(loaded, jsRule{
			name:     name,
			ruleID:   ruleID,
			checkFn:  checkFn,
			actionFn: actionFn,
		})
	}

	return loaded, nil
}

// EvaluateRules runs all rules against the context and returns triggered actions
func (e *JSRuleExecutor) EvaluateRules(ctx RuleContext, force bool) ([]actionmanager.CreateActionRequest, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.vm == nil || len(e.rules) == 0 {
		logger.Warnf("EvaluateRules called with no rules loaded (vm=%v, rules=%d)", e.vm != nil, len(e.rules))
		return nil, nil
	}

	ctxVal := e.vm.ToValue(ctx.ToMap())

	var actions []actionmanager.CreateActionRequest

	for _, rule := range e.rules {
		if !force {
			// Call check function. Guarded: a rule that will not terminate
			// must cost one budget and be skipped, not wedge the daemon.
			result, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
				return rule.checkFn(goja.Undefined(), ctxVal)
			})
			if err != nil {
				logger.Errorf("JS rule '%s' check error: %v", rule.name, err)
				continue
			}

			if !result.ToBoolean() {
				continue
			}
		}

		// Call createAction function, and read its result, inside one budget:
		// Export runs whatever getters the returned object defines.
		var actionObj interface{}
		_, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
			actionResult, aerr := rule.actionFn(goja.Undefined(), ctxVal)
			if aerr != nil {
				return nil, aerr
			}
			actionObj = actionResult.Export()
			return nil, nil
		})
		if err != nil {
			logger.Errorf("JS rule '%s' createAction error: %v", rule.name, err)
			continue
		}

		actionMap, ok := actionObj.(map[string]interface{})
		if !ok {
			logger.Errorf("JS rule '%s' createAction did not return an object", rule.name)
			continue
		}

		logger.Debugf("JS rule '%s' createAction returned: %+v", rule.name, actionMap)

		action := actionmanager.CreateActionRequest{
			Title:       jsmap.String(actionMap, "title"),
			Type:        jsmap.String(actionMap, "type"),
			Description: jsmap.String(actionMap, "description"),
			Status:      jsmap.String(actionMap, "status"),
			Priority:    jsmap.Int(actionMap, "priority"),
			AssignedTo:  jsmap.String(actionMap, "assignedTo"),
			IssuedBy:    jsmap.String(actionMap, "issuedBy"),
			Trigger:     jsmap.String(actionMap, "trigger"),
		}

		if action.Status == "" {
			action.Status = "New"
		}
		if action.IssuedBy == "" {
			action.IssuedBy = "ActionGenerator"
		}
		if action.AssignedTo == "" {
			action.AssignedTo = "User"
		}

		// What comes back is whatever the model wrote, reaching a database and
		// then every screen that lists actions. Nothing downstream constrains
		// it: ActionManager checks that title, type and status are non-empty
		// and nothing about what they contain.
		if err := validateAction(&action, rule.name); err != nil {
			logger.Errorf("JS rule '%s' produced an invalid action, skipping it: %v", rule.name, err)
			continue
		}

		actions = append(actions, action)
	}

	return actions, nil
}

// mockOrganisation is the empty organisation with the given fields set. Built
// from EmptyOrganisation so a scenario always carries the complete key set —
// a hand-written literal here would go stale the next time a field is added,
// and a rule reading the missing one would fail validation for no reason.
func mockOrganisation(overrides map[string]interface{}) map[string]interface{} {
	org := EmptyOrganisation()
	for k, v := range overrides {
		org[k] = v
	}
	return org
}

// mockSettings is the settings shape with each AI assignment stated explicitly.
func mockSettings(research, chat, dashboard string, providers int, language string) map[string]interface{} {
	settings := SettingsToMap(&systemmanager.UserSettings{
		AI:       systemmanager.UserAISettings{Research: research, Chat: chat, Dashboard: dashboard},
		Language: language,
	})
	settings["ai"].(map[string]interface{})["personalProviderCount"] = providers
	return settings
}

// mockQuestionnaires is a questionnaire summary with each state stated explicitly.
func mockQuestionnaires(impact systemmanager.QuestionnaireImpact, readiness systemmanager.QuestionnaireReadiness,
	strategy systemmanager.QuestionnaireStrategy) map[string]interface{} {
	return QuestionnairesToMap(&systemmanager.Questionnaires{
		NIS2Impact: impact, NIS2Readiness: readiness, CyberStrategy: strategy,
	})
}

func floatPtr(v float64) *float64 { return &v }

// registeredUserScenario is mock scenario 3 — a registered user with assets,
// capabilities, a filled-in organisation and an AI provider — with the given
// questionnaire state. A constructor rather than a literal so the scenarios
// built from it share no maps.
func registeredUserScenario(questionnaires map[string]interface{}) RuleContext {
	return RuleContext{
		User: map[string]interface{}{
			"id":                     int64(3),
			"username":               "jdoe",
			"email":                  "jdoe@example.com",
			"role":                   "registered",
			"maxHostAssets":          100,
			"organisationHostAssets": 10,
			"organisationName":       "JDoe Corp",
			"organisationScore":      75.5,
			"createdAt":              "2025-03-01T00:00:00Z",
		},
		// The other side: an organisation that has recorded everything, and
		// a user with providers of their own, one assigned to Research.
		Organisation: mockOrganisation(map[string]interface{}{
			"name": "JDoe Corp", "assetsHosts": 10, "score": 75.5,
			"homepageUrl": "https://jdoe.example", "naceCode": "62.01",
			"employees": 40, "employeesAffiliated": 120,
			"addressStreet": "Hauptstrasse 1", "addressPostalCode": "10115",
			"addressCity": "Berlin", "addressCountry": "DE",
			"companyLegalName": "JDoe Corp GmbH", "vatNumber": "DE123456789", "vatNumberValid": true,
			"itStaffHourlyCost": 120, "currency": "EUR",
			// Providers recorded here and nowhere else, so a rule about
			// them ("no cybersecurity provider") fires in scenarios 1 and 2
			// and not here — which is what keeps it from being flagged as
			// degenerate.
			"itProviders": itProvidersToList([]systemmanager.ITProvider{
				{Name: "SecureOps", HomepageURL: "https://secureops.example", ContactEmail: "soc@secureops.example",
					Types: []string{"cybersecurity"}},
				{Name: "NetCo", HomepageURL: "https://netco.example", ContactEmail: "",
					Types: []string{"network_infrastructure", "general_it_support"}},
			}),
		}),
		Settings:    mockSettings("personal", "personal", "disabled", 2, "de"),
		SourceCount: 2,
		SourceMax:   10,
		Sources: []map[string]interface{}{
			{"id": int64(10), "filename": "network.xml", "sourceType": "nmap", "status": "processed", "createdAt": "2025-05-01T00:00:00Z"},
			{"id": int64(11), "filename": "vuln.nessus", "sourceType": "nessus", "status": "processed", "createdAt": "2025-05-10T00:00:00Z"},
		},
		AssetCount: 4,
		Assets: []map[string]interface{}{
			{"id": int64(10), "name": "db-server", "type": "host", "source": "network.xml", "severity": 9, "score": 95, "createdAt": "2025-05-01T00:00:00Z", "modifiedAt": "2025-05-20T00:00:00Z", "lastSeen": "2025-05-20T00:00:00Z"},
			{"id": int64(11), "name": "app-server", "type": "host", "source": "network.xml", "severity": 5, "score": 60, "createdAt": "2025-05-01T00:00:00Z", "modifiedAt": "2025-05-15T00:00:00Z", "lastSeen": "2025-05-15T00:00:00Z"},
			{"id": int64(12), "name": "old-server", "type": "host", "source": "vuln.nessus", "severity": 8, "score": 70, "createdAt": "2025-01-01T00:00:00Z", "modifiedAt": "2025-01-15T00:00:00Z", "lastSeen": "2024-06-01T00:00:00Z"},
			{"id": int64(13), "name": "printer", "type": "device", "source": "network.xml", "severity": 1, "score": 10, "createdAt": "2025-05-01T00:00:00Z", "modifiedAt": "2025-05-01T00:00:00Z", "lastSeen": "2025-05-01T00:00:00Z"},
		},
		HighestScoreAsset: map[string]interface{}{
			"id": int64(10), "name": "db-server", "type": "host", "source": "network.xml", "severity": 9, "score": 95, "lastSeen": "2025-05-20T00:00:00Z",
		},
		AssetStats: map[string]interface{}{
			"critical": 1, "high": 1, "medium": 1, "low": 1,
			"noThreat": 1, "unmanaged": 2, "total": 4,
		},
		Capabilities: CapabilitySet{Automated: []string{"Asset discovery"}, Manual: []string{"Penetration testing"}},
		ActionCount:  3,
		Actions: []map[string]interface{}{
			{"id": int64(10), "title": "Remediate db-server", "type": "Fix", "status": "New", "priority": 80, "assignedTo": "User", "issuedBy": "ActionGenerator", "trigger": "high score", "createdAt": "2025-05-20T00:00:00Z"},
			{"id": int64(11), "title": "Review old-server", "type": "Info", "status": "InProgress", "priority": 60, "assignedTo": "User", "issuedBy": "ActionGenerator", "trigger": "stale asset", "createdAt": "2025-05-21T00:00:00Z"},
			{"id": int64(12), "title": "Update firmware", "type": "Fix", "status": "Done", "priority": 30, "assignedTo": "User", "issuedBy": "User", "trigger": "manual", "createdAt": "2025-04-01T00:00:00Z"},
		},
		Questionnaires: questionnaires,
	}
}

// MockScenario is one mock context rules are validated against, with the name
// the routine log and the WebUI's Test and Context tabs show it under.
type MockScenario struct {
	Label string
	Ctx   RuleContext
}

// mockScenarioLabels names the scenarios, in order. Kept beside the list they
// name; MockScenarios panics if the two ever differ in length, which a test
// run catches immediately.
var mockScenarioLabels = []string{
	"Anonymous user at source limit",
	"Admin user with empty data",
	"Registered user — NIS2 red, readiness yellow",
	"Registered user — NIS2 orange, readiness red",
	"Registered user — NIS2 yellow, readiness green, strategy expired",
	"Registered user — NIS2 orange, readiness in progress",
}

// MockScenarios returns the mock contexts every generated routine is validated
// against. The WebUI's routine Test and Context tabs show exactly these: a test
// in this package writes them to WebUI/src/generated/actionRuleMockContexts.json
// and fails when that file is stale, so there is no second copy to keep in step.
func MockScenarios() []MockScenario {
	mockContexts := []RuleContext{
		// Scenario 1: Anonymous user with assets, sources at max limit
		{
			User: map[string]interface{}{
				"id":                     int64(1),
				"username":               "testuser",
				"email":                  "test@example.com",
				"role":                   "anonymous",
				"maxHostAssets":          20,
				"organisationHostAssets": 5,
				"organisationName":       "Test Org",
				"organisationScore":      50.0,
				"createdAt":              "2025-01-01T00:00:00Z",
			},
			// An organisation that has recorded almost nothing, and a user with
			// no provider of their own — the state most "you have not
			// configured X" rules are written for.
			Organisation: mockOrganisation(map[string]interface{}{
				"name": "Test Org", "assetsHosts": 10, "score": 50.0,
				"itStaffHourlyCost": 100, "currency": "EUR",
			}),
			// Nothing started: the case "please fill in X" rules are for.
			Questionnaires: EmptyQuestionnaires(),
			Settings:       EmptySettings(),
			SourceCount:    3,
			SourceMax:      3,
			Sources: []map[string]interface{}{
				{"id": int64(1), "filename": "scan1.xml", "sourceType": "nmap", "status": "processed", "createdAt": "2025-06-01T00:00:00Z"},
				{"id": int64(2), "filename": "scan2.xml", "sourceType": "nmap", "status": "processed", "createdAt": "2025-06-15T00:00:00Z"},
				{"id": int64(3), "filename": "scan3.xml", "sourceType": "nessus", "status": "processed", "createdAt": "2025-07-01T00:00:00Z"},
			},
			AssetCount: 2,
			Assets: []map[string]interface{}{
				{"id": int64(1), "name": "test-host", "type": "host", "source": "scan1.xml", "severity": 7, "score": 85, "createdAt": "2025-06-01T00:00:00Z", "modifiedAt": "2025-07-01T00:00:00Z", "lastSeen": "2025-07-01T00:00:00Z"},
				{"id": int64(2), "name": "web-server", "type": "host", "source": "scan2.xml", "severity": 3, "score": 40, "createdAt": "2025-06-15T00:00:00Z", "modifiedAt": "2025-06-15T00:00:00Z", "lastSeen": "2025-06-15T00:00:00Z"},
			},
			HighestScoreAsset: map[string]interface{}{
				"id": int64(1), "name": "test-host", "type": "host", "source": "scan1.xml", "severity": 7, "score": 85, "lastSeen": "2025-07-01T00:00:00Z",
			},
			AssetStats: map[string]interface{}{
				"critical": 1, "high": 1, "medium": 0, "low": 0,
				"noThreat": 0, "unmanaged": 0, "total": 2,
			},
			Capabilities: CapabilitySet{Automated: []string{"Vulnerability detection"}, Manual: []string{}},
			ActionCount:  1,
			Actions: []map[string]interface{}{
				{"id": int64(1), "title": "Existing action", "type": "Info", "status": "New", "priority": 50, "assignedTo": "User", "issuedBy": "ActionGenerator", "trigger": "test", "createdAt": "2025-07-01T00:00:00Z"},
			},
		},
		// Scenario 2: Admin user with no assets, no sources (exercises null/empty
		// paths). Organisation and settings are the empty shapes rather than
		// omitted — a rule reading ctx.settings.ai.research must be exercised
		// against a context where nothing is configured, which is the case it
		// most often exists for.
		{
			User: map[string]interface{}{
				"id":                     int64(2),
				"username":               "admin",
				"email":                  "admin@example.com",
				"role":                   "admin",
				"maxHostAssets":          100,
				"organisationHostAssets": 0,
				"organisationName":       "Admin Org",
				"organisationScore":      0.0,
				"createdAt":              "2024-01-01T00:00:00Z",
			},
			Organisation: mockOrganisation(map[string]interface{}{"name": "Admin Org"}),
			// Impact finished long ago and now expired; the other two started.
			Questionnaires: mockQuestionnaires(
				systemmanager.QuestionnaireImpact{Progress: 100, Answered: 10, Total: 10, Complete: true,
					Level: "green", Size: "small", LastUpdated: "2025-01-10T09:00:00Z", Expired: true},
				systemmanager.QuestionnaireReadiness{Progress: 30, Answered: 11, Total: 37, Score: 18.2,
					Gaps: []string{"B2"}},
				systemmanager.QuestionnaireStrategy{Progress: 20, Answered: 2, Total: 10,
					MissingRequired: []string{"01", "02.1"}},
			),
			Settings:          EmptySettings(),
			SourceCount:       0,
			SourceMax:         10,
			Sources:           []map[string]interface{}{},
			AssetCount:        0,
			Assets:            []map[string]interface{}{},
			HighestScoreAsset: nil,
			AssetStats: map[string]interface{}{
				"critical": 0, "high": 0, "medium": 0, "low": 0,
				"noThreat": 0, "unmanaged": 0, "total": 0,
			},
			Capabilities: CapabilitySet{Automated: []string{}, Manual: []string{}},
			ActionCount:  0,
			Actions:      []map[string]interface{}{},
		},
	}

	// Scenarios 3 to 5: the registered user, built three times so no scenario
	// shares a map with another — goja exposes Go maps by reference, and a rule
	// writing into ctx must not leak into the next scenario. They differ only
	// in the questionnaires, so that between them every Impact level (red,
	// orange, yellow, green) and every Readiness level appears somewhere and is
	// absent somewhere else. Without that, a correct rule such as "the Impact
	// outcome is orange" returns the same answer everywhere and is rejected as
	// degenerate.
	mockContexts = append(mockContexts,
		registeredUserScenario(mockQuestionnaires(
			systemmanager.QuestionnaireImpact{Progress: 100, Answered: 10, Total: 10, Complete: true,
				Level: "red", Size: "large", LastUpdated: "2026-08-20T10:00:00Z"},
			systemmanager.QuestionnaireReadiness{Progress: 100, Answered: 37, Total: 37, Complete: true,
				Score: 58.5, Level: "yellow", UnknownCount: 2, Gaps: []string{"B11", "B4"},
				ReportingScore: floatPtr(35), ReportingCritical: true, LastUpdated: "2026-08-21T10:00:00Z"},
			systemmanager.QuestionnaireStrategy{Progress: 100, Answered: 10, Total: 10, Complete: true,
				MissingRequired: []string{}, LastUpdated: "2026-08-22T10:00:00Z"},
		)),
		registeredUserScenario(mockQuestionnaires(
			systemmanager.QuestionnaireImpact{Progress: 100, Answered: 10, Total: 10, Complete: true,
				Level: "orange", Size: "medium", LastUpdated: "2026-07-01T10:00:00Z"},
			// Complete but out of date, so "the Readiness assessment has expired"
			// is true somewhere — no other scenario has an expired Readiness.
			systemmanager.QuestionnaireReadiness{Progress: 100, Answered: 37, Total: 37, Complete: true,
				Score: 31.2, Level: "red", UnknownCount: 6, Gaps: []string{"B1", "B2", "B4"},
				ReportingScore: floatPtr(60), LastUpdated: "2025-01-02T10:00:00Z", Expired: true},
			// Complete (every required item present) while progress is below
			// 100 — the case a rule must not confuse with "not complete".
			systemmanager.QuestionnaireStrategy{Progress: 80, Answered: 8, Total: 10, Complete: true,
				MissingRequired: []string{}, LastUpdated: "2026-07-03T10:00:00Z"},
		)),
		registeredUserScenario(mockQuestionnaires(
			systemmanager.QuestionnaireImpact{Progress: 100, Answered: 10, Total: 10, Complete: true,
				Level: "yellow", Size: "small", UnclearTreatedAsYes: true, LastUpdated: "2026-09-01T10:00:00Z"},
			systemmanager.QuestionnaireReadiness{Progress: 100, Answered: 37, Total: 37, Complete: true,
				Score: 81.5, Level: "green", Gaps: []string{},
				ReportingScore: floatPtr(90), LastUpdated: "2026-09-02T10:00:00Z"},
			systemmanager.QuestionnaireStrategy{Progress: 100, Answered: 10, Total: 10, Complete: true,
				MissingRequired: []string{}, LastUpdated: "2025-01-03T10:00:00Z", Expired: true},
		)),
		// Directly affected by NIS2 (Impact complete, orange) while Readiness is
		// still being filled in. Without it no scenario is "affected and not
		// ready yet" — the situation rules about Readiness are written for — and
		// a correct rule for it returned false everywhere.
		registeredUserScenario(mockQuestionnaires(
			systemmanager.QuestionnaireImpact{Progress: 100, Answered: 10, Total: 10, Complete: true,
				Level: "orange", Size: "medium", LastUpdated: "2026-09-05T10:00:00Z"},
			systemmanager.QuestionnaireReadiness{Progress: 46, Answered: 17, Total: 37, Score: 27.4,
				UnknownCount: 3, Gaps: []string{"B2", "B11"},
				ReportingScore: floatPtr(25), ReportingCritical: true},
			systemmanager.QuestionnaireStrategy{Progress: 40, Answered: 4, Total: 10,
				MissingRequired: []string{"01"}, LastUpdated: "2026-09-06T10:00:00Z"},
		)),
	)

	if len(mockContexts) != len(mockScenarioLabels) {
		panic(fmt.Sprintf("jsruntime: %d mock contexts but %d labels", len(mockContexts), len(mockScenarioLabels)))
	}
	out := make([]MockScenario, len(mockContexts))
	for i, c := range mockContexts {
		out[i] = MockScenario{Label: mockScenarioLabels[i], Ctx: c}
	}
	return out
}

// ScenarioDecision is what one rule's check() returned in one mock scenario.
type ScenarioDecision struct {
	Label string
	// "true", "false", or "error" when check() threw.
	Result string
}

// RuleValidation is the validation record for one rule.
type RuleValidation struct {
	Name      string
	Decisions []ScenarioDecision
	// The ctx paths check() or createAction() read in any scenario, as leaves:
	// the values tested, not the objects walked through to reach them.
	FieldsRead []string
	// Paths read that exist in no scenario — a typo or an invented field.
	Missing []string
}

// ValidationReport is the outcome of ValidateWithMockCtxReport.
type ValidationReport struct {
	// Why the routine must not be stored as passed. Empty means it passes.
	Errors []error
	// Things worth knowing that do not fail the routine.
	Notes []string
	// Per rule, in load order: what check() decided in each scenario and what
	// it read. Written to the routine log so a result explains itself.
	Rules []RuleValidation
}

// Log renders the report for the routine log.
func (r ValidationReport) Log() string {
	var b strings.Builder
	for i, err := range r.Errors {
		fmt.Fprintf(&b, "Validation error %d: %v\n", i+1, err)
	}
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "Note: %s\n", note)
	}
	if len(r.Rules) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Validation record — check() in each mock scenario:\n")
		for _, rule := range r.Rules {
			fmt.Fprintf(&b, "  rule '%s'\n", rule.Name)
			for i, d := range rule.Decisions {
				fmt.Fprintf(&b, "    %d %-66s %s\n", i+1, d.Label, d.Result)
			}
			if len(rule.FieldsRead) > 0 {
				fmt.Fprintf(&b, "    fields read: %s\n", strings.Join(ctxPaths(rule.FieldsRead), ", "))
			} else {
				b.WriteString("    fields read: none\n")
			}
			if len(rule.Missing) > 0 {
				fmt.Fprintf(&b, "    fields that do not exist: %s\n", strings.Join(ctxPaths(rule.Missing), ", "))
			}
		}
	}
	return b.String()
}

func ctxPaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "ctx." + p
	}
	return out
}

// ValidateWithMockCtxReport runs every rule against every mock scenario with
// its reads traced, and judges the rule from what it decided AND what it read.
//
// Deciding from the return value alone cannot tell a broken rule from a correct
// one no scenario happens to satisfy: both return the same answer everywhere.
// What the rule read separates them:
//
//   - A read of a field that exists in no scenario is an error, named — a typo
//     or an invented field, the thing the old "does the condition match the
//     field names" message was guessing at.
//   - Always the same answer while reading nothing that differs between the
//     scenarios is an error: `return false` reads nothing, and a condition over
//     fields that never vary cannot be expressing a trigger.
//   - Always false while reading fields that DO differ is accepted with a note:
//     the rule is testing real data, and no scenario meets its condition. That
//     was rule 18 — "Impact complete and not green, Readiness incomplete" — which
//     no scenario combined until scenario 6.
//   - Always true is rejected whatever it reads. It would fire for every user,
//     and an action is only withdrawn when its rule stops matching, so it could
//     never be cleared.
//
// ruleTally is what one rule did across every mock scenario: what it decided,
// and what it read to decide it. Both halves matter — see
// ValidateWithMockCtxReport.
type ruleTally struct {
	decisions  []ScenarioDecision
	results    []bool
	threw      bool
	checkReads map[string]bool
	allReads   map[string]bool
	present    map[string]bool
	candidates map[string]bool
}

func (e *JSRuleExecutor) ValidateWithMockCtxReport() ValidationReport {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var report ValidationReport
	if e.vm == nil || len(e.rules) == 0 {
		return report
	}

	scenarios := MockScenarios()
	plains := make([]map[string]interface{}, len(scenarios))
	for i, sc := range scenarios {
		plains[i] = sc.Ctx.ToMap()
	}

	tallies := make([]*ruleTally, len(e.rules))
	for i := range e.rules {
		tallies[i] = &ruleTally{checkReads: map[string]bool{}, allReads: map[string]bool{},
			present: map[string]bool{}, candidates: map[string]bool{}}
	}
	absorb := func(t *ruleTally, tr *jsctxtrace.Trace) {
		missing := map[string]bool{}
		for _, p := range tr.Missing() {
			missing[p] = true
			t.candidates[p] = true
		}
		for _, p := range tr.Read() {
			t.allReads[p] = true
			if !missing[p] {
				t.present[p] = true
			}
		}
	}

	e.runScenarios(scenarios, plains, tallies, absorb, &report)
	e.judgeRules(tallies, plains, &report)

	return report
}

// runScenarios evaluates every rule against every mock scenario, recording what
// each decided and what it read. Failures are reported as they happen; the
// judging of what the decisions MEAN is judgeRules' job.
func (e *JSRuleExecutor) runScenarios(
	scenarios []MockScenario,
	plains []map[string]interface{},
	tallies []*ruleTally,
	absorb func(*ruleTally, *jsctxtrace.Trace),
	report *ValidationReport,
) {
	for si, sc := range scenarios {
		// One traced context per scenario, shared by every rule and by check()
		// and createAction() — as production evaluates a routine's rules against
		// one ctx. Reset attributes the reads to each rule in turn.
		tr := jsctxtrace.New()
		ctxVal := tr.Wrap(e.vm, plains[si])
		for ri, rule := range e.rules {
			t := tallies[ri]
			tr.Reset()

			checkResult, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
				return rule.checkFn(goja.Undefined(), ctxVal)
			})
			for _, p := range tr.Read() {
				t.checkReads[p] = true
			}
			if err != nil {
				absorb(t, tr)
				t.threw = true
				t.decisions = append(t.decisions, ScenarioDecision{Label: sc.Label, Result: "error"})
				report.Errors = append(report.Errors, fmt.Errorf("rule '%s' check() failed in mock scenario %d (%s): %w",
					rule.name, si+1, sc.Label, err))
				continue
			}
			fired := checkResult.ToBoolean()
			t.results = append(t.results, fired)
			t.decisions = append(t.decisions, ScenarioDecision{Label: sc.Label, Result: fmt.Sprintf("%v", fired)})

			if fired {
				if _, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
					return rule.actionFn(goja.Undefined(), ctxVal)
				}); err != nil {
					report.Errors = append(report.Errors, fmt.Errorf("rule '%s' createAction() failed in mock scenario %d (%s): %w",
						rule.name, si+1, sc.Label, err))
				}
			}
			absorb(t, tr)
		}
	}

}

// judgeRules decides, from the tallies, which rules are broken.
//
// A rule is judged on what it decided AND what it read. Deciding from the
// return value alone cannot tell a broken rule from a correct one that no
// scenario happens to satisfy: both answer the same everywhere. What the rule
// read separates them.
func (e *JSRuleExecutor) judgeRules(tallies []*ruleTally, plains []map[string]interface{}, report *ValidationReport) {
	for ri, rule := range e.rules {
		t := tallies[ri]
		rec := RuleValidation{Name: rule.name, Decisions: t.decisions, FieldsRead: jsctxtrace.Leaves(setToSortedBool(t.allReads))}
		for p := range t.candidates {
			if !t.present[p] {
				rec.Missing = append(rec.Missing, p)
			}
		}
		sort.Strings(rec.Missing)
		report.Rules = append(report.Rules, rec)

		if len(rec.Missing) > 0 {
			report.Errors = append(report.Errors, fmt.Errorf(
				"rule '%s' reads %s, which does not exist in the context", rule.name, strings.Join(ctxPaths(rec.Missing), ", ")))
		}
		// A rule that threw somewhere has already been reported; judging it on
		// a partial set of answers would turn one failure into two.
		if t.threw || len(t.results) < 2 || !allSame(t.results) {
			continue
		}
		varying := varyingPaths(setToSortedBool(t.checkReads), plains)
		if t.results[0] {
			report.Errors = append(report.Errors, fmt.Errorf(
				"rule '%s' check() returned true in every mock scenario: it would fire for every user "+
					"and could never be cleared. Does the condition actually test the context?", rule.name))
			continue
		}
		if len(varying) == 0 {
			read := "nothing"
			if len(t.checkReads) > 0 {
				read = "only fields that are the same in every scenario (" + strings.Join(ctxPaths(setToSortedBool(t.checkReads)), ", ") + ")"
			}
			report.Errors = append(report.Errors, fmt.Errorf(
				"rule '%s' check() returned false in every mock scenario and reads %s: it would never fire. "+
					"Does the condition test the context at all?", rule.name, read))
			continue
		}
		report.Notes = append(report.Notes, fmt.Sprintf(
			"rule '%s' check() returned false in every mock scenario, but it reads fields that differ between them "+
				"(%s), so it is accepted: no scenario happens to meet its condition. "+
				"Check it against the scenarios in the routine's Context tab.",
			rule.name, strings.Join(ctxPaths(varying), ", ")))
	}
}

func allSame(results []bool) bool {
	for _, r := range results[1:] {
		if r != results[0] {
			return false
		}
	}
	return true
}

func setToSortedBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// varyingPaths returns the read paths whose value is not the same in every
// scenario. A path through an array ("assets[].severity") is compared at the
// array itself, since that is the value the rule's reading depends on.
func varyingPaths(paths []string, plains []map[string]interface{}) []string {
	var out []string
	for _, p := range paths {
		base := p
		if i := strings.Index(base, "[]"); i >= 0 {
			base = strings.TrimSuffix(base[:i], ".")
		}
		first := ""
		for i, plain := range plains {
			sig := valueSignature(plain, base)
			if i == 0 {
				first = sig
			} else if sig != first {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func valueSignature(root map[string]interface{}, path string) string {
	var cur interface{} = root
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "<absent>"
		}
		v, ok := m[key]
		if !ok {
			return "<absent>"
		}
		cur = v
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return fmt.Sprintf("%v", cur)
	}
	return string(b)
}

// RuleIDs returns the ruleId of every loaded rule.
func (e *JSRuleExecutor) RuleIDs() []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]int64, len(e.rules))
	for i, r := range e.rules {
		ids[i] = r.ruleID
	}
	return ids
}

// RuleCount returns the number of loaded rules
func (e *JSRuleExecutor) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// The vocabularies an action's fields are drawn from.
//
// Taken from where each is actually defined rather than invented here: the
// action types are the options in the WebUI's Action Rule dialog, and the two
// role fields are the sets documented on ActionManager's Action model and in
// the WebUI's ActionAssignedTo/ActionIssuedBy types. A value outside them is a
// broken rule — the UI has no way to render it and no filter selects it.
var (
	validActionTypes  = map[string]bool{"Fix": true, "Info": true, "Support": true}
	validActionStatus = map[string]bool{"New": true, "Done": true}
	validAssignedTo   = map[string]bool{"User": true, "Support": true, "ActionExecutor": true}
	validIssuedBy     = map[string]bool{"User": true, "Support": true, "ActionGenerator": true}
)

// maxCallStackSize bounds recursion in routine code. Deep enough that no
// reasonable rule reaches it — these are flat condition checks, not algorithms
// — and shallow enough that runaway recursion is an error rather than an
// out-of-memory kill.
const maxCallStackSize = 2048

// Bounds on the free-text an action carries.
//
// Generous on purpose: these exist to stop unbounded growth reaching the
// database and the UI, not to enforce a house style. A routine builds these
// strings by interpolating context — an asset name, an organisation — so the
// length is a property of the data as much as of the rule.
const (
	MaxActionTitleLen       = 200
	MaxActionDescriptionLen = 4000
	MaxActionTriggerLen     = 500

	MinActionPriority = 0
	MaxActionPriority = 100
)

// validateAction checks what a routine returned before it becomes a real
// action, and normalises what can be normalised.
//
// Two different policies, deliberately:
//
//   - Over-length text is TRUNCATED. The length comes from the data the rule
//     interpolated, so discarding an otherwise-correct action because an asset
//     had a long name would lose something the user needs over a cosmetic
//     problem.
//   - A field outside its vocabulary, or a priority outside its range, is an
//     ERROR and the action is dropped. Those are the rule being wrong rather
//     than the data being awkward, and a value the UI cannot render is worse
//     than no action at all.
func validateAction(a *actionmanager.CreateActionRequest, ruleName string) error {
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("title is empty")
	}
	if !validActionTypes[a.Type] {
		return fmt.Errorf("type %q is not one of Fix, Info, Support", a.Type)
	}
	if !validActionStatus[a.Status] {
		return fmt.Errorf("status %q is not one of New, Done", a.Status)
	}
	if !validAssignedTo[a.AssignedTo] {
		return fmt.Errorf("assignedTo %q is not one of User, Support, ActionExecutor", a.AssignedTo)
	}
	if !validIssuedBy[a.IssuedBy] {
		return fmt.Errorf("issuedBy %q is not one of User, Support, ActionGenerator", a.IssuedBy)
	}
	if a.Priority < MinActionPriority || a.Priority > MaxActionPriority {
		return fmt.Errorf("priority %d is outside %d-%d", a.Priority, MinActionPriority, MaxActionPriority)
	}

	a.Title = truncate(a.Title, MaxActionTitleLen, ruleName, "title")
	a.Description = truncate(a.Description, MaxActionDescriptionLen, ruleName, "description")
	a.Trigger = truncate(a.Trigger, MaxActionTriggerLen, ruleName, "trigger")
	return nil
}

// truncate shortens s to max runes, saying so in the log.
//
// Counts runes rather than bytes: cutting a UTF-8 string at a byte offset can
// split a character and put an invalid sequence into the database.
func truncate(s string, max int, ruleName, field string) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	logger.Warnf("JS rule '%s': %s was %d characters, truncated to %d", ruleName, field, len(runes), max)
	return string(runes[:max])
}
