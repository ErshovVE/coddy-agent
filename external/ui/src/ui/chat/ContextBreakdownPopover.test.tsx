import React from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ContextBreakdownPopover } from "./ContextBreakdownPopover";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("context action shows the threshold, posts compaction and holds progress", async () => {
  let finish: ((response: Response) => void) | undefined;
  const pending = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  const fetchMock = vi.fn(() => pending);
  vi.stubGlobal("fetch", fetchMock);
  const onCompacted = vi.fn();
  render(
    <ContextBreakdownPopover
      open
      onClose={() => {}}
      maxContextTokens={1000}
      sessionId="sess_123"
      compactEnabled
      compactThreshold={95}
      onCompacted={onCompacted}
    />,
  );

  const action = screen.getByTestId("context-breakdown-compact");
  expect(action).toHaveTextContent("Compact at 95%");
  fireEvent.click(action);
  expect(action).toBeDisabled();
  expect(action).toHaveTextContent("Compacting");
  expect(fetchMock).toHaveBeenCalledWith("/coddy/sessions/sess_123/compact", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Coddy-Session-ID": "sess_123",
    },
    body: "{}",
  });
  finish!(new Response("{}", { status: 200 }));
  await waitFor(() => expect(onCompacted).toHaveBeenCalledOnce());
  expect(action).not.toBeDisabled();
});

test("context action says compact now when automation is disabled", () => {
  render(
    <ContextBreakdownPopover
      open
      onClose={() => {}}
      maxContextTokens={1000}
      sessionId="sess_123"
      compactEnabled={false}
    />,
  );
  expect(screen.getByTestId("context-breakdown-compact")).toHaveTextContent(
    "Compact now",
  );
});
