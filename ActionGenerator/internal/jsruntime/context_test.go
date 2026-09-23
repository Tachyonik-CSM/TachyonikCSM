// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the shape of the context a rule is evaluated against, and for the
// validation that rejects a routine before it is trusted.
//
// The shape matters because a rule can only be written about something it can
// read: a field that is absent rather than empty gets mapped by the AI onto
// whatever nearby field does exist, which is how a rule about the Research
// provider came to test a capability name instead. So every key is present
// whether or not the installation has recorded a value, the empty shape carries
// exactly the keys the populated one does, and the aliases older stored routines
// read still resolve.
//
// The validation tests pin the other half: a check() that answers the same way
// whatever it is given is not a trigger however plausible its code looks, and a
// routine that throws is reported once as a runtime error rather than twice.

package jsruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"tachyonik/actiongenerator/internal/systemmanager"
)

// evalAgainst runs one expression with the given context as `ctx` and returns
// what it evaluated to. A thrown TypeError comes back as an error, which is the
// failure this file mostly exists to catch.
func evalAgainst(t *testing.T, ctx RuleContext, expr string) goja.Value {
	t.Helper()
	vm := goja.New()
	if err := vm.Set("ctx", ctx.ToMap()); err != nil {
		t.Fatalf("bind ctx: %v", err)
	}
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("evaluating %q against the context failed: %v", expr, err)
	}
	return v
}

// The case that prompted all of this: a rule about the Research provider had no
// field to read, so the AI mapped it onto a capability name instead.
func TestContextExposesResearchAssignment(t *testing.T) {
	unconfigured := RuleContext{Settings: EmptySettings()}
	if v := evalAgainst(t, unconfigured, `ctx.settings.ai.research === "none"`); !v.ToBoolean() {
		t.Error("a user with nothing configured does not report research as none")
	}
	if v := evalAgainst(t, unconfigured, `ctx.settings.ai.personalProviderCount`); v.ToInteger() != 0 {
		t.Errorf("personalProviderCount = %v for a user with no providers, want 0", v)
	}

	configured := RuleContext{Settings: mockSettings("personal", "internal", "internal", 2, "de")}
	if v := evalAgainst(t, configured, `ctx.settings.ai.research === "none"`); v.ToBoolean() {
		t.Error("a user with Research assigned still reports none")
	}
	// The distinction a rule needs to give different advice: no providers at
	// all, versus providers that are simply not assigned to Research.
	hasProviders := RuleContext{Settings: mockSettings("none", "internal", "internal", 3, "")}
	if v := evalAgainst(t, hasProviders,
		`ctx.settings.ai.research === "none" && ctx.settings.ai.personalProviderCount > 0`); !v.ToBoolean() {
		t.Error("cannot distinguish 'has providers, none assigned' from 'has no providers'")
	}
}

// A rule about something the organisation has not recorded can only be written
// if the empty value is readable rather than undefined.
func TestContextExposesOrganisationRecord(t *testing.T) {
	empty := RuleContext{Organisation: EmptyOrganisation()}
	for _, expr := range []string{
		`ctx.organisation.homepageUrl === ""`,
		`ctx.organisation.naceCode === ""`,
		`ctx.organisation.addressCity === ""`,
		`ctx.organisation.employeesAffiliated === 0`,
		`ctx.organisation.vatNumberValid === false`,
	} {
		if v := evalAgainst(t, empty, expr); !v.ToBoolean() {
			t.Errorf("%s was not true for an organisation that has recorded nothing", expr)
		}
	}

	filled := RuleContext{Organisation: OrganisationToMap(&systemmanager.Organisation{
		Name: "JDoe Corp", HomepageURL: "https://jdoe.example", NaceCode: "62.01",
		EmployeesAffiliated: 120, AddressCountry: "DE",
	})}
	if v := evalAgainst(t, filled, `ctx.organisation.naceCode`); v.String() != "62.01" {
		t.Errorf("naceCode = %q, want 62.01", v.String())
	}
	if v := evalAgainst(t, filled, `ctx.organisation.homepageUrl.indexOf("https://") === 0`); !v.ToBoolean() {
		t.Error("homepageUrl did not survive into the context")
	}
}

