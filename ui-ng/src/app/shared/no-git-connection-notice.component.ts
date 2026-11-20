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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  input,
  output,
} from "@angular/core";
import { RouterLink } from "@angular/router";

// hasAnyGitConnection is whether deployment (config.deployment, the
// deployment's own settings) has a git connection on any host, as the
// classic UI's gitConnections.js reads it: "anonymous" pushes nothing,
// so it does not count.
export function hasAnyGitConnection(
  deployment: Record<string, unknown> | null | undefined,
): boolean {
  if (!deployment) return false;
  if (deployment["hasGitCredential"] === true) return true;
  const legacy = String(deployment["gitCredential"] ?? "").trim();
  if (legacy && legacy !== "anonymous") return true;
  const conns = deployment["gitConnections"];
  if (!Array.isArray(conns)) return false;
  return conns.some((c: unknown) => {
    const cred = String(
      (c as { credential?: unknown } | null)?.credential ?? "",
    ).trim();
    return cred !== "" && cred !== "anonymous";
  });
}

// NoGitConnectionNoticeComponent is the "add a git connection first"
// state of a place that picks a repository -- the New task sheet's
// Repository field and Settings → Repos' Add repository. A fresh install
// starts with no git connection, and a picker with nothing to discover
// and a task that only waits read as broken unless somebody says why. It
// renders nothing until the deployment has loaded, and nothing once it
// has a connection on any host.
@Component({
  selector: "g-no-git-connection-notice",
  imports: [RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (show()) {
      <div class="s-banner readonly g-no-git" data-testid="no-git-connection">
        <span>
          This deployment has no git connection yet, so its tasks cannot clone
          or push a repository and wait until it has one. Add one first, in
          <a routerLink="/settings/git" (click)="navigated.emit()"
            >Settings → Git</a
          >: a GitHub token or GitHub App for github.com, or a token, SSH key or
          ambient login for another git host.
        </span>
      </div>
    }
  `,
  styles: `
    .g-no-git {
      display: flex;
      padding: 12px 16px;
      border-radius: var(--md-sys-shape-corner-medium);
      background: var(--md-sys-color-secondary-container);
      color: var(--md-sys-color-on-secondary-container);
      font-size: 14px;
      line-height: 20px;
    }
    .g-no-git a {
      color: inherit;
      font-weight: 600;
      text-decoration: underline;
    }
  `,
})
export class NoGitConnectionNoticeComponent {
  // deployment is config.deployment; undefined while it loads.
  readonly deployment = input<Record<string, unknown> | null | undefined>();
  // navigated fires when the link is followed, so a sheet can close.
  readonly navigated = output<void>();

  protected readonly show = computed(() => {
    const d = this.deployment();
    return d != null && !hasAnyGitConnection(d);
  });
}
