import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createApiClient, type FloatLabApi } from "@floatlab/client";
import CommandPalette from "./CommandConsole.vue";

const stack = { id: "stack-1", name: "plex", state: "idle", primary_node: "node-1", compose_file: "services: {}\n" };

function api(overrides: Partial<FloatLabApi> = {}): FloatLabApi {
  return {
    listStacks: vi.fn().mockResolvedValue([stack]),
    getStack: vi.fn().mockResolvedValue(stack),
    getStackConfig: vi.fn().mockResolvedValue(stack.compose_file),
    getStackStats: vi.fn(), execContainer: vi.fn(),
    listNodes: vi.fn().mockResolvedValue([]),
    listContainers: vi.fn().mockResolvedValue([]),
    validateCompose: vi.fn().mockResolvedValue(undefined),
    createStack: vi.fn(), updateCompose: vi.fn(), startStack: vi.fn(), stopStack: vi.fn(),
    startContainer: vi.fn(), stopContainer: vi.fn(), deleteStack: vi.fn(),
    getStackLogs: vi.fn(), getContainerLogs: vi.fn(), searchLogs: vi.fn(),
    upgradeStack: vi.fn(), createSnapshot: vi.fn(), listSnapshots: vi.fn(),
    ...overrides,
  };
}

async function settle() {
  await nextTick();
  await flushPromises();
  await nextTick();
}

