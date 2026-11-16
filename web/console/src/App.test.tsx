import { render, screen } from "@testing-library/react";

describe("App", () => {
  it("lands on sign in when there is no session", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response('{"title":"Unauthorized","detail":"no session"}', { status: 401 }))),
    );
    window.history.replaceState(null, "", "/console/users");
    const { App } = await import("./App");
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});
