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
import { DOCUMENT, LocationStrategy } from "@angular/common";
import {
  type ApplicationConfig,
  inject,
  provideZoneChangeDetection,
} from "@angular/core";
import {
  provideRouter,
  withComponentInputBinding,
  withNavigationErrorHandler,
  withPreloading,
} from "@angular/router";
import { routes } from "./app.routes";
import { connectionInterceptor } from "./services/connection.service";
import {
  isChunkLoadError,
  reloadForStaleChunk,
} from "./services/upgrade-watch.service";
import { IdleRoutePreload } from "./shell/route-preload";

export const appConfig: ApplicationConfig = {
  providers: [
    provideZoneChangeDetection({ eventCoalescing: true }),
    provideRouter(
      routes,
      withComponentInputBinding(),
      withPreloading(IdleRoutePreload),
      // A page whose lazy chunk will not load is one this bundle no
      // longer matches -- the daemon was upgraded underneath it -- so
      // the page is loaded afresh from the new build instead.
      withNavigationErrorHandler((e) => {
        if (!isChunkLoadError(e.error)) return;
        reloadForStaleChunk(
          inject(DOCUMENT).defaultView,
          inject(LocationStrategy).prepareExternalUrl(e.url),
        );
      }),
    ),
    provideHttpClient(withFetch(), withInterceptors([connectionInterceptor])),
  ],
};
