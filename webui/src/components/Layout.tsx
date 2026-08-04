import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "../auth";
import { ArtworkCapabilitiesProvider } from "./Artwork";
import { Logo } from "./Logo";

/** adminOnly entries are hidden from non-admins; the API refuses them anyway. */
const NAV = [
  { to: "/", label: "Dashboard", ic: "◧", end: true },
  { to: "/libraries", label: "Libraries", ic: "▤" },
  { to: "/managers", label: "Managers", ic: "◇" },
  { to: "/data-sources", label: "Data sources", ic: "⛃" },
  { to: "/tagger-profiles", label: "Tagger profiles", ic: "✎" },
  { to: "/items", label: "Items", ic: "≣" },
  { to: "/collection", label: "Collection", ic: "♫" },
  { to: "/activity", label: "Activity", ic: "⟳" },
  { to: "/migrations", label: "Migrations", ic: "⇄" },
  { to: "/mirror", label: "Metadata", ic: "⛁" },
  { to: "/login-providers", label: "Login providers", ic: "⚿" },
  { to: "/settings", label: "Settings", ic: "⚙", adminOnly: true },
];

export default function Layout() {
  const { user, logout } = useAuth();
  const loc = useLocation();
  const nav = NAV.filter((n) => !n.adminOnly || user?.role === "admin");
  const current =
    nav.find((n) => (n.end ? loc.pathname === n.to : loc.pathname.startsWith(n.to))) ?? nav[0];

  return (
    <ArtworkCapabilitiesProvider>
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <Logo /> Autotaggerr
        </div>
        {nav.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.end} className="navitem">
            <span className="ic">{n.ic}</span>
            {n.label}
          </NavLink>
        ))}
        <div className="spacer" />
        <div className="who">
          Signed in as {user?.username}
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <span className="title">{current.label}</span>
          <button className="btn btn-ghost btn-sm" onClick={logout}>
            Sign out
          </button>
        </header>
        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
    </ArtworkCapabilitiesProvider>
  );
}
