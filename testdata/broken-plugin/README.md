# broken-plugin

A manifest that is wrong in every way `dev test --static-only` is supposed
to catch, so the harness is proven to FAIL and not merely to run.

A checker that only ever sees valid input is untested. Every first-party
plugin passes, so without this fixture the suite's green is equally
consistent with "the checks work" and "the checks do nothing" — which is
the same false-green this repo keeps finding elsewhere.

Each defect below is silent at runtime if it ships:

| Field | Defect | What happens without the check |
|---|---|---|
| `id` | not lowercase-alphanumeric-hyphen | refused at load |
| `version` | missing | refused at load |
| `min_api_version` | `9.0.0`, unsatisfiable | refused at load |
| `privileges` | `clipbord` — typo for `clipboard` | grants nothing; the calls it needed are refused |
| `optional_privileges` | `audiox` | same, in the field a hand-written checker forgets |
| `implements.on_actoin` | typo for `on_action` | INERT — never dispatched, never logged |
| `implements.parse_key_event` | not a platform method | INERT. This is a real shipped bug: see `PluginImplements` in actuator/src/plugins/manifest.rs |

Kept in sync by `just check-harness-fixture`, which asserts the run fails,
exits non-zero, and names every defect.
