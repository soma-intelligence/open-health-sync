import { useState } from "react";

import { Button } from "../../ui/Button";
import { Icon } from "../../ui/Icon";

/** One field: Enter looks the external id up, an empty field lists everyone again. */
export function UserSearch({ value, onSearch }: { value: string; onSearch: (externalRef: string) => void }) {
  const [text, setText] = useState(value);
  return (
    <form
      className="list-search"
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        onSearch(text.trim());
      }}
    >
      <input
        type="search"
        className="input"
        aria-label="External id"
        placeholder="Find by external id"
        autoComplete="off"
        spellCheck={false}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
        }}
      />
      <Button type="submit">
        <Icon name="search" size="sm" />
        Search
      </Button>
    </form>
  );
}