// The empty shape must carry every key the populated one does. If it does not,
// a rule works when the organisation was fetched and throws when it was not —
// the failure mode this whole change is about.
func TestEmptyOrganisationHasEveryPopulatedKey(t *testing.T) {
	populated := OrganisationToMap(&systemmanager.Organisation{Name: "x"})
	empty := EmptyOrganisation()
	if len(populated) != len(empty) {
		t.Fatalf("populated has %d keys, empty has %d", len(populated), len(empty))
	}
	for key := range populated {
		if _, ok := empty[key]; !ok {
			t.Errorf("key %q is missing from EmptyOrganisation", key)
		}
	}
}

// Routines already stored in the database read ctx.user.organisation* — the
// aliases must keep working. The generator sets them from the same map, so this
// pins the contract the prompt still documents.
func TestLegacyOrganisationAliasesRemain(t *testing.T) {
	org := OrganisationToMap(&systemmanager.Organisation{Name: "Acme", AssetsHosts: 12, Score: 66})
	ctx := RuleContext{
		Organisation: org,
		User: map[string]interface{}{
			"organisationName":       org["name"],
			"organisationHostAssets": org["assetsHosts"],
			"organisationScore":      org["score"],
		},
	}
	if v := evalAgainst(t, ctx,
		`ctx.user.organisationHostAssets === ctx.organisation.assetsHosts &&
		 ctx.user.organisationName === ctx.organisation.name &&
		 ctx.user.organisationScore === ctx.organisation.score`); !v.ToBoolean() {
		t.Error("the legacy ctx.user.organisation* aliases disagree with ctx.organisation")
	}
}

// A context assembled without them must still be safe to read at the top level.
// Nested reads are the generator's job to guarantee, which is why it never
// leaves either map nil — but a bare RuleContext must not hand a rule undefined.
func TestToMapNeverYieldsUndefinedTopLevelObjects(t *testing.T) {
	bare := RuleContext{}
	for _, expr := range []string{
		`typeof ctx.organisation === "object" && ctx.organisation !== null`,
		`typeof ctx.settings === "object" && ctx.settings !== null`,
		`typeof ctx.user === "object" && ctx.user !== null`,
		`typeof ctx.assetStats === "object" && ctx.assetStats !== null`,
		`typeof ctx.capabilities.automated === "object"`,
	} {
		if v := evalAgainst(t, bare, expr); !v.ToBoolean() {
			t.Errorf("%s was false on an empty RuleContext", expr)
		}
	}
	// highestScoreAsset is the deliberate exception: null is a real state a
	// rule is required to check for, so it must not be defaulted away.
	if v := evalAgainst(t, bare, `ctx.highestScoreAsset === null`); !v.ToBoolean() {
		t.Error("highestScoreAsset should stay null when no asset is scored")
	}
}

// The mock contexts that gate every generated routine must carry the same shape
// the runtime does. This is what failed last time: fields were added to the
// real context and not to a mock, so correct code failed validation.
func TestMockContextsCarryTheFullShape(t *testing.T) {
	e := New(5 * time.Second)
	code := `
		var rules = [{
			name: "reads everything", ruleId: 1,
			check: function (ctx) {
				return ctx.settings.ai.research === "none"
					&& ctx.settings.ai.personalProviderCount >= 0
					&& typeof ctx.settings.language === "string"
					&& typeof ctx.organisation.homepageUrl === "string"
					&& typeof ctx.organisation.naceCode === "string"
					&& typeof ctx.organisation.employeesAffiliated === "number"
					&& typeof ctx.organisation.vatNumberValid === "boolean";
			},
			createAction: function (ctx) {
				return { title: "for " + ctx.organisation.name, type: "Info", priority: 10 };
			}
		}];
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if errs := e.ValidateWithMockCtxReport().Errors; len(errs) > 0 {
		t.Fatalf("a rule reading the full context failed mock validation: %v", errs)
	}
}

// A check() that answers the same way whatever it is given is not a trigger,
// and validation used to wave both shapes through because neither throws.
func TestValidateRejectsDegenerateRules(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{{
		name: "always true",
		body: "return true;",
		// The worse of the two: it fires for everyone, and the sweep only ever
		// removes an action when the rule STOPS matching — so it never does.
		want: "returned true in every mock scenario",
	}, {
		name: "always false",
		body: "return false;",
		want: "returned false in every mock scenario and reads nothing",
	}, {
		// The realistic way this happens: a field that does not exist reads as
		// undefined, and the comparison is quietly always false. The error now
		// names the field instead of asking whether the names match.
		name: "condition on a misspelled field",
		body: `return ctx.settings.ai.reserach === "none";`,
		want: "reads ctx.settings.ai.reserach, which does not exist",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := New(5 * time.Second)
			code := `var rules = [{ name: "r", ruleId: 1, check: function (ctx) { ` + c.body +
				` }, createAction: function () { return { title: "t" }; } }];`
			if err := e.LoadFromString(code); err != nil {
				t.Fatalf("LoadFromString: %v", err)
			}
			errs := e.ValidateWithMockCtxReport().Errors
			if len(errs) == 0 {
				t.Fatalf("a degenerate rule passed validation")
			}
			found := false
			for _, err := range errs {
				if strings.Contains(err.Error(), c.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("errors = %v, want one containing %q", errs, c.want)
			}
		})
	}
}

// A rule that genuinely discriminates must pass. Rule 14's real condition is
// the case that matters: true for the scenarios with nothing configured, false
// for the one with a provider assigned.
func TestValidateAcceptsDiscriminatingRule(t *testing.T) {
	e := New(5 * time.Second)
	code := `
		var rules = [{
			name: "activate-personal-ai-provider-for-research",
			ruleId: 14,
			check: function (ctx) { return ctx.settings.ai.research === "none"; },
			createAction: function () { return { title: "Assign a Research provider" }; }
		}];
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	if errs := e.ValidateWithMockCtxReport().Errors; len(errs) > 0 {
		t.Fatalf("a correct rule was rejected: %v", errs)
	}
}

