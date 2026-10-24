import { KeyboardEvent, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { useDebounced } from "../hooks";

interface FolderSuggestion {
  path: string;
  /** The library this folder is, or contains — a scan of it is refused. */
  library?: string;
}

interface FolderSuggestions {
  folders: FolderSuggestion[];
  separator: string;
}

/**
 * A server path field that completes folders as it is typed (`/import/folders`).
 *
 * A combobox in the ARIA sense: the input keeps focus and the list is announced through
 * `aria-activedescendant`. ↑/↓ move, Enter or Tab take the highlighted folder, Esc closes.
 * Taking a folder appends the separator, so its children are listed next — drilling down
 * is typing nothing but the keys you already have under your fingers. Tab only completes
 * once something is highlighted, so it never traps someone tabbing past the field, and
 * Enter with nothing highlighted still submits the form.
 */
export function PathInput({
  id,
  value,
  onChange,
  placeholder,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  const [focused, setFocused] = useState(false);
  const [closed, setClosed] = useState(false);
  const [active, setActive] = useState(-1);
  const [data, setData] = useState<FolderSuggestions | null>(null);
  const settled = useDebounced(value, 150);
  const seq = useRef(0);

  useEffect(() => {
    if (!focused || !settled.trim()) {
      setData(null);
      return;
    }
    // Responses can land out of order while someone types; only the newest counts.
    const mine = ++seq.current;
    api
      .get<FolderSuggestions>(`/import/folders?path=${encodeURIComponent(settled)}`)
      .then((res) => {
        if (mine === seq.current) {
          setData(res);
          setActive(-1);
        }
      })
      .catch(() => mine === seq.current && setData(null));
  }, [settled, focused]);

  const folders = data?.folders ?? [];
  // Nothing to suggest when the only match is what is already typed.
  const onlyEcho = folders.length === 1 && folders[0].path === value;
  const open = focused && !closed && folders.length > 0 && !onlyEcho;
  const listId = `${id}-folders`;

  const take = (folder: FolderSuggestion) => {
    onChange(folder.path + (data?.separator ?? "/"));
    setClosed(false);
    setActive(-1);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (!open) {
      if (e.key === "ArrowDown" && folders.length > 0) {
        setClosed(false);
        setActive(0);
        e.preventDefault();
      }
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        setActive((i) => (i + 1) % folders.length);
        e.preventDefault();
        break;
      case "ArrowUp":
        setActive((i) => (i <= 0 ? folders.length - 1 : i - 1));
        e.preventDefault();
        break;
      case "Enter":
      case "Tab":
        if (active >= 0) {
          take(folders[active]);
          e.preventDefault();
        }
        break;
      case "Escape":
        setClosed(true);
        e.preventDefault();
        break;
    }
  };

  return (
    <div className="suggest">
      <input
        id={id}
        className="input mono"
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setClosed(false);
        }}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        role="combobox"
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && active >= 0 ? `${listId}-${active}` : undefined}
      />
      {open && (
        <ul className="suggest-list" id={listId} role="listbox">
          {folders.map((f, i) => {
            const cut = Math.max(f.path.lastIndexOf("/"), f.path.lastIndexOf("\\")) + 1;
            return (
              <li
                key={f.path}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                className={`suggest-item${i === active ? " active" : ""}`}
                // mousedown, not click: a click lands after the input's blur has closed the list.
                onMouseDown={(e) => {
                  e.preventDefault();
                  take(f);
                }}
                onMouseEnter={() => setActive(i)}
              >
                <span className="mono">
                  <span className="dim">{f.path.slice(0, cut)}</span>
                  {f.path.slice(cut)}
                </span>
                {f.library && <span className="dim suggest-note">library: {f.library}</span>}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
