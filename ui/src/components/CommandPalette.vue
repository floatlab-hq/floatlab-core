<script setup lang="ts">
import { CommandEngine, type CommandResult, type CommandRuntime, type Completion, type FloatLabApi } from "@floatlab/client";
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";

const props = defineProps<{ api: FloatLabApi }>();

const open = ref(false);
const query = ref("");
const completions = ref<Completion[]>([]);
const active = ref(0);
const result = ref<CommandResult>();
const running = ref(false);
const input = ref<HTMLInputElement>();
const previousFocus = ref<HTMLElement>();
const confirmMessage = ref<string>();
const confirmResolve = ref<(answer: boolean) => void>();
const composeOpen = ref(false);
const composeDraft = ref("");
const composeError = ref("");
const composeResolve = ref<(value: string | undefined) => void>();
const composeWaiting = ref(false);
const promptMessage = ref<string>();
const promptChoices = ref<Completion[]>();
const promptValue = ref("");
const promptResolve = ref<(value: string | undefined) => void>();

const runtime: CommandRuntime = {
  interactive: true,
  confirm: (message) => new Promise((resolve) => {
    confirmMessage.value = message;
    confirmResolve.value = resolve;
  }),
  ask: (message) => prompt(message),
  choose: (message, choices) => prompt(message, choices),
  editCompose: (initial) => new Promise((resolve) => {
    if (!composeOpen.value) {
      composeDraft.value = initial;
      composeError.value = "";
      composeOpen.value = true;
    }
    composeResolve.value = resolve;
    void nextTick(() => document.querySelector<HTMLTextAreaElement>(".palette-compose textarea")?.focus());
  }),
  cleanupCompose: async () => {
    composeOpen.value = false;
    composeWaiting.value = false;
    composeResolve.value = undefined;
  },
  present: async (next) => {
    result.value = next;
    if (composeWaiting.value) {
      composeWaiting.value = false;
      if (next.kind === "error") {
        composeError.value = next.message;

      } else {
        void nextTick(() => input.value?.focus());
      }
    }
  },
};

const engine = new CommandEngine(props.api, runtime);

let completionRequest = 0;
async function updateCompletions() {
  const request = ++completionRequest;
  const next = await engine.complete(query.value).catch(() => []);
  if (request !== completionRequest) return;
  completions.value = next;
  active.value = Math.min(active.value, Math.max(next.length - 1, 0));
}

function show() {
  if (open.value || confirmMessage.value || composeOpen.value || promptMessage.value) return;
  previousFocus.value = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
  open.value = true;
  void nextTick(() => {
    input.value?.focus();
    void updateCompletions();
  });
}

function close() {
  if (confirmMessage.value || composeOpen.value || promptMessage.value) return;
  open.value = false;
  completions.value = [];
  previousFocus.value?.focus();
}

function acceptCompletion() {
  const completion = completions.value[active.value];
  if (!completion) return;
  query.value = query.value.replace(/\S*$/, completion.value);
  completions.value = [];
  void nextTick(() => input.value?.focus());
}

async function execute() {
  if (!query.value.trim() || running.value || confirmMessage.value || composeOpen.value || promptMessage.value) return;
  running.value = true;
  result.value = undefined;
  try {
    await engine.dispatch(query.value);
    await updateCompletions();
  } catch (error) {
    result.value = { kind: "error", message: error instanceof Error ? error.message : "Command failed" };
  } finally {
    running.value = false;
  }
}

function onInput() {
  active.value = 0;
  void updateCompletions();
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    close();
  } else if (event.key === "ArrowDown" && completions.value.length) {
    event.preventDefault();
    active.value = (active.value + 1) % completions.value.length;
  } else if (event.key === "ArrowUp" && completions.value.length) {
    event.preventDefault();
    active.value = (active.value - 1 + completions.value.length) % completions.value.length;
  } else if (event.key === "Tab" && completions.value.length) {
    event.preventDefault();
    acceptCompletion();
  } else if (event.key === "Tab") {
    event.preventDefault();
    input.value?.focus();
  } else if (event.key === "Enter") {
    event.preventDefault();
    void execute();
  }
}

