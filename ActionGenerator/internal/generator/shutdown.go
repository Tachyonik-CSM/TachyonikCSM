// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Stopping without leaving a half-applied set of actions (AG-OPS-09).
//
// The unit that must not be cut is one rule evaluated for one user (or for
// the workspace's owner): the actions it creates and the stale ones it
// withdraws. Stop closes the gate (TachyonikLib drain) — a pass or request
// asked for after it returns at once — and a running pass stops after the
// unit it is in, rather than after every user and rule, which can take longer
// than a container is given to stop. What it leaves is evaluated by the pass
// at the next start.

package generator

import (
	"errors"
	"strings"
	"time"
)

// ErrStopping is returned for an evaluation asked for after Stop.
var ErrStopping = errors.New("ActionGenerator is stopping; nothing is evaluated any more")

// stopping reports whether Stop was called; a pass checks it between units.
func (g *Generator) stopping() bool { return g.gate.Stopping() }

// Stop closes the gate: no evaluation starts any more, and a running pass
// stops after the rule and user it is evaluating.
func (g *Generator) Stop() { g.gate.Stop() }

// Wait waits up to timeout for the running evaluations to finish. It returns
// "" when they did, or the units still running when the time ran out.
func (g *Generator) Wait(timeout time.Duration) string {
	return strings.Join(g.gate.Wait(timeout), ", ")
}
