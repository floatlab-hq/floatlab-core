import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import type { FloatLabApi } from "@floatlab/client";
import CommandPalette from "./CommandPalette.vue";

const stack = { id: "stack-1", name: "plex", state: "idle", primary_node: "node-1", compose_file: "services: {}\n" };

function api(overrides: Partial<FloatLabApi> = {}): FloatLabApi {
  return {
    listStacks: vi.fn().mockResolvedValue([stack]),
    getStack: vi.fn().mockResolvedValue(stack),
    getStackConfig: vi.fn().mockResolvedValue(stack.compose_file),
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
function mountPalette(fake: FloatLabApi) {
  const wrapper = mount(CommandPalette, { attachTo: document.body, props: { api: fake } });
  wrappers.push(wrapper);
  return wrapper;
}

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount());
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("CommandPalette", () => {
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

  it("keeps invalid Compose text open beside its validation error", async () => {
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
    expect(document.querySelector<HTMLTextAreaElement>("#palette-compose-input")?.value).toBe("not: valid");
    expect(document.querySelector(".palette-error")?.textContent).toContain("invalid Compose");
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

  it("uses choose and ask to build an interactive upgrade", async () => {
    vi.stubGlobal("crypto", { randomUUID: () => "test-key" });
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
    expect(fake.upgradeStack).toHaveBeenCalledWith("stack-1", { web: "nginx:2" }, "test-key");
  });
});
