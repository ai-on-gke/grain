import {
  Checkbox,
  FormControlLabel,
  TextField,
  Typography,
} from "@mui/material";
import SettingsCard from "./SettingsCard.jsx";

// CachesSection is Settings → Caches (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function CachesSection({ s }) {
  const { settings, saveChoice, onIntBlur, onEnterKey } = s;
  return (
    <>
      <SettingsCard title="Proxy cache" anchor="proxy-cache">
        <FormControlLabel
          control={
            <Checkbox
              name="packageCacheEnabled"
              data-setting-id="packageCacheEnabled"
              checked={!!settings.packageCacheEnabled}
              onChange={(evt) =>
                saveChoice({ packageCacheEnabled: evt.target.checked })
              }
            />
          }
          label={
            <>
              Cache apt, pip, Docker Hub, npm and Go module downloads for every
              sandbox
              <span className="hint">
                on (default): each new sandbox has its apt, pip, docker, npm and
                Go toolchain pointed at this deployment&apos;s own cache, so a
                package or image layer one run downloads is not downloaded again
                by the next -- and a sandbox with no network of its own can
                install packages at all. Packages, not package lists: apt still
                fetches and verifies the archive&apos;s signed indexes itself,
                pip and npm still check every artifact against the hash their
                index gave, docker still resolves every tag upstream and
                verifies every layer against its digest, and the go command
                still checks every module against go.sum. An https:// fetch that
                reaches the cache as a proxy (a build exporting http_proxy, an
                image whose sources.list names an archive over TLS) is tunnelled
                straight to that archive, uncached, rather than failing -- and
                refused if it names a host outside the cache&apos;s allowlist,
                so a blanket http_proxy is still not the way to use it. Turning
                it off applies to the next run dispatched, not to sandboxes
                already running, and leaves what the cache has collected where
                it is.
              </span>
            </>
          }
        />
        {!!settings.packageCacheEnabled && (
          <>
            <TextField
              key={`cacheSize-${settings.packageCacheSizeGb || ""}`}
              name="packageCacheSizeGb"
              data-setting-id="packageCacheSizeGb"
              label="Proxy cache max size (GiB)"
              helperText="maximum disk space the proxy cache may use before evicting oldest packages and image layers. Blank uses grain's default."
              type="number"
              inputProps={{ min: 0, step: 1 }}
              defaultValue={
                settings.packageCacheSizeGb
                  ? String(settings.packageCacheSizeGb)
                  : ""
              }
              placeholder={
                settings.packageCacheSizeGbDefault
                  ? String(settings.packageCacheSizeGbDefault)
                  : undefined
              }
              onBlur={onIntBlur("packageCacheSizeGb", { emptyVal: 0 })}
              onKeyDown={onEnterKey}
              fullWidth
              margin="normal"
            />
            <Typography variant="caption" color="text.secondary">
              What the cache is holding, and the buttons that empty it, are on
              System → Caches.
            </Typography>
          </>
        )}

        {/* The other cache on the controller's disk
            (pkg/actionscache, grain/task-357). The switch reads
            differently from the one above it, and the hint says so
            at length on purpose: turning the proxy cache on
            changes where a download comes from, and turning this
            one on decides that every branch of a repository may
            read what every other branch's CI wrote. That is the
            fact an operator is deciding about, so it is beside the
            switch rather than in a package doc. */}
      </SettingsCard>
      <SettingsCard title="Actions cache" anchor="actions-cache">
        <FormControlLabel
          control={
            <Checkbox
              name="actionsCacheEnabled"
              data-setting-id="actionsCacheEnabled"
              checked={!!settings.actionsCacheEnabled}
              onChange={(evt) =>
                saveChoice({ actionsCacheEnabled: evt.target.checked })
              }
            />
          }
          label={
            <>
              Keep what CI jobs cache on the controller, so builds are warm
              across runs
              <span className="hint">
                on (default): each CI job is handed a GitHub Actions cache that
                lives on this machine rather than inside the sandbox that is
                destroyed when the job ends, so a{" "}
                <code>cache-from: type=gha</code> build, and anything a workflow
                writes with <code>actions/cache</code>, is read back by the next
                run instead of rebuilt. A miss costs a cold build and nothing
                else: an entry is only ever handed to a lookup naming the same
                key and the same version. What it does not separate is branches
                -- every branch of a repository shares that repository&apos;s
                entries, so anything one branch&apos;s CI writes another
                branch&apos;s CI can read back, and a branch&apos;s own workflow
                file is a file that branch can rewrite. A deployment that would
                rather not grant that should turn this off rather than reason
                about cache keys. Turning it off applies to the next job
                dispatched, not to jobs already running, and leaves what the
                cache has collected where it is.
              </span>
            </>
          }
        />
        {!!settings.actionsCacheEnabled && (
          <>
            <TextField
              key={`actionsCacheSize-${settings.actionsCacheSizeGb || ""}`}
              name="actionsCacheSizeGb"
              data-setting-id="actionsCacheSizeGb"
              label="Actions cache max size (GiB)"
              helperText="maximum disk space the Actions cache may use before evicting the entries nothing has read for longest. One bound across every repository, so a busy repository can evict a quiet one's entries. Blank uses grain's default."
              type="number"
              inputProps={{ min: 0, step: 1 }}
              defaultValue={
                settings.actionsCacheSizeGb
                  ? String(settings.actionsCacheSizeGb)
                  : ""
              }
              placeholder={
                settings.actionsCacheSizeGbDefault
                  ? String(settings.actionsCacheSizeGbDefault)
                  : undefined
              }
              onBlur={onIntBlur("actionsCacheSizeGb", { emptyVal: 0 })}
              onKeyDown={onEnterKey}
              fullWidth
              margin="normal"
            />
            <Typography variant="caption" color="text.secondary">
              What the cache is holding, which repository is filling it, and the
              buttons that empty it, are on System &rarr; Caches.
            </Typography>
          </>
        )}
      </SettingsCard>
    </>
  );
}
