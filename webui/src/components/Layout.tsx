import { KeyboardEvent, useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "../auth";
import { SetupProvider, SetupStep, useSetup } from "../setup";
import { ArtworkCapabilitiesProvider } from "./Artwork";
import { Logo } from "./Logo";

interface NavItem {
  to: string;
  label: string;
  ic: string;
  end?: boolean;
  /** Hidden from non-admins; the API refuses them anyway. */
  adminOnly?: boolean;
}

interface NavGroup {
  /** No title for the group of one at the top: Overview needs no heading. */
  title?: string;
  items: NavItem[];
}

/**
 * Grouped by how often each page is used, most-used first. Setup is in the order
 * things have to be created — a library picks its manager, a download client picks
 * a library — with the two that come with working defaults last.
 */
const GROUPS: NavGroup[] = [
  { items: [{ to: "/", label: "Overview", ic: "◧", end: true }] },
  {
    title: "Music",
    items: [
      { to: "/collection", label: "Collection", ic: "♫" },
      { to: "/import", label: "Import", ic: "⇣", adminOnly: true },
      { to: "/items", label: "Items", ic: "≣" },
    ],
  },
  {
    title: "Activity",
    items: [
      { to: "/activity", label: "Activity", ic: "⟳" },
      { to: "/mirror", label: "Metadata refresh", ic: "⛁" },
      { to: "/migrations", label: "MusicBrainz changes", ic: "⇄" },
    ],
  },
  {
    title: "Setup",
    items: [
      { to: "/managers", label: "Managers", ic: "◇" },
      { to: "/libraries", label: "Libraries", ic: "▤" },
      { to: "/download-clients", label: "Download clients", ic: "⤓", adminOnly: true },
      { to: "/media-servers", label: "Media servers", ic: "▷", adminOnly: true },
      { to: "/tagger-profiles", label: "Tagger profiles", ic: "✎" },
      { to: "/data-sources", label: "Data sources", ic: "⛃" },
    ],
  },
  {
    title: "System",
    items: [
      { to: "/settings", label: "Settings", ic: "⚙", adminOnly: true },
      { to: "/login-providers", label: "Login providers", ic: "⚿" },
    ],
  },
];

export default function Layout() {
  return (
    <SetupProvider>
      <ArtworkCapabilitiesProvider>
        <Shell />
      </ArtworkCapabilitiesProvider>
    </SetupProvider>
  );
}

function Shell() {
  const { user, logout } = useAuth();
  const loc = useLocation();
  const admin = user?.role === "admin";
  const groups = GROUPS.map((g) => ({ ...g, items: g.items.filter((n) => !n.adminOnly || admin) })).filter((g) => g.items.length > 0);
  const all = groups.flatMap((g) => g.items);
  const current = all.find((n) => (n.end ? loc.pathname === n.to : loc.pathname.startsWith(n.to))) ?? all[0];

  // The phone drawer. Closed by navigating, by Escape, or by the scrim; focus goes into
  // it on open and back to the button on close, and Tab stays inside while it is open.
  const [navOpen, setNavOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const sidebar = useRef<HTMLElement>(null);
  useEffect(() => setNavOpen(false), [loc.pathname]);
  useEffect(() => {
    if (navOpen) sidebar.current?.querySelector<HTMLElement>("a, button")?.focus();
  }, [navOpen]);
  const closeNav = () => {
    setNavOpen(false);
    menuButton.current?.focus();
  };
  const onSidebarKey = (e: KeyboardEvent<HTMLElement>) => {
    if (!navOpen) return;
    if (e.key === "Escape") {
      e.preventDefault();
      closeNav();
      return;
    }
    if (e.key !== "Tab" || !sidebar.current) return;
    const focusable = sidebar.current.querySelectorAll<HTMLElement>("a, button");
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <div className={`app${navOpen ? " nav-open" : ""}`}>
      <aside
        ref={sidebar}
        id="sidebar"
        className="sidebar"
        aria-label="Main"
        onKeyDown={onSidebarKey}
        {...(navOpen ? { role: "dialog", "aria-modal": true } : {})}
      >
        <div className="brand">
          <Logo /> Autotaggerr
        </div>
        {admin && <SetupRail />}
        {groups.map((g, i) => (
          <nav key={g.title ?? i} className="navgroup" aria-label={g.title ?? "Overview"}>
            {g.title && <div className="navgroup-title">{g.title}</div>}
            {g.items.map((n) => (
              <NavLink key={n.to} to={n.to} end={n.end} className="navitem">
                <span className="ic" aria-hidden="true">{n.ic}</span>
                {n.label}
              </NavLink>
            ))}
          </nav>
        ))}
        <div className="spacer" />
        <div className="who">
          <span>Signed in as {user?.username}</span>
          <button className="btn btn-ghost btn-sm" onClick={logout}>Sign out</button>
        </div>
      </aside>
      {navOpen && <div className="nav-scrim" onClick={closeNav} aria-hidden="true" />}

      <div className="main">
        <header className="topbar">
          <button
            ref={menuButton}
            type="button"
            className="btn btn-ghost btn-sm menu-btn"
            aria-label="Menu"
            aria-controls="sidebar"
            aria-expanded={navOpen}
            onClick={() => setNavOpen((o) => !o)}
          >
            ☰
          </button>
          <span className="title">{current.label}</span>
        </header>
        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/**
 * The setup steps, at the top of the sidebar until the required ones are done. The
 * one place the nav is numbered by its order, because it is the one place the order is
 * real: each step needs the one before it. Steps are never locked — a step's page says
 * what it is missing — the rail only says which comes next.
 *
 * When the last required step is done it stays a moment, all ticked, and settles away
 * on the next navigation: the finished state is worth seeing once.
 */
function SetupRail() {
  const setup = useSetup();
  const loc = useLocation();
  const [finished, setFinished] = useState(false);
  const wasIncomplete = useRef(false);

  useEffect(() => {
    if (!setup.health) return;
    if (!setup.complete) wasIncomplete.current = true;
    else if (wasIncomplete.current) {
      wasIncomplete.current = false;
      setFinished(true);
    }
  }, [setup.complete, setup.health]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setFinished(false), [loc.pathname]);

  if (!setup.health || (setup.complete && !finished)) return null;
  const steps = finished ? setup.steps.filter((s) => !s.optional || s.done) : setup.steps;

  return (
    <nav className="setup" aria-label="Set up">
      <div className="setup-title">{finished ? "You're set up" : "Set up"}</div>
      <ol className="setup-steps">
        {steps.map((s, i) => (
          <SetupStepLink key={s.key} step={s} isNext={setup.next?.key === s.key} beforeOptional={!!steps[i + 1]?.optional} />
        ))}
      </ol>
    </nav>
  );
}

function SetupStepLink({ step, isNext, beforeOptional }: { step: SetupStep; isNext: boolean; beforeOptional: boolean }) {
  const state = step.done ? "done" : isNext ? "next" : "later";
  const said = step.done ? "done" : isNext ? "next step" : step.optional ? "optional" : "not done yet";
  return (
    <li className={`setup-step ${state}${step.optional ? " optional" : ""}${beforeOptional ? " before-optional" : ""}`}>
      {/* `end` would not help: two steps share /libraries, and both are right to light up there. */}
      <NavLink to={step.to} className="setup-link" aria-label={`${step.label}, ${said}`}>
        <span className="setup-node" aria-hidden="true">{step.done ? "✓" : ""}</span>
        <span className="setup-label">
          {step.label}
          {!isNext && step.optional && !step.done && <span className="setup-aside">optional</span>}
        </span>
        {isNext && <span className="pill pill-chg">Next</span>}
      </NavLink>
    </li>
  );
}
