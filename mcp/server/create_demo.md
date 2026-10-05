Create a narrated demo video of the developer's app with screencaster.

Description from the developer: {{description}}

Follow these steps in order:

1. Call `get_options`. It lists the installed languages and voices (with the default voice per language), the audiences, and the names of existing demos.
2. Ask the developer once, in a single message, skipping everything the description already answers:
   - languages (default: English only);
   - a voice for each selected language, chosen from `get_options` with the default marked; when a language has no default voice (`defaultVoice` is null) the choice is required;
   - the audience (release-notes, sales or marketing);
   - a title, proposed from the description;
   - the app's base URL (absolute, e.g. http://host.docker.internal:3000) and, if the app needs a login, its Playwright storage state (cookies and localStorage; JSON pasted from an export is fine).
   The developer may reply "defaults" to accept every default.
3. Explore the running app with `explore_page` to find real selectors and URLs. Pass absolute URLs (the base URL joined with the path) and the storageState object, if there is one. Use only selectors that `explore_page` returned. To inspect a page deeper in a flow, pass the `actions` that reach it.
4. Write `demos/<name>.yaml` following the script format in the `render_video` tool description. Include `baseUrl`, `storageState` (inline object, only if the app needs a login), `languages`, `voices` (only for voices that differ from the default) and `meta` (title, description, audience). Choose a `name` that is not in `existingDemos`. Give every narrated step narration text for every selected language.
5. Call `render_video` with the script path, then poll `get_render_status` until the job is `succeeded` or `failed`. Report the output paths. If it failed, fix only the failing step in the YAML and submit again.
6. Do not ask the developer to approve the YAML before rendering; they review the video, not the script.
7. If the description above is empty, first ask the developer what the demo should show, then continue with step 1.
