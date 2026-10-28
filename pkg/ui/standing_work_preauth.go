// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"context"
)

// The question the standing-work edits ask before their body is read:
// whether the object exists, so an edit of an unknown id is a 404 rather
// than a 400 about its body.

// authorizeScheduleEdit refuses an edit of a schedule that does not
// exist, asked on its id alone.
func (c *Client) authorizeScheduleEdit(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "schedule"); err != nil {
		return err
	}
	existing, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &scheduleNotFoundError{ID: id}
	}
	return nil
}

// authorizeTemplateEdit is authorizeScheduleEdit for a template.
func (c *Client) authorizeTemplateEdit(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return err
	}
	if err := refuseSystemTemplateEdit(id, "edit"); err != nil {
		return err
	}
	existing, err := c.Store.GetTemplate(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &templateNotFoundError{ID: id}
	}
	return nil
}

// authorizeSuiteEdit is authorizeScheduleEdit for a suite.
func (c *Client) authorizeSuiteEdit(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return err
	}
	existing, err := c.Store.GetSuite(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &suiteNotFoundError{ID: id}
	}
	return nil
}
