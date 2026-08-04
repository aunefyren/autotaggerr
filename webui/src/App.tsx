import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import Layout from "./components/Layout";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Libraries from "./pages/Libraries";
import Managers from "./pages/Managers";
import DataSources from "./pages/DataSources";
import AuthProviders from "./pages/AuthProviders";
import Artist from "./pages/Artist";
import ReleaseGroup from "./pages/ReleaseGroup";
import TaggerProfiles from "./pages/TaggerProfiles";
import Items from "./pages/Items";
import Collection from "./pages/Collection";
import Activity from "./pages/Activity";
import Migrations from "./pages/Migrations";
import Mirror from "./pages/Mirror";
import Settings from "./pages/Settings";

export default function App() {
  const { user, loading } = useAuth();

  if (loading) {
    return (
      <div className="login-wrap">
        <div className="muted">Loading…</div>
      </div>
    );
  }

  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route path="/login" element={<Navigate to="/" replace />} />
      <Route element={<Layout />}>
        <Route path="/" element={<Dashboard />} />
        <Route path="/libraries" element={<Libraries />} />
        <Route path="/managers" element={<Managers />} />
        <Route path="/data-sources" element={<DataSources />} />
        <Route path="/tagger-profiles" element={<TaggerProfiles />} />
        <Route path="/items" element={<Items />} />
        <Route path="/collection" element={<Collection />} />
        <Route path="/collection/:mbid" element={<Artist />} />
        <Route path="/collection/:mbid/:rgid" element={<ReleaseGroup />} />
        <Route path="/activity" element={<Activity />} />
        <Route path="/migrations" element={<Migrations />} />
        <Route path="/mirror" element={<Mirror />} />
        <Route path="/login-providers" element={<AuthProviders />} />
        {/* Settings is admin-only server-side; a non-admin who types the URL is sent
            home rather than shown a page that can only 403. */}
        <Route
          path="/settings"
          element={user.role === "admin" ? <Settings /> : <Navigate to="/" replace />}
        />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
