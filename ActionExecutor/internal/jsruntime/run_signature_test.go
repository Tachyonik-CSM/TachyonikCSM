// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how run is called: run(api), with api its one and only argument. The
// dry run, in AIManager's dryrun package, is tested to call it the same way.

package jsruntime

import (
	"testing"
	"time"

	"github.com/dop251/goja"
)

const reportsItsArguments = `var executors = [{ name: "e", ruleId: 4, run: function (api) {
	return { status: "success", message: arguments.length + ":" + typeof api.user };
} }];`

func TestARealRunPassesApiAlone(t *testing.T) {
	rt := New()
	if err := rt.LoadFromString(reportsItsArguments); err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := rt.Run(func(vm *goja.Runtime) goja.Value {
		api := vm.NewObject()
		_ = api.Set("user", vm.NewObject())
		return api
	}, time.Second)
	if err != nil || res.Message != "1:object" {
		t.Errorf("got %+v, %v; want api as the one argument", res, err)
	}
}
