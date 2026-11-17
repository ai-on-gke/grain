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
  ViewEncapsulation,
  computed,
  input,
} from "@angular/core";
import type { InstallNotice } from "../api/models";
import { StatusIconComponent } from "./status-icon.component";

// InstallNoticeComponent is GET /api/config's installNotice on screen,
// the counterpart of the classic UI's InstallNotice.jsx: something about
// how this deployment is installed that only its operator can fix --
// today a host-mode install no installer has migrated (pkg/daemon's
// installnotice.go). It names what fixes it, the CLIs that cannot run
// until then, and where the migration is written up.
//
// variant "banner" is the standing strip the shell shows below the top
// bar on every page; it has no close button, since nothing changes until
// the install is migrated and the daemon restarted. variant "card" is the
// same notice in full, for Fleet's health area and Settings → Upgrade.
//
// Every word of it is the server's: the command is the notice's own
// (editions register theirs through daemon.RegisterHostModeMigrateCommand),
// never one written here.
@Component({
  selector: "g-install-notice",
  imports: [StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .install-notice {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        background: var(--md-sys-color-error-container);
        color: var(--md-sys-color-on-error-container);
        overflow-wrap: anywhere;
        min-width: 0;
      }
      .install-notice.is-banner {
        flex: none;
        padding: 8px 20px;
        border-bottom: 1px solid var(--md-sys-color-error);
        font: 400 13px/18px var(--grain-font-body);
      }
      .install-notice.is-card {
        padding: 12px 16px;
        border-radius: var(--md-sys-shape-corner-medium);
        font: 400 14px/20px var(--grain-font-body);
      }
      .install-notice > g-status-icon {
        flex: none;
        display: inline-flex;
        margin-top: 3px;
      }
      .install-notice.is-card > g-status-icon {
        margin-top: 4px;
      }
      .install-notice-body {
        min-width: 0;
        flex: 1 1 auto;
      }
      .install-notice-title {
        margin: 0;
        font: 600 14px/20px var(--grain-font-body);
      }
      .install-notice-body p {
        margin: 4px 0 0;
      }
      .install-notice-command {
        font-family: var(--grain-font-mono);
        font-size: 12px;
        padding: 1px 6px;
        border-radius: var(--md-sys-shape-corner-xs);
        border: 1px solid var(--md-sys-color-outline-variant);
        background: var(--md-sys-color-surface-container-lowest);
        color: var(--md-sys-color-on-surface);
        white-space: pre-wrap;
      }
      .install-notice-clis {
        margin: 2px 0 0;
        padding-left: 20px;
      }
      .install-notice a {
        color: inherit;
        font-weight: 600;
        text-decoration: underline;
      }
    `,
  ],
  template: `
    @let n = notice();
    @if (variant() === "banner") {
      <div
        class="install-notice is-banner"
        role="status"
        data-testid="install-notice-banner"
      >
        <g-status-icon tone="error" kind="error" />
        <span class="install-notice-body">
          <strong>Unmigrated host-mode install:</strong>
          {{ sentence() }}
          @if (n.command) {
            Run
            <code class="install-notice-command">{{ n.command }}</code
            >.
          }
          @if (cliNames(); as names) {
            Cannot run until then:
            <span data-testid="install-notice-banner-clis">{{ names }}</span
            >.
          }
          @if (n.docsUrl) {
            <a
              [href]="n.docsUrl"
              target="_blank"
              rel="noopener noreferrer"
              data-testid="install-notice-docs"
              >Migration docs</a
            >
          }
        </span>
      </div>
    } @else {
      <section
        class="install-notice is-card"
        role="status"
        aria-labelledby="install-notice-title"
        [attr.data-testid]="testId()"
      >
        <g-status-icon tone="error" kind="error" />
        <div class="install-notice-body">
          <h3 class="install-notice-title" id="install-notice-title">
            Unmigrated host-mode install
          </h3>
          <p>{{ sentence() }}</p>
          @if (n.command) {
            <p>
              Run
              <code
                class="install-notice-command"
                data-testid="install-notice-command"
                >{{ n.command }}</code
              >.
            </p>
          }
          @if (clis().length > 0) {
            <p>CLIs that lost their run location:</p>
            <ul class="install-notice-clis" data-testid="install-notice-clis">
              @for (c of clis(); track c) {
                <li>{{ c }}</li>
              }
            </ul>
          }
          @if (n.docsUrl) {
            <p>
              <a
                [href]="n.docsUrl"
                target="_blank"
                rel="noopener noreferrer"
                data-testid="install-notice-docs"
                >Migration docs</a
              >
            </p>
          }
        </div>
      </section>
    }
  `,
})
export class InstallNoticeComponent {
  readonly notice = input.required<InstallNotice>();
  readonly variant = input<"banner" | "card">("card");
  readonly testId = input("install-notice");

  // sentence is the server's message as a sentence: capitalised, with
  // its full stop.
  protected readonly sentence = computed(() => {
    const m = (this.notice().message ?? "").trim();
    if (!m) return "";
    const s = m[0].toUpperCase() + m.slice(1);
    return /[.!?]$/.test(s) ? s : s + ".";
  });

  protected readonly clis = computed(() => this.notice().affectedClis ?? []);

  // cliNames are the affected CLIs by name alone ("claude: no host
  // binary, ..." is "claude"), for the banner's one line.
  protected readonly cliNames = computed(() =>
    this.clis()
      .map((c) => c.split(":")[0].trim())
      .filter(Boolean)
      .join(", "),
  );
}
