import { Fragment, useMemo, useState } from "react";
import { Link, Navigate, NavLink, useNavigate, useParams } from "react-router-dom";
import { api, errMsg } from "../../api";
import { useFetch } from "../../hooks";
import { SettingsField, SettingsPage, SettingsSaveResult, SettingsSection, SettingsView } from "../../types";
import { ErrorNote, Note } from "../../components/ui";
import { useToast } from "../../toast";
import { SettingRow, SettingValue } from "./fields";
import { DayStrip, Lane } from "./DayStrip";
import { EmailTest } from "./EmailTest";

/** A pending edit. Only touched fields are held, so a save sends only what changed. */
type Edits = Record<string, SettingValue>;

/**
 * Settings as an index and one page at a time, at /settings/:page. The page used to be
 * fourteen cards in one scroll; finding "when does the loudness pass run" meant
 * scrolling past SMTP. Each page is a route, so it can be linked to and Back works.
 *
 * Edits outlive the page they were made on — the shell holds them, and one save writes
 * them all, all-or-nothing, as before — so the index and the save bar say where the
 * unsaved ones are. A change made two pages ago and forgotten is otherwise the setting
 * that silently saves along with the one meant.
 */
export default function Settings() {
  const { page: pageId } = useParams();
  const navigate = useNavigate();
  const { data, err, loading, reload } = useFetch<SettingsView>(() => api.get("/settings"));
  const [edits, setEdits] = useState<Edits>({});
  const [busy, setBusy] = useState(false);
  const [lastSave, setLastSave] = useState<SettingsSaveResult | null>(null);
  const [query, setQuery] = useState("");
  const toast = useToast();

  const pages = data?.pages ?? [];
  const sections = data?.sections ?? [];
  const fields = useMemo(() => sections.flatMap((s) => s.fields.map((f) => ({ field: f, section: s }))), [sections]);
  const fieldByKey = useMemo(() => new Map(fields.map((x) => [x.field.key, x.field])), [fields]);

  const dirtyKeys = Object.keys(edits);
  const dirtyByPage = useMemo(() => {
    const out: Record<string, number> = {};
    for (const { field, section } of fields) if (field.key in edits) out[section.page] = (out[section.page] ?? 0) + 1;
    return out;
  }, [fields, edits]);

  const valueOf = (key: string) => (key in edits ? edits[key] : fieldByKey.get(key)?.value);
  const set = (key: string, value: SettingValue) => setEdits((cur) => ({ ...cur, [key]: value }));
  // A schedule whose job is switched off is still a setting, but it does not run.
  const switchedOff = (field: SettingsField) =>
    field.enabled_by && valueOf(field.enabled_by) === false ? fieldByKey.get(field.enabled_by)?.label : undefined;

  // A test instance redirects every outgoing message to the test recipient, so the
  // Email page has to say so where the sending happens.
  const testEnvironment = String(valueOf("autotaggerr_environment") ?? "").toLowerCase() === "test";

  const save = async () => {
    setBusy(true);
    try {
      const result = await api.put<SettingsSaveResult>("/settings", { values: edits });
      setEdits({});
      setLastSave(result);
      reload();
      toast(
        "ok",
        result.changed.length === 0
          ? "Nothing to save — those values were already stored"
          : `Saved ${result.changed.length} setting${result.changed.length === 1 ? "" : "s"}`,
      );
    } catch (e) {
      // The server names the field and what it expected, so show that verbatim.
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  if (data && !pages.some((p) => p.id === pageId)) {
    return <Navigate to={`/settings/${pages[0]?.id ?? ""}`} replace />;
  }
  const page = pages.find((p) => p.id === pageId);
  const searching = query.trim() !== "";

  const row = (field: SettingsField) => (
    <SettingRow
      key={field.key}
      field={field}
      edited={edits[field.key]}
      dirty={field.key in edits}
      onChange={(v) => set(field.key, v)}
      switchedOff={switchedOff(field)}
    />
  );

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Settings</h1>
        <input
          className="input settings-find"
          type="search"
          placeholder="Find a setting…"
          aria-label="Find a setting by name, config key or description"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      {err && <ErrorNote message={err} />}
      {loading && !data && <div className="muted">Loading…</div>}

      {lastSave && lastSave.applied.length > 0 && (
        <Note kind="ok" title="Applied now">
          {lastSave.applied.join(" · ")}
        </Note>
      )}
      {lastSave && lastSave.restart_required.length > 0 && (
        <Note kind="warn" title="Waiting for a restart">
          {lastSave.restart_required.join(", ")} — saved to config.json, read at the next start.
        </Note>
      )}

      {data && (
        <div className="settings-layout">
          <SettingsIndex pages={pages} current={searching ? undefined : pageId} dirty={dirtyByPage} onPick={() => setQuery("")} />
          {/* Below 760px the index is this select instead: a column of links beside the
              content leaves neither legible at phone width. */}
          <select
            className="select settings-index-select"
            aria-label="Settings page"
            value={pageId}
            onChange={(e) => {
              setQuery("");
              navigate(`/settings/${e.target.value}`);
            }}
          >
            {pages.map((p) => (
              <option key={p.id} value={p.id}>
                {p.group} · {p.title}
                {dirtyByPage[p.id] ? ` (${dirtyByPage[p.id]} unsaved)` : ""}
              </option>
            ))}
          </select>

          <div className="settings-page stack">
            {searching ? (
              <SearchResults query={query} pages={pages} sections={sections} row={row} />
            ) : (
              page && (
                <PageBody
                  page={page}
                  sections={sections.filter((s) => s.page === page.id)}
                  clock={data.clock}
                  row={row}
                  lanes={(pageSections) => lanesOf(pageSections, edits, switchedOff)}
                  extra={(section) =>
                    section.id === "email" ? (
                      <EmailTest
                        pendingEdits={dirtyKeys.some((key) => key.startsWith("smtp_") || key === "autotaggerr_test_email")}
                        testEnvironment={testEnvironment}
                      />
                    ) : section.id === "mcp" ? (
                      <p className="muted" style={{ margin: 0, fontSize: 12 }}>
                        Assistants connect with an API key. Each person makes their own under{" "}
                        <Link to="/account">Account</Link>, where the connection snippets are too.
                      </p>
                    ) : null
                  }
                />
              )
            )}
          </div>
        </div>
      )}

      {dirtyKeys.length > 0 && (
        <div className="savebar" role="status">
          <span className="savebar-count">
            {dirtyKeys.length} unsaved change{dirtyKeys.length === 1 ? "" : "s"}
            {" in "}
            {pages
              .filter((p) => dirtyByPage[p.id])
              .map((p, i, list) => (
                <Fragment key={p.id}>
                  {i > 0 && (i === list.length - 1 ? " and " : ", ")}
                  <Link to={`/settings/${p.id}`} onClick={() => setQuery("")}>
                    {p.title}
                  </Link>
                </Fragment>
              ))}
          </span>
          <button className="btn btn-ghost btn-sm" onClick={() => { setEdits({}); setLastSave(null); }} disabled={busy}>
            Discard
          </button>
          <button className="btn btn-primary btn-sm" onClick={save} disabled={busy}>
            {busy ? "Saving…" : "Save changes"}
          </button>
        </div>
      )}
    </div>
  );
}

/** The page list, grouped, with a count of unsaved edits on each page that has some. */
function SettingsIndex({
  pages,
  current,
  dirty,
  onPick,
}: {
  pages: SettingsPage[];
  current?: string;
  dirty: Record<string, number>;
  onPick: () => void;
}) {
  const groups = pages.reduce<{ title: string; pages: SettingsPage[] }[]>((out, p) => {
    const last = out[out.length - 1];
    if (last && last.title === p.group) last.pages.push(p);
    else out.push({ title: p.group, pages: [p] });
    return out;
  }, []);

  return (
    <nav className="settings-index" aria-label="Settings pages">
      {groups.map((g) => (
        <div key={g.title} className="settings-index-group">
          <div className="navgroup-title">{g.title}</div>
          {g.pages.map((p) => (
            <NavLink
              key={p.id}
              to={`/settings/${p.id}`}
              className={({ isActive }) => `settings-index-item${isActive && current ? " active" : ""}`}
              onClick={onPick}
            >
              <span>{p.title}</span>
              {dirty[p.id] ? (
                <span className="settings-index-dirty" title={`${dirty[p.id]} unsaved`}>
                  {dirty[p.id]}
                </span>
              ) : null}
            </NavLink>
          ))}
        </div>
      ))}
    </nav>
  );
}

/**
 * One page: its sections as cards. A page with a single section is that section, so
 * the card's own heading would repeat the page's and is left off.
 */
function PageBody({
  page,
  sections,
  clock,
  row,
  lanes,
  extra,
}: {
  page: SettingsPage;
  sections: SettingsSection[];
  clock: SettingsView["clock"];
  row: (field: SettingsField) => JSX.Element;
  lanes: (sections: SettingsSection[]) => Lane[];
  extra: (section: SettingsSection) => JSX.Element | null;
}) {
  const single = sections.length === 1;
  const intro = page.description ?? (single ? sections[0].description : undefined);
  const pageLanes = lanes(sections);

  return (
    <>
      <div>
        <h2 className="settings-page-title">{page.title}</h2>
        {intro && <p className="muted settings-page-intro">{intro}</p>}
      </div>
      {pageLanes.length > 1 && <DayStrip lanes={pageLanes} clock={clock} />}
      {sections.map((section) => (
        <section key={section.id} id={`section-${section.id}`} className="card stack settings-section" style={{ gap: 14 }}>
          {!single && (
            <div>
              <h3 className="section-title">{section.title}</h3>
              {section.description && <p className="muted settings-section-intro">{section.description}</p>}
            </div>
          )}
          {section.fields.map(row)}
          {extra(section)}
        </section>
      ))}
    </>
  );
}

/**
 * Every setting matching the query, by label, config key or help, under its page and
 * section — two jobs both have a "Files in parallel", and the section is what tells
 * them apart.
 */
function SearchResults({
  query,
  pages,
  sections,
  row,
}: {
  query: string;
  pages: SettingsPage[];
  sections: SettingsSection[];
  row: (field: SettingsField) => JSX.Element;
}) {
  const q = query.trim().toLowerCase();
  const matches = (f: SettingsField) => [f.label, f.key, f.help ?? ""].some((s) => s.toLowerCase().includes(q));
  const titleOf = new Map(pages.map((p) => [p.id, p.title]));
  const hits = sections
    .map((section) => ({ section, fields: section.fields.filter(matches) }))
    .filter((x) => x.fields.length > 0);

  if (hits.length === 0) {
    return (
      <p className="muted" style={{ margin: 0 }}>
        No setting matches “{query.trim()}”. Searches cover names, config keys and descriptions.
      </p>
    );
  }
  return (
    <>
      {hits.map(({ section, fields }) => {
        const pageTitle = titleOf.get(section.page) ?? section.page;
        return (
          <section key={section.id} className="card stack settings-section" style={{ gap: 14 }}>
            <h3 className="section-title">
              <Link to={`/settings/${section.page}`}>{pageTitle}</Link>
              {section.title !== pageTitle && <span className="muted"> › {section.title}</span>}
            </h3>
            {fields.map(row)}
          </section>
        );
      })}
    </>
  );
}

/** The day strip's lanes: one per schedule on the page, labelled by its job. */
function lanesOf(sections: SettingsSection[], edits: Edits, switchedOff: (f: SettingsField) => string | undefined): Lane[] {
  return sections.flatMap((section) =>
    section.fields
      .filter((f) => f.type === "cron")
      .map((field) => ({
        section,
        field,
        value: String(field.key in edits ? edits[field.key] : field.value ?? ""),
        dirty: field.key in edits,
        off: !!switchedOff(field),
      })),
  );
}
