package daemon

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

type stateResetNotice struct{}

func (stateResetNotice) announce(_ context.Context, _ string, resets []staterepo.Reset) error {
	if len(resets) == 0 {
		return nil
	}
	return localResetNoticeError()
}

func (stateResetNotice) announceSystemScheduleEdits(
	_ context.Context,
	_ string,
	edits []staterepo.SystemScheduleEdit,
) error {
	if len(edits) == 0 {
		return nil
	}
	return localResetNoticeError()
}

func localResetNoticeError() error {
	return fmt.Errorf(
		"the config repository is controller-local, so %w -- there is no external pull request to post this on",
		staterepo.ErrNowhereToAnnounce,
	)
}
