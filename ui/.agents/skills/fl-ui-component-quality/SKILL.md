---
name: fl-ui-component-quality
description: Enforce FloatLab Vue component quality and relational design-system structure with Primitive-first composition, numbered Primitive, Pattern, or Layout Histoire filing, colocated stories, Vitest coverage, type safety, and clean linting. Use whenever creating, changing, refactoring, reviewing, classifying, or diagnosing UI components, component stories, or component tests in this repository.
---

# FloatLab UI Component Quality

Follow the repository's existing Vue, Histoire, and Vitest patterns. Keep changes scoped to the requested behavior.

## Classify every Histoire story

Use exactly one numbered top-level title so Histoire keeps the hierarchy ordered: `1. Primitive/Name`, `2. Pattern/Name`, or `3. Layout/Name`. Classify by responsibility, not size or DOM complexity:

1. **Primitive:** File here as a context-agnostic visual or interactive foundation. Examples: buttons, inputs, icons, badges, avatars, text, and basic displays. It may manage only its own state, such as hover, focus, active, or disabled.
2. **Pattern:** File here when the component solves a user task, represents domain data, or combines controls into a functional workflow. Examples: data tables, search headers, form groups, date pickers, and media cards.
3. **Layout:** File here when the component's sole purpose is spacing, alignment, responsive structure, or positioning arbitrary children. Examples: containers, grids, page shells, split panes, and stacks. It must not contain domain content, business logic, or specific actions.

Choose Pattern for a task or domain responsibility; otherwise choose Layout for structural-only responsibility; otherwise choose Primitive. Patterns may compose Primitives. Layouts may arrange either without knowing their content.

## Compose from Primitives

1. Search existing Primitives before adding a reusable visual control or display to a Pattern or Layout.
2. Reuse the Primitive for buttons, inputs, metrics, status treatments, and other shared UI foundations. Keep workflow and domain structure in the Pattern; keep positioning in the Layout.
3. If the required Primitive does not exist, build it first with its own story and tests, then consume it. Do not create a wrapper that merely renames one-off markup.
4. Keep foundational appearance and state behavior in the Primitive. Patterns and Layouts may control placement and composition, but must not duplicate or override the Primitive's look and feel without a documented component-specific reason.
5. When changing a Primitive, inspect every Pattern and Layout consumer and verify that the change propagates safely.

## Build the complete component change

1. Inspect the component, its callers, its `*.story.vue`, its `*.spec.ts`, and the nearest comparable component before editing.
2. Add a colocated Histoire story and Vitest spec for every new component.
3. Update the existing story and tests whenever a component change affects behavior, states, props, events, or interactions.
4. Give every story its classified `1. Primitive/Name`, `2. Pattern/Name`, or `3. Layout/Name` title and demonstrate the meaningful supported states and interactions. Do not add duplicate stories for cosmetic implementation details.
5. Test every function and branch introduced or changed, including prop-driven states, emitted events, and user interactions. Assert observable behavior instead of private implementation details.
6. For a bug fix, add a test that fails for the reported behavior before fixing the root cause.

## Verify before completion

Run all checks from the repository root:

```sh
pnpm test --run
pnpm typecheck
pnpm lint
pnpm story:build
```

Do not claim completion while a check fails. Fix failures caused by the change; report unrelated pre-existing failures without expanding scope.

## Completion criteria

- Every new or changed component has a current story and tests.
- Every story is filed under `1. Primitive`, `2. Pattern`, or `3. Layout` using the responsibility rules above.
- Tests cover every new or changed function and behavior.
- Tests, typecheck, lint, and the Histoire build pass.