// A rule that throws is reported once, as a runtime error — the degeneracy
// check must not pile a second complaint on top of an incomplete result set.
func TestValidateDoesNotDoubleReportThrowingRule(t *testing.T) {
	e := New(5 * time.Second)
	code := `var rules = [{ name: "boom", ruleId: 1,
		check: function () { throw new Error("no"); },
		createAction: function () { return { title: "t" }; } }];`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	errs := e.ValidateWithMockCtxReport().Errors
	for _, err := range errs {
		if strings.Contains(err.Error(), "every mock context") {
			t.Errorf("throwing rule also reported as degenerate: %v", errs)
		}
	}
}

// The IT service providers arrive as an array a rule can use directly. The
// empty case is the one that matters most: a rule looking for an organisation
// with no cybersecurity provider has to reach .some() on an empty array, not
// fail on null — and each provider's types must be an array too.
func TestContextExposesITProviders(t *testing.T) {
	empty := RuleContext{Organisation: EmptyOrganisation()}
	if v := evalAgainst(t, empty,
		`Array.isArray(ctx.organisation.itProviders) && ctx.organisation.itProviders.length === 0`); !v.ToBoolean() {
		t.Error("an organisation with no providers did not get an empty array")
	}

	filled := RuleContext{Organisation: OrganisationToMap(&systemmanager.Organisation{
		Name: "JDoe Corp",
		ITProviders: []systemmanager.ITProvider{
			{Name: "SecureOps", ContactEmail: "soc@secureops.example", Types: []string{"cybersecurity"}},
			{Name: "Bare", Types: nil},
		},
	})}
	for _, expr := range []string{
		`ctx.organisation.itProviders.length === 2`,
		`ctx.organisation.itProviders.some(function (p) { return p.types.indexOf("cybersecurity") >= 0; })`,
		`ctx.organisation.itProviders[0].contactEmail === "soc@secureops.example"`,
		// A provider stored without types still has an array to call indexOf on.
		`Array.isArray(ctx.organisation.itProviders[1].types) && ctx.organisation.itProviders[1].types.length === 0`,
		`ctx.organisation.itProviders[0].notes === undefined`,
	} {
		if v := evalAgainst(t, filled, expr); !v.ToBoolean() {
			t.Errorf("%s was not true", expr)
		}
	}
}

// The rule this feature was asked for must survive validation. It can only do
// that if the mock contexts disagree about providers — scenario 3 has a
// cybersecurity provider, the other two have none. Were providers absent from
// every mock, this correct rule would be rejected as degenerate.
func TestValidateAcceptsITProviderRule(t *testing.T) {
	e := New(5 * time.Second)
	code := `
		var rules = [{
			name: "record-a-cybersecurity-provider",
			ruleId: 40,
			check: function (ctx) {
				return !ctx.organisation.itProviders.some(function (p) {
					return p.types.indexOf("cybersecurity") >= 0;
				});
			},
			createAction: function () { return { title: "Record your cybersecurity provider" }; }
		}];
	`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	if errs := e.ValidateWithMockCtxReport().Errors; len(errs) > 0 {
		t.Fatalf("a correct provider rule was rejected: %v", errs)
	}
}

