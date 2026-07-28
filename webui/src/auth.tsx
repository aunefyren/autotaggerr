import { createContext, useContext, useEffect, useState, ReactNode } from "react";
import { api, consumeTokenFromUrl, setToken } from "./api";
import { User } from "./types";

interface AuthState {
  user: User | null;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthState>({
  user: null,
  loading: true,
  login: async () => {},
  logout: () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  // Restore the session on load. An external login lands here with its token in
  // the URL fragment, so claim that first — then /auth/me resolves the user
  // identically for password and federated logins.
  useEffect(() => {
    let active = true;
    consumeTokenFromUrl();
    api
      .get<User>("/auth/me")
      .then((u) => active && setUser(u))
      .catch(() => active && setUser(null))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, []);

  const login = async (username: string, password: string) => {
    const res = await api.post<{ token: string; user: User }>("/auth/login", { username, password });
    setToken(res.token);
    setUser(res.user);
  };

  const logout = () => {
    setToken(null);
    setUser(null);
  };

  return <AuthContext.Provider value={{ user, loading, login, logout }}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  return useContext(AuthContext);
}
