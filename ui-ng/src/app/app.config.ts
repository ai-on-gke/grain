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
  provideHttpClient,
  withFetch,
  withInterceptors,
} from "@angular/common/http";
import {
  type ApplicationConfig,
  provideZoneChangeDetection,
} from "@angular/core";
import {
  provideRouter,
  withComponentInputBinding,
  withNavigationErrorHandler,
  withPreloading,
  withRouterConfig,
} from "@angular/router";
import { routes } from "./app.routes";
import { connectionInterceptor } from "./services/connection.service";
import {
  IdleRoutePreload,
  staleChunkErrorHandler,
} from "./shell/route-preload";

export const appConfig: ApplicationConfig = {
  providers: [
    provideZoneChangeDetection({ eventCoalescing: true }),
    provideRouter(
      routes,
      withComponentInputBinding(),
      withPreloading(IdleRoutePreload),
      withNavigationErrorHandler(staleChunkErrorHandler),
      // A leave guard (Processes' or a repository page's unsaved edit)
      // that cancels a Back has to put the browser's history back where
      // it was, not just the address bar: under the default "replace"
      // the history index stays on the entry Back moved to, so the next
      // Back skips that page -- and on the first page of a visit leaves
      // the app altogether (docs/test-runs/2026-11-20-recurring-composed-ui.md,
      // bug 2).
      withRouterConfig({ canceledNavigationResolution: "computed" }),
    ),
    provideHttpClient(withFetch(), withInterceptors([connectionInterceptor])),
  ],
};