// ctx.questionnaires must have the same keys, nested, whether the summary was
// fetched or not — the empty shape is what a rule meets when SystemManager is
// unreachable, and a missing key there is a TypeError in production.
func TestEmptyQuestionnairesHaveEveryPopulatedKey(t *testing.T) {
	score := 42.0
	populated := QuestionnairesToMap(&systemmanager.Questionnaires{
		NIS2Readiness: systemmanager.QuestionnaireReadiness{ReportingScore: &score, Gaps: []string{"B1"}},
		CyberStrategy: systemmanager.QuestionnaireStrategy{MissingRequired: []string{"01"}},
	})
	empty := EmptyQuestionnaires()
	for name, section := range populated {
		emptySection, ok := empty[name].(map[string]interface{})
		if !ok {
			t.Fatalf("EmptyQuestionnaires has no %q", name)
		}
		for key := range section.(map[string]interface{}) {
			if _, ok := emptySection[key]; !ok {
				t.Errorf("EmptyQuestionnaires().%s is missing %q", name, key)
			}
		}
	}
	if (RuleContext{}).ToMap()["questionnaires"] == nil {
		t.Error("a context with no questionnaires set did not get the empty shape")
	}
}

// null never reaches a rule as the reporting score: in JavaScript `null < 50`
// is true, so a rule about a low score would fire for an organisation that has
// answered nothing. A number and reportingAnswered arrive instead.
func TestReportingScoreIsNeverNull(t *testing.T) {
	ctx := RuleContext{Questionnaires: EmptyQuestionnaires()}
	for _, expr := range []string{
		`typeof ctx.questionnaires.nis2Readiness.reportingScore === "number"`,
		`ctx.questionnaires.nis2Readiness.reportingAnswered === false`,
		`!(ctx.questionnaires.nis2Readiness.reportingAnswered && ctx.questionnaires.nis2Readiness.reportingScore < 50)`,
		`Array.isArray(ctx.questionnaires.nis2Readiness.gaps)`,
		`Array.isArray(ctx.questionnaires.cyberStrategy.missingRequired)`,
		`ctx.questionnaires.nis2Impact.progress === 0 && ctx.questionnaires.nis2Impact.level === ""`,
	} {
		if v := evalAgainst(t, ctx, expr); !v.ToBoolean() {
			t.Errorf("%s was not true for an empty summary", expr)
		}
	}
}

// The rule this feature was asked for, and the verdict rules that come with
// it. Each must discriminate across the mock scenarios — true somewhere, false
// somewhere — or ValidateWithMockCtx rejects it as degenerate. This is what
// pins the scenarios' questionnaire states: remove a level from them and the
// rule about that level fails here.
func TestValidateAcceptsQuestionnaireRules(t *testing.T) {
	checks := map[string]string{
		"impact progress below 100 (rule 17)": `ctx.questionnaires.nis2Impact.progress < 100`,
		"impact not complete":                 `!ctx.questionnaires.nis2Impact.complete`,
		"impact red":                          `ctx.questionnaires.nis2Impact.level === "red"`,
		"impact orange":                       `ctx.questionnaires.nis2Impact.level === "orange"`,
		"impact yellow":                       `ctx.questionnaires.nis2Impact.level === "yellow"`,
		"impact green":                        `ctx.questionnaires.nis2Impact.level === "green"`,
		"impact directly affected":            `ctx.questionnaires.nis2Impact.level === "red" || ctx.questionnaires.nis2Impact.level === "orange"`,
		"impact expired":                      `ctx.questionnaires.nis2Impact.expired`,
		"impact rests on unclear":             `ctx.questionnaires.nis2Impact.unclearTreatedAsYes`,
		"readiness red":                       `ctx.questionnaires.nis2Readiness.level === "red"`,
		"readiness yellow":                    `ctx.questionnaires.nis2Readiness.level === "yellow"`,
		"readiness green":                     `ctx.questionnaires.nis2Readiness.level === "green"`,
		"readiness score below 50":            `ctx.questionnaires.nis2Readiness.complete && ctx.questionnaires.nis2Readiness.score < 50`,
		"readiness reporting critical":        `ctx.questionnaires.nis2Readiness.reportingCritical`,
		"readiness has gaps":                  `ctx.questionnaires.nis2Readiness.gaps.length > 0`,
		"strategy not complete":               `!ctx.questionnaires.cyberStrategy.complete`,
		"strategy complete but not 100":       `ctx.questionnaires.cyberStrategy.complete && ctx.questionnaires.cyberStrategy.progress < 100`,
		"strategy expired":                    `ctx.questionnaires.cyberStrategy.expired`,
		"readiness expired":                   `ctx.questionnaires.nis2Readiness.expired`,
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			e := New(5 * time.Second)
			code := `var rules = [{ name: "q", ruleId: 50,
				check: function (ctx) { return ` + check + `; },
				createAction: function () { return { title: "t" }; } }];`
			if err := e.LoadFromString(code); err != nil {
				t.Fatalf("LoadFromString: %v", err)
			}
			if errs := e.ValidateWithMockCtxReport().Errors; len(errs) > 0 {
				t.Errorf("rejected: %v", errs)
			}
		})
	}
}

