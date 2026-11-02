# FloatLab command contract

These modules mirror `api/openapi/*.yaml`. `manifest.json` lists them in assembly order; `cli.json` describes local utilities. A module is `{ "commands": [...] }` containing OpenCLI command objects. Modules are repository fragments, not independently published OpenCLI documents, and do not use `$ref`.

Run from the repository root:

```sh
pnpm commands:generate
pnpm commands:check
pnpm commands:coverage
```

Generation writes the assembled document to `packages/floatlab-client/src/commands.generated.json` and the five shell scripts to `cli/completions/`. Commit both sources and generated files. CI checks freshness. Both CLI and browser import the assembled document; installed users need only the compiled FloatLab binary.

Domain commands live with their primary API operation. Cross-domain workflows reference other modules' operation IDs. To split a group across modules, repeat the same group properties and contribute distinct children. Assembly rejects conflicting properties, duplicate executable commands, and colliding names or aliases.

## FloatLab metadata

All extensions use OpenCLI's standard `metadata: [{ "name": "floatlab", "value": { ... } }]` shape:

| Property | Purpose |
|---|---|
| `provider` | Suggestion provider: `stacks`, `nodes`, `containers`, `images`, `alerts`, `active-alerts`, `rules`, or CLI-only `files` |
| `dependsOn` | Argument supplying the resource scope, such as `stack` for containers |
| `suggestions` | Suggested examples without restricting accepted input |
| `appendSpace` | Continue with the next word when a candidate is accepted |
| `prompt` | Allow a required argument to be omitted in interactive mode; the existing handler gathers it |
| `defaultCommand` | Child selected when a group is invoked without arguments |
| `passthrough` | Preserve words after `--`; `exec` requires this delimiter |
| `capability` | Restrict suggestions to `cli`, `files`, or `interactive` contexts |
| `utility` | Local command handled by the CLI rather than an API workflow |
| `api` | Operation and request-input coverage declarations |

The parser validates command-specific flags, required inputs, choices, and arity. Existing handlers retain JSON/content validation, conditional option rules, confirmations, and API orchestration. Only use `acceptedValues` for actual constraints; put examples such as mute durations in `suggestions`.

## API mappings and report

For example:

```json
{
  "name": "floatlab",
  "value": {
    "api": [{
      "operationId": "deleteStack",
      "inputs": {
        "path/id": "argument:stack",
        "query/purge": "option:--purge",
        "header/Idempotency-Key": "runtime:idempotency"
      }
    }]
  }
}
```

Input keys are `path/name`, `query/name`, `header/name`, or `body/field/nested-field`. Body arrays and maps use `*` segments. Parameter names retain OpenAPI casing. Sources use `argument:`, `option:`, `json:`, `editor:`, `derived:`, or `runtime:`. Argument/option/JSON sources must reference a declared input; derived/runtime/editor sources describe existing handler behavior. Map only fields that handlers actually accept. A parent body mapping includes its descendants, appropriate for unrestricted JSON or Compose inputs.

An API mapping may carry an `exclusion` reason instead of presenting the operation as a CLI action. Reports retain these reasons. Unmapped operations require no baseline allowlist: they appear automatically as gaps. New fields beneath a broad JSON mapping count as exposed only when the handler truly forwards them; restrict mappings to individual fields when handlers use an allowlist.

`commands:coverage` resolves the complete OpenAPI document and writes `dist/command-coverage/coverage.{md,json}`, grouped by API module. JSON includes per-input sources and whether they are user-exposed or derived/runtime-managed. Unsupported schema constructs are reported. Coverage gaps do not fail CI; malformed contracts, invalid mapping references, and unreadable OpenAPI do. The report describes declarations, not proof that handlers transmit them: keep transport tests for that behavior.

## Upstream schema

`vendor/schema.json` and `vendor/LICENSE.md` are unmodified files from [spectreconsole/open-cli](https://github.com/spectreconsole/open-cli) at revision `e42891559d59e84b1da8a8901450fa37784597ed`, recorded in the manifest. The validator disables Unicode regular expressions because that revision's email pattern escapes `@`; local validation additionally rejects unknown contract/metadata properties. Update the vendor files and manifest together when deliberately adopting a newer revision.
