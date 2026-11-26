// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the order a pass evaluates the rules in (AG-EVAL-28): from the highest
// action priority to the lowest, equal priorities by ascending id.

package aigenerator

import (
	"reflect"
	"testing"

	"tachyonik/actiongenerator/internal/aimanager"
)

func TestRuleOrderIsByPriorityThenID(t *testing.T) {
	ag := &AIActionGenerator{rules: map[int64]*aimanager.ActionRule{
		1: {ID: 1, ActionPriority: 30},
		4: {ID: 4, ActionPriority: 100},
		3: {ID: 3, ActionPriority: 90},
		2: {ID: 2, ActionPriority: 100},
	}}
	if got, want := ag.RuleOrder(), []int64{2, 4, 3, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
