package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TimeZoneHeader carries the IANA zone of whoever is driving the
// browser this request came from: the zone they picked in Settings →
// Preferences, or the browser's own when they left it on "Browser
// default" (ui/src/api.js sends it on every write). It is what a task
// filed or messaged from that browser records as model.Task.TimeZone,
// which is the zone that task's runs are told the time in.
//
// It is a preference, not an identity or a permission, so there is
// nothing to believe or distrust about it: a zone this host cannot load
// is ignored, exactly as one that was never sent.
const TimeZoneHeader = "X-Grain-Time-Zone"

// requestTimeZone is the requester's zone from TimeZoneHeader, or "" for
// a request that sent none or one that is not a loadable zone.
func requestTimeZone(r *http.Request) string {
	return validRequesterTimeZone(r.Header.Get(TimeZoneHeader))
}

func validRequesterTimeZone(zone string) string {
	zone = strings.TrimSpace(zone)
	if zone == "" || !model.ValidTimeZone(zone) {
		return ""
	}
	return zone
}

// noteRequesterTimeZone records zone -- the zone of the person who just
// wrote to task id -- as the task's own. A task that already has a zone
// keeps it, since it belongs to whoever filed it; the exception is a
// conversation (an interactive chat or a standing console session),
// whose next turn answers whoever spoke last, so it follows them (a
// console session is always interactive). An empty zone, or one the
// task already has, writes nothing.
func (c *Client) noteRequesterTimeZone(ctx context.Context, id, zone string) error {
	zone = validRequesterTimeZone(zone)
	if zone == "" {
		return nil
	}
	adopts := func(t model.Task) bool {
		return t.TimeZone != zone && (t.TimeZone == "" || t.Interactive)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil || task == nil || !adopts(*task) {
		return err
	}
	return c.Store.UpdateTask(ctx, id, func(t *model.Task) error {
		if adopts(*t) {
			t.TimeZone = zone
		}
		return nil
	})
}
