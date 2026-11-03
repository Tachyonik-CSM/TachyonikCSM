// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Coalescing the audit events that quota rejections produce.
//
// An import does not stop when it hits a limit — it keeps posting every parsed
// item — so the rejections arrive as a tight burst. Reported one by one they
// would bury the log; reported per user, source and kind they become one honest
// line ("dropped 890 host assets").

package handlers

import (
	"fmt"
	"sync"
	"time"
)

// Limit-exceeded audit events are coalesced per (user, source, kind) so a
// bulk import that blows past a quota produces ONE summary audit entry
// ("… dropped 890 host assets …") instead of one entry per dropped item.
//
// An import keeps POSTing every parsed item even after the limit is hit, so
// the over-limit creates arrive as a tight burst. A short debounce window
// collapses that burst: each rejected create resets the timer, and the
// summary is emitted once the burst goes quiet. A max-wait cap bounds how
// long the summary can be delayed under a sustained stream so it never
// starves.
const (
	limitAuditQuietWindow = 2 * time.Second
	limitAuditMaxWait     = 15 * time.Second
)

// limitKind describes one quota's audit wording. The count==1 message
// matches the previous per-item wording verbatim.
type limitKind struct {
	prefix   string // e.g. "Host asset limit reached"
	singular string // e.g. "host asset"
	plural   string // e.g. "host assets"
	withID   bool   // append " (id N)" — host assets carry the numeric source id
}

var (
	assetLimit         = limitKind{"Host asset limit reached", "host asset", "host assets", true}
	instanceAssetLimit = limitKind{"Appliance host asset limit reached", "host asset", "host assets", true}
	detectionLimit     = limitKind{"Detection limit reached", "detection", "detections", false}
	vulnerabilityLimit = limitKind{"Vulnerability limit reached", "vulnerability", "vulnerabilities", false}
)

type limitDrop struct {
	kind     limitKind
	userID   int64
	current  int
	max      int
	source   string
	sourceID int64
	count    int
	firstAt  time.Time
	timer    *time.Timer
}

func (d *limitDrop) message() string {
	noun := d.kind.singular
	if d.count != 1 {
		noun = d.kind.plural
	}
	msg := fmt.Sprintf("%s (%d/%d): dropped %d %s from source %q",
		d.kind.prefix, d.current, d.max, d.count, noun, d.source)
	if d.kind.withID {
		msg += fmt.Sprintf(" (id %d)", d.sourceID)
	}
	return msg
}

// limitAuditAggregator coalesces per-item limit rejections into one summary
// audit per (user, source, kind). Safe for concurrent use.
type limitAuditAggregator struct {
	emit    func(userID int64, level, message string)
	mu      sync.Mutex
	pending map[string]*limitDrop
}

func newLimitAuditAggregator(emit func(userID int64, level, message string)) *limitAuditAggregator {
	return &limitAuditAggregator{
		emit:    emit,
		pending: make(map[string]*limitDrop),
	}
}

func limitKey(userID int64, kind limitKind, source string, sourceID int64) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%d", userID, kind.prefix, source, sourceID)
}

// record registers one dropped item. The summary audit fires once the burst
// for this (user, source, kind) goes quiet (or hits the max-wait cap).
func (a *limitAuditAggregator) record(kind limitKind, userID int64, current, max int, source string, sourceID int64) {
	if a == nil || a.emit == nil || userID == 0 {
		return
	}
	key := limitKey(userID, kind, source, sourceID)

	a.mu.Lock()
	defer a.mu.Unlock()

	if d, ok := a.pending[key]; ok {
		d.count++
		// Keep the latest current/max in case the limit changed mid-burst.
		d.current = current
		d.max = max
		// Debounce, but never delay past maxWait from the first drop.
		remaining := limitAuditQuietWindow
		if elapsed := time.Since(d.firstAt); elapsed+remaining > limitAuditMaxWait {
			remaining = limitAuditMaxWait - elapsed
			if remaining < 0 {
				remaining = 0
			}
		}
		d.timer.Reset(remaining)
		return
	}

	d := &limitDrop{
		kind:     kind,
		userID:   userID,
		current:  current,
		max:      max,
		source:   source,
		sourceID: sourceID,
		count:    1,
		firstAt:  time.Now(),
	}
	d.timer = time.AfterFunc(limitAuditQuietWindow, func() { a.flush(key) })
	a.pending[key] = d
}

// flush emits the accumulated summary for key and removes it. A no-op if the
// entry was already flushed (e.g. by FlushAll or a racing timer).
func (a *limitAuditAggregator) flush(key string) {
	a.mu.Lock()
	d, ok := a.pending[key]
	if !ok {
		a.mu.Unlock()
		return
	}
	delete(a.pending, key)
	a.mu.Unlock()
	a.emit(d.userID, "Limit Exceeded", d.message())
}

// FlushAll emits every pending summary immediately. Call on shutdown so a
// burst still in flight isn't silently lost.
func (a *limitAuditAggregator) FlushAll() {
	if a == nil {
		return
	}
	a.mu.Lock()
	pending := a.pending
	a.pending = make(map[string]*limitDrop)
	a.mu.Unlock()
	for _, d := range pending {
		if d.timer != nil {
			d.timer.Stop()
		}
		a.emit(d.userID, "Limit Exceeded", d.message())
	}
}
