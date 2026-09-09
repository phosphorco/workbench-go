# Context examples

These files are small, deliberately bounded examples for the accepted Workbench
context lane. They are not a replacement for the host's native hook
configuration and they do not prove model admission.

1. Install the binary once and reconcile host settings:

   ```sh
   workbench context setup --harness both
   ```

2. Copy `ai-context.md` into a project root. Its frontmatter contributes
   guidance only when the project is opted in and an observed resource matches.

3. Copy `project-context.pkl` to the project root as `workbench-context.pkl`
   and make the provider executable:

   ```sh
   cp project-context.pkl ./workbench-context.pkl
   chmod +x ./provider.sh
   workbench context status --path "$PWD" --json
   ```

The contributor declaration is executable Pkl, amends `workbench:context`, and
uses only the `contribute` capability. Its relative executable path is resolved
from the consuming declaration root; Workbench does not search `PATH`.

To exercise the profile capability, replace `workbench-context.pkl` with
`profile-context.pkl` and use `profile-provider.py`. It advertises the `profile`
capability and returns bounded `role` and
`preference:reviewStyle` facts with an end-of-day `until` expiry. The fixture
parses each request with Python's standard-library `json` module and rejects
request lines larger than 1 MiB. Make that fixture executable too:

```sh
chmod +x /absolute/path/to/profile-provider.py
```

A provider may advertise both capabilities and implement both request methods.
The Pkl snippets are evaluated by the installed Workbench binary through its
pinned private Pkl runtime and bundled schemas; JSON in this example is only the
provider RPC payload.

An executable provider receives one JSON-RPC 2.0 request per line and must send
one matching response per line. It must accept `initialize`, implement every
advertised capability, and answer `shutdown`. The runtime owns scope,
configuration, audience, profile, content, and source-revision identity; the
provider authors only its stable slot/source/body/reasons within the typed
response.
