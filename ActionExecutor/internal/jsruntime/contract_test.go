// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that only bridge routines load: a routine without
// `var routineContract = 2;` was written for the api object this module no
// longer has, and is refused with ErrPredatesBridge however valid it is.

package jsruntime

import (
	"errors"
	"testing"
)

func TestARoutineWithoutTheContractIsRefused(t *testing.T) {
	for name, code := range map[string]string{
		"no declaration": `var executors = [{ name: "e", ruleId: 4, run: function (api) { return { status: "success" }; } }];`,
		"contract 1":     `var routineContract = 1; var executors = [{ name: "e", ruleId: 4, run: function (api) { return {}; } }];`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := New()
			if err := rt.LoadFromString(code); !errors.Is(err, ErrPredatesBridge) {
				t.Fatalf("err = %v, want ErrPredatesBridge", err)
			}
			if _, err := rt.Run(nil, 0); err == nil {
				t.Error("a refused routine can still be run")
			}
		})
	}
}

func TestABridgeRoutineLoads(t *testing.T) {
	rt := New()
	if err := rt.LoadFromString(`var routineContract = 2;
var executors = [{ name: "e", ruleId: 4, run: function (api) { return { status: "success" }; } }];`); err != nil {
		t.Fatalf("load: %v", err)
	}
	if ids := rt.RuleIDs(); len(ids) != 1 || ids[0] != 4 {
		t.Errorf("rule ids %v", ids)
	}
}
