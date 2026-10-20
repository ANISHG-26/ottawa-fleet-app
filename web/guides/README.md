# Record the short learning videos

The root [quick guide](../../QUICK_GUIDE.md) explains the app. This folder owns
the repeatable Playwright recorder, separate from fixture-based UI tests.

Use Node 20.19+ and the existing locked dependencies:

```sh
cd web
npm ci
npx playwright install chromium
npm run guide:record -- --record-live
```

Start the [synthetic simulation stack](../../docs/local-development.md#opt-in-route-simulation-profile)
first. The default target is `http://127.0.0.1:18088`. For an already-running
lab exposed through a private loopback port forward:

```sh
npm run guide:record -- --record-live --base-url http://127.0.0.1:19000
```

The explicit flag acknowledges two new bounded synthetic runs/rides. The
recorder requires the 20-vehicle profile, at least two available Lansdowne
vehicles and no active run. It does not start infrastructure, reset data or
extend a lab lease. Failed owned runs are stopped/drained; existing history stays.
Use a new dedicated simulation project if the existing fleet cannot run both
trips; do not reset another operator's dataset.

Three scenarios assert the visible outcome and save captioned WebM videos,
posters and public-safe capture metadata under root `videos/`. Only a complete
successful recording publishes those outputs. Scratch recordings stay in
ignored `web/artifacts/guide-recordings/`; they are not PR artifacts.

Review every clip before committing: readable captions, actual expected result,
no private URLs/credentials, and capture source matching the release shown.
Keep clips short. Commit only these intentional synthetic demonstrations; do not
commit raw browser traces, profiles, cookies, or unrelated recordings.

Validation:

```sh
node --check guides/record.mjs
python ../scripts/check_repository.py
```

Open root `WATCH.html` to play the three clips locally. The captions are added by
the recorder for the video; they are not changes to the shipped console.

## Refresh the GitHub README players

GitHub renders uploaded media attachment URLs as players. A relative repository
WebM link opens a file/download view instead. After recording changed behavior,
upload the reviewed clips using the Markdown editor or a GitHub CLI version
with `--attach`, then replace the standalone attachment URLs in root README.
Keep the repository download links and `WATCH.html` for local/offline use.

For example, when preparing an authorized documentation PR with a current CLI:

```sh
gh pr edit <PR-number> --attach ./videos/02-trip-and-refresh.webm
```

Run that from the repository root, copy the generated attachment URL from the
PR body into the corresponding README paragraph, and check the rendered
README's actual player before committing the final update. Older CLI versions
without `--attach` need the web editor's upload control instead.
See [GitHub's attachment documentation](https://docs.github.com/en/github-cli/github-cli/attaching-files-with-github-cli).
