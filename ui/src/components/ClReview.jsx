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

import { Button, Chip, Stack, Tooltip } from "@mui/material";
import api from "../api.js";

// A CL's review (the task's `clReview`, google3-cls.md §5): its state
// beside the cl/N chip, who has said LGTM and which approvals it still
// lacks, and the two things a person does with it -- Mail it to its
// reviewers, and Publish the replies a run left as drafts.
const STATES = {
  draft: { label: "draft", color: "default", variant: "outlined" },
  in_review: { label: "in review", color: "info", variant: "outlined" },
  changes_requested: {
    label: "changes requested",
    color: "warning",
    variant: "filled",
  },
  needs_approval: {
    label: "needs approval",
    color: "warning",
    variant: "outlined",
  },
  approved: { label: "approved", color: "success", variant: "filled" },
};

export function reviewLabel(review) {
  const state = STATES[review?.state] ? review.state : "in_review";
  let label = STATES[state].label;
  if (review?.needsMailing) return "needs mailing";
  const missing = review?.needsApproval || [];
  if (state === "needs_approval" && missing.length > 0) {
    label += `: ${missing.join(", ")}`;
  } else if (state === "approved" && review?.lgtm?.length > 0) {
    label = `LGTM ${review.lgtm.join(", ")}`;
  }
  return label;
}

function reviewTitle(review) {
  const parts = [];
  if (review.lgtm?.length) parts.push(`LGTM: ${review.lgtm.join(", ")}`);
  if (review.needsApproval?.length)
    parts.push(`Missing approval: ${review.needsApproval.join(", ")}`);
  if (review.needsMailing) parts.push("Not mailed to a reviewer yet");
  return parts.join(". ") || reviewLabel(review);
}

export default function ReviewBadge({ review }) {
  if (!review) return null;
  const state = STATES[review.state] ? review.state : "in_review";
  return (
    <Tooltip title={reviewTitle(review)}>
      <Chip
        className="cl-review-badge"
        data-testid="cl-review-badge"
        data-state={state}
        size="small"
        color={STATES[state].color}
        variant={STATES[state].variant}
        label={reviewLabel(review)}
        aria-label={`Review: ${reviewTitle(review)}`}
        sx={{ maxWidth: 260, verticalAlign: "middle" }}
      />
    </Tooltip>
  );
}

// ReviewActions is the Mail and Publish replies buttons, each disabled
// with the reason when it does not apply. A disabled button takes no
// pointer events, so the reason's tooltip sits on a wrapper.
export function ReviewActions({ t, act }) {
  const review = t?.clReview;
  if (!review) return null;
  const publishLabel =
    review.drafts > 0
      ? `Publish replies (${review.drafts})`
      : "Publish replies";
  return (
    <Stack direction="row" spacing={1} data-testid="cl-review-actions">
      <Tooltip
        title={
          review.canMail
            ? "Mail this CL to its reviewers"
            : review.mailReason || ""
        }
      >
        <span>
          <Button
            size="small"
            variant="outlined"
            disabled={!review.canMail}
            data-testid="cl-mail"
            onClick={() =>
              act(
                () =>
                  api(`/api/tasks/${t.id}/change-mail`, {
                    method: "POST",
                    body: JSON.stringify({}),
                  }),
                t.id,
              )
            }
          >
            Mail
          </Button>
        </span>
      </Tooltip>
      <Tooltip
        title={
          review.canPublish
            ? review.publishReason
              ? `Left as drafts: ${review.publishReason}`
              : "Publish the replies this task's runs drafted"
            : review.publishReason || ""
        }
      >
        <span>
          <Button
            size="small"
            variant="outlined"
            disabled={!review.canPublish}
            data-testid="cl-publish"
            onClick={() =>
              act(
                () =>
                  api(`/api/tasks/${t.id}/change-publish`, { method: "POST" }),
                t.id,
              )
            }
          >
            {publishLabel}
          </Button>
        </span>
      </Tooltip>
    </Stack>
  );
}