// varyingPaths decides between "no scenario meets this condition" (accepted)
// and "this condition cannot react to anything" (rejected). Every field of the
// real scenarios differs somewhere, so the rejecting branch is exercised here
// with contexts built for it.
func TestVaryingPaths(t *testing.T) {
	plains := []map[string]interface{}{
		{"org": map[string]interface{}{"currency": "EUR", "name": "A"},
			"assets": []map[string]interface{}{{"severity": 1}}},
		{"org": map[string]interface{}{"currency": "EUR", "name": "B"},
			"assets": []map[string]interface{}{{"severity": 9}}},
	}
	got := varyingPaths([]string{"org.currency", "org.name", "assets[].severity", "org.nope"}, plains)
	want := []string{"org.name", "assets[].severity"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("varyingPaths = %v, want %v", got, want)
	}
}

// The rule that prompted read-aware validation: correct, and — before scenario
// 6 — met by no scenario. It must pass, and its record must show what it read.
func TestValidateAcceptsRule18AndRecordsWhatItRead(t *testing.T) {
	e := New(5 * time.Second)
	code := `var rules = [{ name: "complete-your-nis-2-readiness-assessment", ruleId: 18,
		check: function (ctx) {
			return ctx.questionnaires.nis2Impact.progress === 100 &&
			       ctx.questionnaires.nis2Impact.level !== "green" &&
			       ctx.questionnaires.nis2Readiness.progress < 100;
		},
		createAction: function () { return { title: "t" }; } }];`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("rule 18 rejected: %v", report.Errors)
	}
	if len(report.Rules) != 1 {
		t.Fatalf("record has %d rules", len(report.Rules))
	}
	rec := report.Rules[0]
	if len(rec.Decisions) != len(MockScenarios()) {
		t.Errorf("decisions for %d scenarios, want %d", len(rec.Decisions), len(MockScenarios()))
	}
	fired := 0
	for _, d := range rec.Decisions {
		if d.Result == "true" {
			fired++
		}
	}
	if fired == 0 {
		t.Error("no scenario is 'affected, readiness in progress' — scenario 6 is missing or wrong")
	}
	joined := strings.Join(rec.FieldsRead, ",")
	for _, p := range []string{"questionnaires.nis2Impact.level", "questionnaires.nis2Readiness.progress"} {
		if !strings.Contains(joined, p) {
			t.Errorf("fields read %v do not include %s", rec.FieldsRead, p)
		}
	}
	log := report.Log()
	if !strings.Contains(log, "Validation record") || !strings.Contains(log, "fields read: ctx.questionnaires") {
		t.Errorf("log does not carry the record:\n%s", log)
	}
}

// Correct, but reading fields no scenario combines to satisfy it: accepted,
// with a note that says so. Built on a condition no scenario meets (Impact
// green AND Readiness red), rather than relying on a real gap staying open.
func TestValidateAcceptsUnmetConditionWithANote(t *testing.T) {
	e := New(5 * time.Second)
	code := `var rules = [{ name: "unmet", ruleId: 60,
		check: function (ctx) {
			return ctx.questionnaires.nis2Impact.level === "green" &&
			       ctx.questionnaires.nis2Readiness.level === "red";
		},
		createAction: function () { return { title: "t" }; } }];`
	if err := e.LoadFromString(code); err != nil {
		t.Fatalf("LoadFromString: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("a correct but unmet rule was rejected: %v", report.Errors)
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "no scenario happens to meet its condition") {
		t.Errorf("notes = %v", report.Notes)
	}
}
