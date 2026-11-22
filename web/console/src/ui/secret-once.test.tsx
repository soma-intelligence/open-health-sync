import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { SecretOnce } from "./SecretOnce";

const SECRET = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";

function Parent() {
  const [value, setValue] = useState<string | null>(SECRET);
  const [renders, setRenders] = useState(0);
  return (
    <div>
      <button
        type="button"
        onClick={() => {
          setRenders(renders + 1);
        }}
      >
        Rerender
      </button>
      {value === null ? (
        <p>closed</p>
      ) : (
        <SecretOnce
          title="Signing secret"
          intro="Store it now."
          value={value}
          onDone={() => {
            setValue(null);
          }}
        />
      )}
    </div>
  );
}

describe("SecretOnce", () => {
  it("shows the secret once with Copy and drops it on Done", async () => {
    render(<Parent />);
    const dialog = screen.getByRole("dialog", { name: "Signing secret" });
    expect(within(dialog).getByDisplayValue(SECRET)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Copy/ })).toBeInTheDocument();
    expect(within(dialog).getByText("Store it now.")).toBeInTheDocument();

    const done = within(dialog).getByRole("button", { name: "Done" });
    expect(done, "Done waits for the operator to say the secret is stored").toBeDisabled();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I have stored it somewhere safe" }));
    await userEvent.click(done);
    expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Rerender" }));
    expect(screen.getByText("closed")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);
    expect(screen.queryAllByDisplayValue(SECRET)).toHaveLength(0);
  });

  it("does not close on Escape", async () => {
    render(<Parent />);
    await userEvent.keyboard("{Escape}");
    expect(screen.getByDisplayValue(SECRET)).toBeInTheDocument();
  });
});