const wrappers: VueWrapper[] = [];
function mountPalette(fake: FloatLabApi, inline = false) {
  const wrapper = mount(CommandPalette, { attachTo: document.body, props: { api: fake, inline } });
  wrappers.push(wrapper);
  return wrapper;
}

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount());
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("CommandPalette", () => {
  it('lets Tab leave the inline field when there is nothing to complete', async () => {
    const wrapper = mountPalette(api(), true);
    wrapper.vm.show();
    await settle();
    const input = wrapper.get('#palette-input');
    await input.setValue('no-matching-command');
    await settle();
    const event = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    input.element.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    const next = document.createElement('button'); document.body.append(next); next.focus();
    await settle();
    expect(wrapper.find('#palette-input').exists()).toBe(false);
    expect(document.activeElement).toBe(next);
  });

  it('keeps confirmation dialogs separate from the inline field', async () => {
    const fake = api();
    const wrapper = mountPalette(fake, true);
    wrapper.vm.launch(['start', 'stack-1']);
    await settle();
    expect(document.querySelector('[role="alertdialog"]')).not.toBeNull();
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false);
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    await settle();
    expect(wrapper.find('#palette-input').exists()).toBe(true);
    document.querySelector<HTMLButtonElement>('[role="alertdialog"] button')!.click();
    await settle();
    expect(fake.startStack).not.toHaveBeenCalled();
  });
  it('opens inside its header box and closes without an overlay', async () => {
    const wrapper = mountPalette(api(), true);
    await wrapper.get('[aria-label="Open command palette"]').trigger('click');
    await settle();
    expect(wrapper.get('.palette-inline').find('#palette-input').exists()).toBe(true);
    expect(document.querySelector('.palette-backdrop')).toBeNull();
    expect(wrapper.get('.palette').attributes('aria-modal')).toBeUndefined();
    expect(wrapper.find('.palette-trigger').exists()).toBe(false);
    const input = wrapper.get('#palette-input');
    await input.trigger('keydown', { key: 'Escape' });
    await settle();
    expect(wrapper.find('#palette-input').exists()).toBe(false);
    expect(document.activeElement).toBe(wrapper.get('[aria-label="Open command palette"]').element);
    await wrapper.get('[aria-label="Open command palette"]').trigger('click');
    await settle();
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    await settle();
    expect(wrapper.find('#palette-input').exists()).toBe(false);
  });
  it.each([
    { key: '/', code: 'Slash' },
    { key: 'k', code: 'KeyK', metaKey: true },
    { key: 'P', code: 'KeyP', ctrlKey: true, shiftKey: true },
  ])('opens with $key and prevents the browser shortcut', async shortcut => {
    mountPalette(api());
    const event = new KeyboardEvent('keydown', { ...shortcut, cancelable: true });
    window.dispatchEvent(event);
    await settle();
    expect(document.querySelector('#palette-input')).not.toBeNull();
    expect(event.defaultPrevented).toBe(true);
  });

  it.each(['input', 'textarea', 'select', 'div'])('leaves slash typing alone in %s editors', async tag => {
    mountPalette(api());
    const editor = document.createElement(tag);
    if (tag === 'div') editor.setAttribute('contenteditable', 'true');
    document.body.append(editor);
    const event = new KeyboardEvent('keydown', { key: '/', code: 'Slash', bubbles: true, cancelable: true });
    editor.dispatchEvent(event);
    await settle();
    expect(document.querySelector('#palette-input')).toBeNull();
    expect(event.defaultPrevented).toBe(false);
  });

  it('allows Cmd+K from an input but ignores composition and unrelated keys', async () => {
    mountPalette(api());
    for (const shortcut of [{ key: 'k' }, { key: '?', shiftKey: true }, { key: '/', isComposing: true }, { key: '/', repeat: true }]) {
      window.dispatchEvent(new KeyboardEvent('keydown', shortcut));
    }
    await settle();
    expect(document.querySelector('#palette-input')).toBeNull();
    const editor = document.createElement('input'); document.body.append(editor);
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }));
    await settle();
    expect(document.querySelector('#palette-input')).not.toBeNull();
  });
  it("completes API modules and sends direct commands through the shared HTTP client", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "ok" }), { headers: { "content-type": "application/json" } }));
    mountPalette(createApiClient({ baseUrl: "https://lab.invalid", token: "test", fetch }));
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "api hea";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    expect(document.querySelector("#palette-options code")?.textContent).toBe("health");
    input.value = "api health getHealth";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    expect(String(fetch.mock.calls[0]?.[0])).toBe("https://lab.invalid/api/v1/health");
    expect(document.querySelector(".palette-result")?.textContent).toContain('"status": "ok"');
  });

  it("offers binary responses as downloads and releases their URLs on unmount", async () => {
    const createObjectURL = vi.fn().mockReturnValue("blob:response");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = createObjectURL;
      static revokeObjectURL = revokeObjectURL;
    });
    const wrapper = mountPalette(createApiClient({ baseUrl: "https://lab.invalid", fetch: async () => new Response(new Uint8Array([0, 255]), { headers: { "content-type": "application/octet-stream" } }) }));
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "api health getHealth";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    const download = document.querySelector<HTMLAnchorElement>(".palette-result a")!;
    expect(download.href).toBe("blob:response");
    expect(download.textContent).toContain("2 bytes");
    expect(createObjectURL).toHaveBeenCalledOnce();
    wrapper.unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:response");
  });

  it("can stop an ongoing API request", async () => {
    let signal: AbortSignal | null | undefined;
    const fetch = vi.fn((_url, init?: RequestInit) => {
      signal = init?.signal;
      return new Promise<Response>((_resolve, reject) => signal?.addEventListener("abort", () => reject(new Error("Aborted"))));
    });
    mountPalette(createApiClient({ baseUrl: "https://lab.invalid", fetch }));
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "api health getHealth";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    document.querySelector<HTMLButtonElement>(".palette-running button")!.click();
    await settle();
    expect(signal?.aborted).toBe(true);
    expect(document.querySelector(".palette-running")).toBeNull();
    expect(document.querySelector(".palette-result")?.textContent).toContain("Aborted");
  });

  it("launches dashboard actions with safe arguments and reports completion", async () => {
    const fake = api({ getStackLogs: vi.fn().mockResolvedValue([]) });
    const wrapper = mountPalette(fake);
    wrapper.vm.launch(['logs', 'stack-1']);
    await settle();
    expect(fake.getStackLogs).toHaveBeenCalledWith('stack-1', { since: undefined, tail: undefined });
    expect(wrapper.emitted('completed')).toHaveLength(1);
    wrapper.vm.launch(['exec', 'stack-1', 'container-1', '--'], false);
    await settle();
    expect(document.querySelector<HTMLInputElement>('#palette-input')!.value).toBe('"exec" "stack-1" "container-1" "--" ');
    expect(fake.execContainer).not.toHaveBeenCalled();
  });
  it("completes quoted resource names and hides CLI-only file options", async () => {
    mountPalette(api({ listStacks: vi.fn().mockResolvedValue([{ ...stack, name: 'media stack' }]) }));
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = 'start "media s';
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    await settle();
    expect(input.value).toBe('start "media stack"');
    input.value = 'create demo --fi';
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    expect(document.querySelector("#palette-options")).toBeNull();
  });

  it("shows management actions and nodes as update tokens are completed", async () => {
    mountPalette(api({ listNodes: vi.fn().mockResolvedValue([{ id: "node-1", name: "fl-x220", status: "ready" }]) }));
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "upd";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    await settle();
    expect(input.value).toBe("update ");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    await settle();
    expect(input.value).toBe("update management ");
    expect(Array.from(document.querySelectorAll("#palette-options code"), item => item.textContent)).toEqual(["releases", "status", "start", "rollback", "cleanup"]);
    input.value += "sta";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    expect(Array.from(document.querySelectorAll("#palette-options code"), item => item.textContent)).toEqual(["status", "start"]);
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    await settle();
    expect(input.value).toBe("update management status ");
    expect(document.querySelector("#palette-options code")?.textContent).toBe("fl-x220");
  });

  it("opens with Alt+/, completes the final token, and restores focus on Escape", async () => {
    const origin = document.createElement("button");
    document.body.append(origin);
    origin.focus();
    mountPalette(api());

    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    expect(document.activeElement).toBe(input);

    input.value = "start pl";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await settle();
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    await settle();
    expect(input.value).toBe("start plex");

    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await settle();
    expect(document.querySelector("#palette-input")).toBeNull();
    expect(document.activeElement).toBe(origin);
  });

  it("does not mutate when lifecycle confirmation is cancelled", async () => {
    const fake = api();
    mountPalette(fake);
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "delete plex";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    expect(document.querySelector("[role='alertdialog']")).not.toBeNull();
    (document.querySelector("[role='alertdialog'] button") as HTMLButtonElement).click();
    await settle();
    expect(fake.deleteStack).not.toHaveBeenCalled();
  });

  it("asks before reopening invalid Compose with the rejected text", async () => {
    const fake = api({ validateCompose: vi.fn().mockRejectedValue(new Error("invalid Compose")) });
    mountPalette(fake);
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "edit plex";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    const editor = document.querySelector<HTMLTextAreaElement>("#palette-compose-input")!;
    editor.value = "not: valid";
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector<HTMLButtonElement>(".palette-compose .primary")!.click();
    await settle();
    expect(document.querySelector("[role='alertdialog']")?.textContent).toContain("Edit and try again?");
    expect(document.querySelector("[role='alertdialog']")?.textContent).toContain("invalid Compose");
    expect(document.querySelector("#palette-compose-input")).toBeNull();
    document.querySelector<HTMLButtonElement>("[role='alertdialog'] .danger")!.click();
    await settle();
    expect(document.querySelector<HTMLTextAreaElement>("#palette-compose-input")?.value).toBe("not: valid");
    expect(fake.validateCompose).toHaveBeenCalled();
    document.querySelector<HTMLButtonElement>(".palette-compose button")!.click();
    await settle();
  });

  it("gathers a missing create name and primary node", async () => {
    const fake = api({ listNodes: vi.fn().mockResolvedValue([{ id: "node-1", name: "node-a", status: "ready", zfs_pool: "tank" }]) });
    mountPalette(fake);
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "create";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    const prompt = document.querySelector<HTMLInputElement>("#palette-prompt-input")!;
    expect(document.activeElement).toBe(prompt);
    prompt.value = "new-stack";
    prompt.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector<HTMLFormElement>(".palette-prompt form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await settle();
    const select = document.querySelector<HTMLSelectElement>("#palette-prompt-input")!;
    expect(select.value).toBe("node-a");
    document.querySelector<HTMLFormElement>(".palette-prompt form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await settle();
    expect(document.querySelector("#palette-compose-input")).not.toBeNull();
    document.querySelector<HTMLButtonElement>(".palette-compose button")!.click();
    await settle();
  });

  it("builds an interactive upgrade without crypto.randomUUID on HTTP", async () => {
    vi.stubGlobal("crypto", { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) });
    const fake = api({
      listContainers: vi.fn().mockResolvedValue([{ id: "container-1", name: "web", image: "nginx:1", status: "running" }]),
      upgradeStack: vi.fn().mockResolvedValue({ state: "accepted" }),
    });
    mountPalette(fake);
    window.dispatchEvent(new KeyboardEvent("keydown", { altKey: true, code: "Slash" }));
    await settle();
    const input = document.querySelector<HTMLInputElement>("#palette-input")!;
    input.value = "upgrade plex";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
    expect(document.querySelector<HTMLSelectElement>("#palette-prompt-input")?.value).toBe("web");
    document.querySelector<HTMLFormElement>(".palette-prompt form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await settle();
    const image = document.querySelector<HTMLInputElement>("#palette-prompt-input")!;
    image.value = "nginx:2";
    image.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector<HTMLFormElement>(".palette-prompt form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await settle();
    const done = document.querySelector<HTMLSelectElement>("#palette-prompt-input")!;
    done.value = "";
    done.dispatchEvent(new Event("change", { bubbles: true }));
    document.querySelector<HTMLFormElement>(".palette-prompt form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await settle();
    expect(fake.upgradeStack).toHaveBeenCalledWith("stack-1", { web: "nginx:2" }, expect.stringMatching(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/));
  });
});
