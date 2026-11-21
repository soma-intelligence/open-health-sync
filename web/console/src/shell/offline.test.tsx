import { onlineManager } from "@tanstack/react-query";
import { act, screen } from "@testing-library/react";
import { Route, Routes } from "react-router";

import type { ConsoleSession } from "../api/types";
import sessionJson from "../test/fixtures/session.json";
import { renderWithApp } from "../test/render";
import { AppShell } from "./AppShell";

const session = sessionJson as ConsoleSession;

describe("offline notice", () => {
  afterEach(() => {
    onlineManager.setOnline(true);
  });

  it("says the console is offline, and goes away when the connection is back", () => {
    renderWithApp(
      <Routes>
        <Route element={<AppShell />}>
          <Route path="overview" element={<p>page</p>} />
        </Route>
      </Routes>,
      { session, route: "/console/overview" },
    );
    expect(screen.queryByText(/You are offline/)).not.toBeInTheDocument();
    act(() => {
      onlineManager.setOnline(false);
    });
    expect(screen.getByText(/You are offline/)).toBeInTheDocument();
    act(() => {
      onlineManager.setOnline(true);
    });
    expect(screen.queryByText(/You are offline/)).not.toBeInTheDocument();
  });
});