function answerConfirmation(answer: boolean) {
  const resolve = confirmResolve.value;
  confirmMessage.value = undefined;
  confirmResolve.value = undefined;
  resolve?.(answer);
  void nextTick(() => input.value?.focus());
}

function saveCompose() {
  const resolve = composeResolve.value;
  if (!resolve) return;
  composeWaiting.value = true;
  composeOpen.value = false;
  composeResolve.value = undefined;
  resolve(composeDraft.value);
}

function cancelCompose() {
  composeOpen.value = false;
  composeError.value = "";
  composeWaiting.value = false;
  const resolve = composeResolve.value;
  composeResolve.value = undefined;
  resolve?.(undefined);
  void nextTick(() => input.value?.focus());
}

function prompt(message: string, choices?: Completion[]) {
  promptMessage.value = message;
  promptChoices.value = choices;
  promptValue.value = choices?.[0]?.value ?? "";
  return new Promise<string | undefined>((resolve) => {
    promptResolve.value = resolve;
    void nextTick(() => document.querySelector<HTMLElement>(".palette-prompt select, .palette-prompt input")?.focus());
  });
}

function answerPrompt(value?: string) {
  const resolve = promptResolve.value;
  promptMessage.value = undefined;
  promptChoices.value = undefined;
  promptResolve.value = undefined;
  resolve?.(value);
  void nextTick(() => {
    if (!promptMessage.value) input.value?.focus();
  });
}

function trapModalFocus(event: KeyboardEvent) {
  if (event.key !== "Tab") return;
  const root = event.currentTarget as HTMLElement;
  const elements = [...root.querySelectorAll<HTMLElement>("button, textarea, input, [tabindex]:not([tabindex='-1'])")];
  const current = elements.indexOf(document.activeElement as HTMLElement);
  event.preventDefault();
  elements[(current + (event.shiftKey ? elements.length - 1 : 1)) % elements.length]?.focus();
}

function onConfirmKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    answerConfirmation(false);
    return;
  }
  trapModalFocus(event);
}

function onComposeKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    cancelCompose();
    return;
  }
  trapModalFocus(event);
}

function onPromptKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    answerPrompt();
    return;
  }
  trapModalFocus(event);
}

function onGlobalKeydown(event: KeyboardEvent) {
  if (event.altKey && event.code === "Slash" && !open.value && !confirmMessage.value && !composeOpen.value && !promptMessage.value) {
    event.preventDefault();
    show();
  }
}

onMounted(() => window.addEventListener("keydown", onGlobalKeydown));
onBeforeUnmount(() => window.removeEventListener("keydown", onGlobalKeydown));
</script>

