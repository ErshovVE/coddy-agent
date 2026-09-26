import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { EnvironmentChip } from "./EnvironmentChip";

// On a relay there is no composer, so the chip sits in the swarm header at the
// top of the screen. A menu that always grew upward left it off-screen there.
describe("EnvironmentChip menu direction", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          ({ ok: true, status: 200, json: async () => ({}) }) as Response,
      ),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  const rect = (top: number): DOMRect =>
    ({
      left: 40,
      top,
      bottom: top + 28,
      right: 160,
      width: 120,
      height: 28,
      x: 40,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;

  it("opens downward when the chip is near the top of the window", async () => {
    render(<EnvironmentChip />);
    const btn = screen.getByTestId("composer-env-btn");
    btn.getBoundingClientRect = () => rect(48);
    fireEvent.click(btn);
    const menu = await screen.findByTestId("composer-env-menu");
    expect(menu).toHaveClass("opens-down");
    expect(menu.style.top).toBe("84px");
    expect(menu.style.bottom).toBe("");
  });

  // The menu opens from a click, so the focus stays on the chip: Escape has
  // to reach the menu from there too.
  it("closes on Escape with the focus still on the chip", async () => {
    render(<EnvironmentChip />);
    const btn = screen.getByTestId("composer-env-btn");
    btn.getBoundingClientRect = () => rect(48);
    fireEvent.click(btn);
    await screen.findByTestId("composer-env-menu");
    expect(fireEvent.keyDown(btn, { key: "Escape" })).toBe(false);
    expect(screen.queryByTestId("composer-env-menu")).toBeNull();
  });

  it("keeps opening upward from the composer at the foot", async () => {
    render(<EnvironmentChip />);
    const btn = screen.getByTestId("composer-env-btn");
    btn.getBoundingClientRect = () => rect(window.innerHeight - 90);
    fireEvent.click(btn);
    const menu = await screen.findByTestId("composer-env-menu");
    expect(menu).toHaveClass("opens-up");
    expect(menu.style.bottom).not.toBe("");
    expect(menu.style.top).toBe("");
  });
});
