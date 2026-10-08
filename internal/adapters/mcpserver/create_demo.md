Create a narrated demo video of the developer's app with screencaster.

Description from the developer: {{description}}

Follow these steps in order:

1. Call `get_options`. It lists the installed languages and voices (with the default voice per language), the audiences, and the names of existing demos.
2. Ask the developer once, in a single message, skipping everything the description already answers:
   - languages (default: English only);
   - a voice for each selected language, chosen from `get_options` with the default marked; when a language has no default voice (`defaultVoice` is null) the choice is required;
   - the audience (release-notes, sales or marketing);
   - a title, proposed from the description;
   - the app's address as the Docker container reaches it (e.g. http://172.17.0.1:3000 for a host app on Linux, http://host.docker.internal:3000 on macOS or Windows) and, if the app needs a login, its Playwright storage state (cookies and localStorage; JSON pasted from an export is fine).
   The developer may reply "defaults" to accept every default.
3. Explore the running app with `explore_page` to find real selectors and URLs. Pass absolute URLs (the app's address joined with the path) and the storageState object, if there is one. Use only selectors that `explore_page` returned. To inspect a page deeper in a flow, pass the `actions` that reach it.
4. Write `demos/<name>.yaml` following the script format in the `render_video` tool description. Write every `goto` as an absolute URL (the app's address joined with the path); there is no `baseUrl`. Include `storageState` (inline object, only if the app needs a login), `languages`, `voices` (only for voices that differ from the default) and `meta` (title, description, audience). Every video gets a 3 s start card (meta title and description) and a 3 s end card; add `intro` or `outro` only to change one: an `image` (PNG or JPEG, a path relative to the demo file, e.g. `assets/logo.png` for a file in `demos/assets/`, which the developer provides), other text, or `false` for none. Paths in the demo, such as `outputDir` and `image`, are relative to the demo file's folder, and videos land in `demos/output/` by default. Choose a `name` that is not in `existingDemos`. Give every narrated step narration text for every selected language.
5. Call `render_video` with the script path, then poll `get_render_status` until the job is `succeeded` or `failed`. Report the output paths. If it failed, fix only the failing step in the YAML and submit again.
6. Do not ask the developer to approve the YAML before rendering; they review the video, not the script.
7. If the description above is empty, first ask the developer what the demo should show, then continue with step 1.
8. If the developer wants screenshots of the app instead of a video, write a script with `type: screenshots` (no narration, languages, voices or cards; `screenshot` steps mark each capture) and queue it with `take_screenshots`; the PNGs land in `demos/output/<name>/screenshots/`.
