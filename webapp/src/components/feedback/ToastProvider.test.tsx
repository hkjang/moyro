// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { ToastProvider, useToast } from "./ToastProvider";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Trigger() {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast.success("저장했습니다")}>ok</button>
      <button type="button" onClick={() => toast.error("실패했습니다")}>fail</button>
    </>
  );
}

describe("ToastProvider", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  function button(label: string): HTMLButtonElement {
    const match = [...container.querySelectorAll("button")].find((el) => el.textContent === label);
    if (!match) throw new Error(`button not found: ${label}`);
    return match;
  }

  it("shows a success toast as a status and queues a following toast", async () => {
    await act(async () => root.render(<ToastProvider><Trigger /></ToastProvider>));

    await act(async () => button("ok").click());
    expect(document.querySelector('[role="status"]')?.textContent).toContain("저장했습니다");

    // The second toast waits its turn rather than overwriting the first.
    await act(async () => button("fail").click());
    expect(document.querySelector('[role="status"]')?.textContent).toContain("저장했습니다");
    expect(document.querySelector('[role="alert"]')).toBeNull();
  });

  it("is a no-op without a provider so isolated components stay renderable", async () => {
    await act(async () => root.render(<Trigger />));
    await act(async () => button("ok").click());
    expect(document.querySelector('[role="status"]')).toBeNull();
  });
});
