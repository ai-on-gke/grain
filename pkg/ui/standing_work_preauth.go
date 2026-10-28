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