<template>
  <button
    class="palette-trigger"
    type="button"
    aria-label="Open command palette"
    @click="show"
  >
    ⌘ <span>Alt</span>/
  </button>

  <Teleport to="body">
    <div
      v-if="open"
      class="palette-backdrop"
      @mousedown.self="close"
    >
      <section
        class="palette"
        role="dialog"
        aria-modal="true"
        aria-labelledby="palette-title"
        @keydown="onKeydown"
      >
        <h2
          id="palette-title"
          class="sr-only"
        >
          FloatLab command palette
        </h2>
        <label
          class="sr-only"
          for="palette-input"
        >Command</label>
        <input
          id="palette-input"
          ref="input"
          v-model="query"
          class="palette-input"
          role="combobox"
          autocomplete="off"
          spellcheck="false"
          placeholder="Type a FloatLab command…"
          aria-autocomplete="list"
          aria-controls="palette-options"
          :aria-expanded="completions.length > 0"
          :aria-activedescendant="completions.length ? `palette-option-${active}` : undefined"
          @input="onInput"
        >
        <ul
          v-if="completions.length"
          id="palette-options"
          class="palette-options"
          role="listbox"
          aria-label="Command suggestions"
        >
          <li
            v-for="(completion, index) in completions"
            :id="`palette-option-${index}`"
            :key="completion.value"
            role="option"
            :aria-selected="index === active"
            :class="{ active: index === active }"
            @mousedown.prevent="active = index; acceptCompletion()"
          >
            <code>{{ completion.value }}</code><small v-if="completion.description">{{ completion.description }}</small>
          </li>
        </ul>
        <p
          v-else
          class="palette-hint"
        >
          Tab completes · Enter runs · Esc closes
        </p>

        <output
          v-if="result"
          class="palette-result"
          :class="`palette-result-${result.kind}`"
          aria-live="polite"
        >
          <p v-if="result.kind === 'text'">{{ result.text }}</p>
          <table v-else-if="result.kind === 'table'">
            <thead><tr><th
              v-for="column in result.columns"
              :key="column"
            >{{ column }}</th></tr></thead>
            <tbody><tr
              v-for="(row, rowIndex) in result.rows"
              :key="rowIndex"
            ><td
              v-for="(cell, cellIndex) in row"
              :key="cellIndex"
            >{{ cell }}</td></tr></tbody>
          </table>
          <pre v-else-if="result.kind === 'logs'">{{ result.lines.map((line) => `${line.ts} ${line.stream}: ${line.msg}`).join('\n') }}</pre>
          <pre v-else-if="result.kind === 'exec'">{{ result.stdout }}{{ result.stderr }}</pre>
          <p v-else-if="result.kind === 'operation'">{{ result.text ?? `${result.action}${result.state ? `: ${result.state}` : ""}` }}</p>
          <p
            v-else
            role="alert"
          >{{ result.message }}</p>
        </output>
        <p
          v-if="running"
          class="palette-running"
          role="status"
        >
          Running…
        </p>
      </section>
    </div>

    <div
      v-if="confirmMessage"
      class="palette-backdrop"
      role="presentation"
    >
      <section
        class="palette-modal"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="palette-confirm-title"
        @keydown="onConfirmKeydown"
      >
        <h2 id="palette-confirm-title">
          Confirm command
        </h2>
        <p
          v-if="result?.kind === 'error'"
          role="alert"
        >
          {{ result.message }}
        </p>
        <p>{{ confirmMessage }}</p>
        <div class="palette-actions">
          <button
            type="button"
            @click="answerConfirmation(false)"
          >
            Cancel
          </button><button
            type="button"
            class="danger"
            autofocus
            @click="answerConfirmation(true)"
          >
            Confirm
          </button>
        </div>
      </section>
    </div>

    <div
      v-if="composeOpen"
      class="palette-backdrop palette-compose"
      role="presentation"
    >
      <section
        class="palette-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="palette-compose-title"
        @keydown="onComposeKeydown"
      >
        <h2 id="palette-compose-title">
          Edit Compose
        </h2>
        <label
          class="sr-only"
          for="palette-compose-input"
        >Compose configuration</label>
        <textarea
          id="palette-compose-input"
          v-model="composeDraft"
          spellcheck="false"
        />
        <p
          v-if="composeError"
          class="palette-error"
          role="alert"
        >
          {{ composeError }}
        </p>
        <div class="palette-actions">
          <button
            type="button"
            @click="cancelCompose"
          >
            Cancel
          </button><button
            type="button"
            class="primary"
            @click="saveCompose"
          >
            Save
          </button>
        </div>
      </section>
    </div>

    <div
      v-if="promptMessage"
      class="palette-backdrop palette-prompt"
      role="presentation"
    >
      <form
        class="palette-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="palette-prompt-title"
        @keydown="onPromptKeydown"
        @submit.prevent="answerPrompt(promptValue)"
      >
        <h2 id="palette-prompt-title">
          {{ promptMessage }}
        </h2>
        <label
          class="sr-only"
          for="palette-prompt-input"
        >{{ promptMessage }}</label>
        <select
          v-if="promptChoices"
          id="palette-prompt-input"
          v-model="promptValue"
        >
          <option
            v-for="choice in promptChoices"
            :key="choice.value"
            :value="choice.value"
          >
            {{ choice.value }}{{ choice.description ? ` — ${choice.description}` : "" }}
          </option>
        </select>
        <input
          v-else
          id="palette-prompt-input"
          v-model="promptValue"
          autocomplete="off"
        >
        <div class="palette-actions">
          <button
            type="button"
            @click="answerPrompt()"
          >
            Cancel
          </button>
          <button
            type="submit"
            class="primary"
          >
            Continue
          </button>
        </div>
      </form>
    </div>
  </Teleport>
</template>
