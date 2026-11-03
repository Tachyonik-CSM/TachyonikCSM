// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Whether the account behind a session token may still use it.
//
// A token is valid until it expires, a day by default, but the account it
// names can be deactivated or deleted in the meantime. SystemManager refuses
// such a token at once; this service asks SystemManager about the account and
// refuses it too. The answer is remembered briefly, so a page that fires a
// dozen requests causes one lookup rather than twelve, and a deactivation
// takes effect here within accountCheckTTL.
//
// When SystemManager cannot be asked, the request is refused: an account that
// cannot be verified is not let through.

package handlers

import (
	"errors"
	"sync"
	"time"

	"tachyonik/assetmanager/internal/systemmanager"
)

// accountCheckTTL is how long an account's status is remembered.
const accountCheckTTL = 30 * time.Second

var (
	// errAccountInactive: the account is deactivated or no longer exists.
	errAccountInactive = errors.New("account deactivated or deleted")
	// errAccountUnverified: SystemManager could not be asked.
	errAccountUnverified = errors.New("account could not be verified")
)

// userLookup is the one SystemManager call the check needs.
type userLookup interface {
	GetUser(id int64) (*systemmanager.User, error)
}

type accountStatus struct {
	active bool
	at     time.Time
}

// accountChecker answers whether an account may use its session.
type accountChecker struct {
	users userLookup
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	known map[int64]accountStatus
}

func newAccountChecker(users userLookup) *accountChecker {
	return &accountChecker{users: users, ttl: accountCheckTTL, now: time.Now, known: map[int64]accountStatus{}}
}

// check returns nil when the account may proceed, errAccountInactive when it
// is deactivated or gone, and errAccountUnverified when SystemManager could
// not be asked. Only answers are remembered, never a failed lookup.
func (c *accountChecker) check(userID int64) error {
	c.mu.Lock()
	s, ok := c.known[userID]
	c.mu.Unlock()
	if !ok || c.now().Sub(s.at) >= c.ttl {
		user, err := c.users.GetUser(userID)
		if err != nil {
			return errAccountUnverified
		}
		// An unknown user is a deleted one. A record without the field comes
		// from a SystemManager that predates deactivation, where every
		// account is active.
		s = accountStatus{active: user != nil && (user.Active == nil || *user.Active), at: c.now()}
		c.mu.Lock()
		c.known[userID] = s
		c.mu.Unlock()
	}
	if !s.active {
		return errAccountInactive
	}
	return nil
}
